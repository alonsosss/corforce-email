// Package formguard implementa el freno anti abuso de los formularios publicos sobre los
// limitadores de pkg/middleware: el mismo Redis de la plataforma que los cupos del gateway,
// comun a todas las replicas, y la memoria del proceso como respaldo si Redis no responde.
package formguard

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Nombres de los limitadores: forman la clave en Redis (rl:<nombre>:...), asi que todas las
// replicas usan los mismos y cambiarlos reinicia los cupos.
const (
	ipLimiterName    = "contacts:form-ip"
	formLimiterName  = "contacts:form"
	nonceLimiterName = "contacts:form-nonce"
)

// Config son los cupos: PerIP envios por IP en IPWindow, PerFormHour envios por formulario y
// hora, y TokenTTL la vigencia del token, que es cuanto se recuerda un nonce usado.
type Config struct {
	PerIP       int
	IPWindow    time.Duration
	PerFormHour int
	TokenTTL    time.Duration
}

type Guard struct {
	ip    *middleware.RateLimiter
	form  *middleware.RateLimiter
	nonce *middleware.RateLimiter
}

// New arma los tres limitadores sobre store. El de nonces admite una sola operacion por clave
// en la vigencia del token: la segunda vez que llega el mismo nonce ya no cabe.
func New(store middleware.RateLimitStore, cfg Config, logger *zap.Logger) *Guard {
	return &Guard{
		ip:    middleware.NewSharedRateLimiter(store, ipLimiterName, cfg.PerIP, cfg.IPWindow, logger),
		form:  middleware.NewSharedRateLimiter(store, formLimiterName, cfg.PerFormHour, time.Hour, logger),
		nonce: middleware.NewSharedRateLimiter(store, nonceLimiterName, 1, cfg.TokenTTL, logger),
	}
}

func (g *Guard) AllowIP(ctx context.Context, ip string) (bool, time.Duration) {
	return g.ip.AllowIP(ctx, ip)
}

func (g *Guard) AllowForm(ctx context.Context, tenantID, formID uuid.UUID) (bool, time.Duration) {
	return g.form.AllowKey(ctx, tenantID.String()+"/"+formID.String())
}

func (g *Guard) FirstUse(ctx context.Context, nonce string) bool {
	ok, _ := g.nonce.AllowKey(ctx, nonce)
	return ok
}
