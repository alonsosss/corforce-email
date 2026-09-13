// Package sweep programa el barrido diario que contrasta el estado de todos los contactos
// con las causas vigentes de suppression. Es la red de seguridad de los eventos de
// suppression (added, removed y expired), que llegan al menos una vez pero pueden no
// llegar a aplicarse (agotan sus reentregas y quedan en la DLQ). Corre en una sola replica
// a la vez (cerrojo de lider sobre el registro).
package sweep

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	// DefaultFullAt es cuanto despues de la medianoche UTC corre el barrido
	// (CONTACTS_FULL_SWEEP_AT).
	DefaultFullAt = 4 * time.Hour

	fullLockKey int64 = 0x636f6e66 // "conf"

	tenantConcurrency = 2
	fullTenantBudget  = 30 * time.Minute
)

type Runner struct {
	registry *pgxpool.Pool
	tenants  *db.TenantDB
	uc       *app.UseCase
	logger   *zap.Logger
	fullAt   time.Duration
}

func New(registry *pgxpool.Pool, tenants *db.TenantDB, uc *app.UseCase, logger *zap.Logger, fullAt time.Duration) *Runner {
	return &Runner{registry: registry, tenants: tenants, uc: uc, logger: logger, fullAt: fullAt}
}

// Run bloquea hasta que el contexto termina.
func (r *Runner) Run(ctx context.Context) {
	for {
		timer := time.NewTimer(time.Until(NextRun(time.Now(), r.fullAt)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		r.pass(ctx)
	}
}

// pass barre las empresas activas.
func (r *Runner) pass(ctx context.Context) {
	release, ok := db.TryLeaderLock(ctx, r.registry, fullLockKey)
	if !ok {
		r.logger.Debug("contacts: el barrido de suppression lo ejecuta otra replica")
		return
	}
	defer release()
	started := time.Now()
	var (
		mu                       sync.Mutex
		tenants, failed          int
		checked, changedContacts int
	)
	err := r.tenants.ForEachActiveTenantConcurrent(ctx, tenantConcurrency, fullTenantBudget, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		rep, err := r.uc.SweepSuppression(tctx, id)
		mu.Lock()
		tenants++
		checked += rep.Checked
		changedContacts += rep.Changed
		if err != nil {
			failed++
		}
		mu.Unlock()
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("contacts: barrido de suppression incompleto; se retoma en la siguiente pasada",
				zap.String("tenant_id", tenantID), zap.Error(err))
		}
	})
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Error("contacts: barrido de suppression: no se pudieron listar las empresas", zap.Error(err))
		}
		return
	}
	r.logger.Info("contacts: barrido de suppression completado",
		zap.Int("tenants", tenants), zap.Int("failed", failed), zap.Int("checked", checked),
		zap.Int("changed", changedContacts), zap.Duration("took", time.Since(started)))
}

// NextRun es el siguiente instante UTC en que toca el barrido: at despues de una
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
