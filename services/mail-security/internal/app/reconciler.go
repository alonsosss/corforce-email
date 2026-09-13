package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Reconciler corre en segundo plano: al arrancar y cada intervalo recalcula las claves
// de Redis desde la base y poda la cuarentena caducada de cada empresa con su propio
// max_age_days (Q_MAX_AGE en Redis es solo el tope de la celda).
type Reconciler struct {
	sync       *RedisSync
	policy     ports.PolicyReader
	quarantine ports.QuarantineRepository
	interval   time.Duration
	logger     *zap.Logger
}

func NewReconciler(sync *RedisSync, policy ports.PolicyReader, quarantine ports.QuarantineRepository, interval time.Duration, logger *zap.Logger) *Reconciler {
	return &Reconciler{sync: sync, policy: policy, quarantine: quarantine, interval: interval, logger: logger}
}

// Run bloquea hasta que el contexto se cancele.
func (r *Reconciler) Run(ctx context.Context) {
	r.tick(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Reconciler) tick(ctx context.Context) {
	tctx, cancel := context.WithTimeout(ctx, r.interval)
	defer cancel()
	if err := r.sync.ReconcileAll(tctx); err != nil {
		r.logger.Warn("reconciliacion de redis incompleta", zap.Error(err))
	} else {
		r.logger.Info("claves de redis reconciliadas")
	}
	r.pruneAged(tctx)
}

func (r *Reconciler) pruneAged(ctx context.Context) {
	all, err := r.policy.AllQuarantineSettings(ctx)
	if err != nil {
		r.logger.Warn("ajustes de cuarentena", zap.Error(err))
		return
	}
	for _, s := range all {
		if n, err := r.quarantine.PruneAged(ctx, s.TenantID, s.MaxAgeDays); err != nil {
			r.logger.Warn("poda de cuarentena caducada", zap.String("tenant", s.TenantID.String()), zap.Error(err))
		} else if n > 0 {
			r.logger.Info("cuarentena caducada podada", zap.String("tenant", s.TenantID.String()), zap.Int64("filas", n))
		}
	}
	// Las empresas sin fila propia se podan con el defecto: PruneAged con tenant nulo
	// cubre a todas las que no tienen ajustes.
	if n, err := r.quarantine.PruneAged(ctx, uuid.Nil, domain.DefaultQuarantineMaxAgeDays); err != nil {
		r.logger.Warn("poda de cuarentena caducada (defecto)", zap.Error(err))
	} else if n > 0 {
		r.logger.Info("cuarentena caducada podada (defecto)", zap.Int64("filas", n))
	}
}
