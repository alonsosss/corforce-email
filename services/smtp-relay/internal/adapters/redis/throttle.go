// Package redis implementa sobre el Redis de la plataforma el freno de fuerza bruta y los cupos
// del relay, comunes a todas sus replicas.
package redis

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const keyPrefix = "smtp-relay:"

// ThrottleConfig son los limites del freno. Una pareja (usuario, IP) que falla es alguien
// probando una credencial; una IP que falla contra muchas es un barrido, con umbral propio.
type ThrottleConfig struct {
	MaxFailures      int64
	MaxFailuresPerIP int64
	Window           time.Duration
	LockTTL          time.Duration
}

// Throttle cuenta fallos en ventanas por caducidad y, al superar el maximo, deja un bloqueo con
// vida propia. Como el de mail-auth, deja pasar si Redis no responde: la contrasena es un secreto
// de 256 bits que no se adivina probando, y los cupos de conexiones por IP siguen contando en
// memoria de cada replica; cerrarlo dejaria sin envio SMTP a todas las empresas por una caida de
// Redis.
type Throttle struct {
	rdb    redis.UniversalClient
	cfg    ThrottleConfig
	logger *zap.Logger
}

func NewThrottle(rdb redis.UniversalClient, cfg ThrottleConfig, logger *zap.Logger) *Throttle {
	return &Throttle{rdb: rdb, cfg: cfg, logger: logger}
}

func pairKey(kind, username, ip string) string {
	return keyPrefix + kind + ":pair:" + username + "|" + ip
}

func ipKey(kind, ip string) string { return keyPrefix + kind + ":ip:" + ip }

func (t *Throttle) Blocked(ctx context.Context, username, ip string) bool {
	n, err := t.rdb.Exists(ctx, pairKey("lock", username, ip), ipKey("lock", ip)).Result()
	if err != nil {
		t.logger.Warn("smtp-relay: freno no disponible, se continua sin bloqueo", zap.Error(err))
		return false
	}
	return n > 0
}

func (t *Throttle) Failure(ctx context.Context, username, ip string) {
	t.count(ctx, pairKey("fail", username, ip), pairKey("lock", username, ip), t.cfg.MaxFailures,
		zap.String("username", username), zap.String("remote_ip", ip))
	t.count(ctx, ipKey("fail", ip), ipKey("lock", ip), t.cfg.MaxFailuresPerIP, zap.String("remote_ip", ip))
}

// count incrementa el contador de la ventana (que se fija en el primer fallo y no se renueva) y,
// al alcanzar el maximo, crea el bloqueo.
func (t *Throttle) count(ctx context.Context, failKey, lockKey string, max int64, fields ...zap.Field) {
	if max <= 0 {
		return
	}
	n, err := t.rdb.Incr(ctx, failKey).Result()
	if err != nil {
		t.logger.Warn("smtp-relay: no se pudo anotar el fallo en el freno", zap.Error(err))
		return
	}
	if n == 1 {
		if err := t.rdb.Expire(ctx, failKey, t.cfg.Window).Err(); err != nil {
			t.logger.Warn("smtp-relay: no se pudo fijar la ventana del freno", zap.Error(err))
		}
	}
	if n < max {
		return
	}
	if err := t.rdb.Set(ctx, lockKey, "1", t.cfg.LockTTL).Err(); err != nil {
		t.logger.Warn("smtp-relay: no se pudo crear el bloqueo", zap.Error(err))
		return
	}
	t.logger.Warn("smtp-relay: bloqueo por fuerza bruta",
		append(fields, zap.Int64("failures", n), zap.Duration("lock_ttl", t.cfg.LockTTL))...)
}

// Success borra el contador de la pareja; el de la IP se conserva (acertar una credencial no
// borra el barrido contra otras).
func (t *Throttle) Success(ctx context.Context, username, ip string) {
	if err := t.rdb.Del(ctx, pairKey("fail", username, ip)).Err(); err != nil {
		t.logger.Warn("smtp-relay: no se pudo limpiar el contador del freno", zap.Error(err))
	}
}

// Limits son los cupos por minuto de conexiones por IP y de mensajes por clave y por IP. Usan los
// limitadores compartidos de pkg/middleware: con Redis caido deciden en memoria de cada replica
// con el mismo cupo, nunca sin limite.
type Limits struct {
	connections *middleware.RateLimiter
	perKey      *middleware.RateLimiter
	perIP       *middleware.RateLimiter
}

// LimitsConfig son los cupos por minuto.
type LimitsConfig struct {
	ConnectionsPerIP int
	MessagesPerKey   int
	MessagesPerIP    int
}

func NewLimits(store middleware.RateLimitStore, cfg LimitsConfig, logger *zap.Logger) *Limits {
	return &Limits{
		connections: middleware.NewSharedRateLimiter(store, "smtp-relay:connections", cfg.ConnectionsPerIP, time.Minute, logger),
		perKey:      middleware.NewSharedRateLimiter(store, "smtp-relay:messages-key", cfg.MessagesPerKey, time.Minute, logger),
		perIP:       middleware.NewSharedRateLimiter(store, "smtp-relay:messages-ip", cfg.MessagesPerIP, time.Minute, logger),
	}
}

func (l *Limits) AllowConnection(ctx context.Context, ip string) bool {
	ok, _ := l.connections.AllowIP(ctx, ip)
	return ok
}

func (l *Limits) AllowMessage(ctx context.Context, keyID, ip string) bool {
	byKey, _ := l.perKey.AllowKey(ctx, keyID)
	byIP, _ := l.perIP.AllowIP(ctx, ip)
	return byKey && byIP
}
