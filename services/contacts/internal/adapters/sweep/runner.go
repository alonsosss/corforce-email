// Package sweep programa los barridos que contrastan el estado de los contactos con las
// causas vigentes de suppression: el de caducidad (solo los excluded, cada poco, porque
// una exclusion manual caduca sin evento) y el completo (todos, una vez al dia). Cada uno
// corre en una sola replica a la vez (cerrojo de lider sobre el registro).
package sweep

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	// DefaultExpiryInterval es cada cuanto se revisan los excluded
	// (CONTACTS_EXPIRY_SWEEP_INTERVAL): lo que puede tardar en volver un contacto cuya
	// exclusion manual caduco.
	DefaultExpiryInterval = 5 * time.Minute
	// DefaultFullAt es cuanto despues de la medianoche UTC corre el barrido completo
	// (CONTACTS_FULL_SWEEP_AT).
	DefaultFullAt = 4 * time.Hour

	expiryLockKey int64 = 0x636f6e65 // "cone"
	fullLockKey   int64 = 0x636f6e66 // "conf"

	tenantConcurrency  = 2
	expiryTenantBudget = 2 * time.Minute
	fullTenantBudget   = 30 * time.Minute
)

type Runner struct {
	registry *pgxpool.Pool
	tenants  *db.TenantDB
	uc       *app.UseCase
	logger   *zap.Logger
	every    time.Duration
	fullAt   time.Duration
}

func New(registry *pgxpool.Pool, tenants *db.TenantDB, uc *app.UseCase, logger *zap.Logger, expiryEvery, fullAt time.Duration) *Runner {
	return &Runner{registry: registry, tenants: tenants, uc: uc, logger: logger, every: expiryEvery, fullAt: fullAt}
}

// Run bloquea hasta que el contexto termina.
func (r *Runner) Run(ctx context.Context) {
	go r.runExpiry(ctx)
	r.runFull(ctx)
}

func (r *Runner) runExpiry(ctx context.Context) {
	ticker := time.NewTicker(r.every)
	defer ticker.Stop()
	for {
		r.pass(ctx, expiryLockKey, domain.StatusExcluded, expiryTenantBudget, false)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) runFull(ctx context.Context) {
	for {
		timer := time.NewTimer(time.Until(NextRun(time.Now(), r.fullAt)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		r.pass(ctx, fullLockKey, "", fullTenantBudget, true)
	}
}

// pass barre las empresas activas. Sin alwaysLog calla el resumen de una pasada sin
// cambios ni fallos: la de caducidad corre cada pocos minutos.
func (r *Runner) pass(ctx context.Context, key int64, only domain.Status, budget time.Duration, alwaysLog bool) {
	log := r.logger.With(zap.String("scope", scopeName(only)))
	release, ok := db.TryLeaderLock(ctx, r.registry, key)
	if !ok {
		log.Debug("contacts: el barrido de suppression lo ejecuta otra replica")
		return
	}
	defer release()
	started := time.Now()
	var (
		mu                       sync.Mutex
		tenants, failed          int
		checked, changedContacts int
	)
	err := r.tenants.ForEachActiveTenantConcurrent(ctx, tenantConcurrency, budget, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		rep, err := r.uc.SweepSuppression(tctx, id, only)
		mu.Lock()
		tenants++
		checked += rep.Checked
		changedContacts += rep.Changed
		if err != nil {
			failed++
		}
		mu.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Warn("contacts: barrido de suppression incompleto; se retoma en la siguiente pasada",
				zap.String("tenant_id", tenantID), zap.Error(err))
		}
	})
	if err != nil {
		if ctx.Err() == nil {
			log.Error("contacts: barrido de suppression: no se pudieron listar las empresas", zap.Error(err))
		}
		return
	}
	if alwaysLog || changedContacts > 0 || failed > 0 {
		log.Info("contacts: barrido de suppression completado",
			zap.Int("tenants", tenants), zap.Int("failed", failed), zap.Int("checked", checked),
			zap.Int("changed", changedContacts), zap.Duration("took", time.Since(started)))
	}
}

func scopeName(only domain.Status) string {
	if only == "" {
		return "all"
	}
	return string(only)
}

// NextRun es el siguiente instante UTC en que toca el barrido completo: at despues de una
// medianoche.
func NextRun(now time.Time, at time.Duration) time.Time {
	now = now.UTC()
	y, m, d := now.Date()
	next := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Add(at)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
