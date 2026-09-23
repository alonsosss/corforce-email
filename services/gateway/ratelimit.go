package main

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Nombres de los limitadores del gateway. Forman la clave en Redis (rl:<nombre>:ip:<ip>),
// asi que todas las replicas deben usar los mismos y cambiarlos reinicia los cupos.
const (
	apiLimiterName  = "gateway:api"
	authLimiterName = "gateway:auth"
	// exfilLimiterName es el contador de lecturas del detector de extraccion masiva (audit.go).
	exfilLimiterName = "gateway:exfil"
	// webhookLimiterName es el cupo de las rutas publicas marcadas "limit": "webhook".
	webhookLimiterName = "gateway:webhook"
)

// newRateLimitStore abre el Redis de la plataforma (REDIS_*) para los cupos del gateway,
// compartidos entre replicas. Tiempos cortos: el limitador va delante de cada peticion y,
// si Redis no responde, decide en memoria en vez de esperar. Un Redis caido al arrancar no
// impide el arranque; el cliente reconecta solo. Una configuracion TLS invalida, o ausente
// fuera de desarrollo, si lo impide.
func newRateLimitStore(logger *zap.Logger) (*middleware.RedisRateLimitStore, error) {
	rc, err := config.LoadRedis()
	if err != nil {
		return nil, err
	}
	tlsCfg, err := rc.TLSConfig()
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:                  rc.Addr(),
		Password:              rc.Password,
		TLSConfig:             tlsCfg,
		DialTimeout:           time.Second,
		ReadTimeout:           250 * time.Millisecond,
		WriteTimeout:          250 * time.Millisecond,
		PoolTimeout:           250 * time.Millisecond,
		ContextTimeoutEnabled: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Warn("gateway: Redis no disponible al arrancar; los limites se cuentan en memoria de cada replica hasta que vuelva", zap.Error(err))
	}
	return middleware.NewRedisRateLimitStore(rdb), nil
}

// newRateLimiters crea el limitador general (todo /api/v1) y el estricto de
// autenticacion (/auth y los inicios de sesion de los prefijos self_authenticated) sobre
// el mismo almacen. Ante Redis caido los dos caen a memoria con el mismo cupo por replica:
// en el estricto no se deja pasar sin limite, pero tampoco se tumba el inicio de sesion,
// que ademas tiene el bloqueo por cuenta de identity y el freno de mail-auth.
func newRateLimiters(store middleware.RateLimitStore, apiRate, authRate int, logger *zap.Logger) (api, auth *middleware.RateLimiter) {
	api = middleware.NewSharedRateLimiter(store, apiLimiterName, apiRate, time.Minute, logger)
	auth = middleware.NewSharedRateLimiter(store, authLimiterName, authRate, time.Minute, logger)
	return api, auth
}

// newExfilCounter crea el contador de lecturas por usuario del detector de extraccion
// masiva sobre el mismo almacen que los limitadores. A diferencia de ellos no rechaza
// nada: su funcion es alertar, asi que un almacen caido nunca corta una lectura; solo
// degrada el conteo a la memoria de cada replica.
func newExfilCounter(store middleware.RateLimitStore, threshold int, window time.Duration, logger *zap.Logger) *middleware.RateLimiter {
	return middleware.NewSharedRateLimiter(store, exfilLimiterName, threshold, window, logger)
}

// newWebhookLimiter es el cupo por IP de los webhooks de proveedores. Va aparte del general
// para que una rafaga de eventos no agote el cupo de las personas que comparten esa IP ni el
// general frene los eventos; sigue siendo un limite, porque la ruta es anonima.
func newWebhookLimiter(store middleware.RateLimitStore, ratePerMin int, logger *zap.Logger) *middleware.RateLimiter {
	return middleware.NewSharedRateLimiter(store, webhookLimiterName, ratePerMin, time.Minute, logger)
}
