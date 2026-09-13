// Package sweep programa el barrido diario de reputacion: una vez al dia, en una sola
// replica a la vez (cerrojo de lider sobre el registro).
package sweep

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/reputation/internal/app"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// lockKey serializa el barrido entre replicas (advisory lock en el registro).
const lockKey int64 = 0x72657075 // "repu"

type Runner struct {
	registry *pgxpool.Pool
	uc       *app.UseCase
	logger   *zap.Logger
	at       time.Duration
}

// New recibe at: cuanto despues de la medianoche UTC corre el barrido cada dia.
func New(registry *pgxpool.Pool, uc *app.UseCase, logger *zap.Logger, at time.Duration) *Runner {
	return &Runner{registry: registry, uc: uc, logger: logger, at: at}
}

// Run espera a la proxima hora de barrido y lo repite cada dia hasta que el contexto
// termina.
func (r *Runner) Run(ctx context.Context) {
	for {
		timer := time.NewTimer(time.Until(NextRun(time.Now(), r.at)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		r.once(ctx)
	}
}

func (r *Runner) once(ctx context.Context) {
	release, ok := db.TryLeaderLock(ctx, r.registry, lockKey)
	if !ok {
		r.logger.Info("reputation: el barrido diario lo ejecuta otra replica")
		return
	}
	defer release()
	started := time.Now()
	rep, err := r.uc.Sweep(ctx)
	if err != nil {
		r.logger.Error("reputation: barrido diario: no se pudieron listar las empresas", zap.Error(err))
		return
	}
	r.logger.Info("reputation: barrido diario completado",
		zap.Int("tenants", rep.Tenants), zap.Int("changed", rep.Changed), zap.Int("failed", rep.Failed),
		zap.Int64("events_pruned", rep.EventsPruned), zap.Int64("days_pruned", rep.DaysPruned),
		zap.Duration("took", time.Since(started)))
}

// NextRun es el siguiente instante UTC en que toca barrer: at despues de una medianoche.
func NextRun(now time.Time, at time.Duration) time.Time {
	now = now.UTC()
	y, m, d := now.Date()
	next := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Add(at)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
