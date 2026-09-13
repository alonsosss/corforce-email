// Package app orquesta la reputacion de envio: la ingesta de los hechos de entrega, la
// evaluacion de cada clase, la autorizacion previa a un envio y las operaciones del
// superadmin sobre cualquier empresa.
package app

import (
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/alonsosss/corforce-email/services/reputation/internal/ports"
	"go.uber.org/zap"
)

const (
	// EntitlementTTL es cuanto se reutiliza la respuesta de billing por empresa y clase.
	EntitlementTTL = 30 * time.Second
	// ProcessedRetention: los ids de evento se guardan mucho mas que la retencion del
	// stream (7 dias), asi que una reentrega siempre encuentra su marca.
	ProcessedRetention = 30 * 24 * time.Hour
	// StatsRetentionDays: los contadores diarios se conservan mas de un ano para poder
	// explicar un estado pasado; la ventana nunca pasa de domain.MaxWindowDays.
	StatsRetentionDays = 400

	// Dependencias que la autorizacion puede no alcanzar sin denegar por ello.
	DependencyRedis   = "redis"
	DependencyBilling = "billing"
)

type Deps struct {
	Stats   ports.StatsRepository
	States  ports.StateRepository
	Limits  ports.LimitRepository
	Tx      ports.Transactor
	Events  ports.EventPublisher
	Rate    ports.RateLimiter
	Billing ports.Entitlements
	Tenants ports.TenantDirectory
	Metrics ports.Metrics
	Policy  domain.Policy
	Logger  *zap.Logger
	// Now permite fijar el reloj en las pruebas; nil = time.Now en UTC.
	Now func() time.Time
}

type UseCase struct {
	stats   ports.StatsRepository
	states  ports.StateRepository
	limits  ports.LimitRepository
	tx      ports.Transactor
	events  ports.EventPublisher
	rate    ports.RateLimiter
	billing ports.Entitlements
	tenants ports.TenantDirectory
	metrics ports.Metrics
	policy  domain.Policy
	logger  *zap.Logger
	now     func() time.Time
	ent     *entitlementCache
}

func New(d Deps) *UseCase {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &UseCase{
		stats:   d.Stats,
		states:  d.States,
		limits:  d.Limits,
		tx:      d.Tx,
		events:  d.Events,
		rate:    d.Rate,
		billing: d.Billing,
		tenants: d.Tenants,
		metrics: d.Metrics,
		policy:  d.Policy,
		logger:  d.Logger,
		now:     now,
		ent:     newEntitlementCache(EntitlementTTL),
	}
}
