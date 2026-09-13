// Package redis implementa el freno de fuerza bruta de mail-auth sobre Redis.
package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const keyPrefix = "mail-auth:"

// Config son los limites del freno. Hay dos maximos porque una pareja (usuario, IP)
// que falla diez veces es alguien probando una cuenta, mientras que una IP que falla
// contra muchas cuentas es una oficina detras de un NAT o un barrido; el segundo umbral
// es mas alto para no bloquear a toda una empresa por unos cuantos olvidos.
type Config struct {
	MaxFailures      int64
	MaxFailuresPerIP int64
	Window           time.Duration
	LockTTL          time.Duration
}

// Throttle cuenta fallos por (usuario, IP) y por IP en ventanas deslizantes por
// caducidad y, al superar el maximo, deja una clave de bloqueo con vida propia.
//
// Fail-open a proposito: si Redis no responde, Blocked devuelve false y los fallos se
// pierden. La capa de red la aporta netfilter a partir de los fallos que Dovecot
// escribe en el log, asi que perder este freno degrada la defensa pero no la elimina;
// cerrarlo, en cambio, dejaria sin correo a toda la celda por una caida de Redis.
type Throttle struct {
	rdb    *redis.Client
	cfg    Config
	logger *zap.Logger
}

func NewThrottle(rdb *redis.Client, cfg Config, logger *zap.Logger) *Throttle {
	return &Throttle{rdb: rdb, cfg: cfg, logger: logger}
}

func pairKey(kind, username, ip string) string {
	return keyPrefix + kind + ":pair:" + username + "|" + ip
}

func ipKey(kind, ip string) string {
	return keyPrefix + kind + ":ip:" + ip
}

func (t *Throttle) Blocked(ctx context.Context, username, ip string) bool {
	n, err := t.rdb.Exists(ctx, pairKey("lock", username, ip), ipKey("lock", ip)).Result()
	if err != nil {
		t.logger.Warn("mail-auth: freno no disponible, se continua sin bloqueo", zap.Error(err))
		return false
	}
	return n > 0
}

func (t *Throttle) Failure(ctx context.Context, username, ip string) {
	t.count(ctx, pairKey("fail", username, ip), pairKey("lock", username, ip), t.cfg.MaxFailures,
		zap.String("username", username), zap.String("remote_ip", ip))
	t.count(ctx, ipKey("fail", ip), ipKey("lock", ip), t.cfg.MaxFailuresPerIP,
		zap.String("remote_ip", ip))
}

// count incrementa el contador dentro de la ventana y, al alcanzar el maximo, crea el
// bloqueo. El EXPIRE solo se fija en el primer fallo para que la ventana no se renueve
// con cada intento.
func (t *Throttle) count(ctx context.Context, failKey, lockKey string, max int64, fields ...zap.Field) {
	if max <= 0 {
		return
	}
	n, err := t.rdb.Incr(ctx, failKey).Result()
	if err != nil {
		t.logger.Warn("mail-auth: no se pudo anotar el fallo en el freno", zap.Error(err))
		return
	}
	if n == 1 {
		if err := t.rdb.Expire(ctx, failKey, t.cfg.Window).Err(); err != nil {
			t.logger.Warn("mail-auth: no se pudo fijar la ventana del freno", zap.Error(err))
		}
	}
	if n < max {
		return
	}
	if err := t.rdb.Set(ctx, lockKey, "1", t.cfg.LockTTL).Err(); err != nil {
		t.logger.Warn("mail-auth: no se pudo crear el bloqueo", zap.Error(err))
		return
	}
	t.logger.Warn("mail-auth: bloqueo por fuerza bruta",
		append(fields, zap.Int64("failures", n), zap.Duration("lock_ttl", t.cfg.LockTTL))...)
}

// Success borra el contador de la pareja: un inicio correcto demuestra que quien esta
// detras conoce la contrasena. El contador de la IP se conserva, porque una IP que
// acierta una cuenta y falla contra otras sigue siendo un barrido.
func (t *Throttle) Success(ctx context.Context, username, ip string) {
	if err := t.rdb.Del(ctx, pairKey("fail", username, ip)).Err(); err != nil {
		t.logger.Warn("mail-auth: no se pudo limpiar el contador del freno", zap.Error(err))
	}
}
