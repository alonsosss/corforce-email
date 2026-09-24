// Package prune programa la poda de la proyeccion de interaccion (contacts.engagement):
// cada Interval borra, empresa por empresa, lo que no tiene actividad dentro de la
// retencion. Corre en una sola replica a la vez (cerrojo de lider sobre el registro).
package prune

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	// Interval es cada cuanto se poda; la retencion se mide en dias, asi que basta.
	Interval = 6 * time.Hour

	lockKey int64 = 0x636f6e65 // "cone"

	tenantConcurrency = 2
	tenantBudget      = 10 * time.Minute
)

type Runner struct {
	registry *pgxpool.Pool
	tenants  *db.TenantDB
	uc       *app.UseCase
	logger   *zap.Logger
}

func New(registry *pgxpool.Pool, tenants *db.TenantDB, uc *app.UseCase, logger *zap.Logger) *Runner {
	return &Runner{registry: registry, tenants: tenants, uc: uc, logger: logger}
}

// Run bloquea hasta que el contexto termina.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.pass(ctx)
	}
}

func (r *Runner) pass(ctx context.Context) {
	release, ok := db.TryLeaderLock(ctx, r.registry, lockKey)
	if !ok {
		return
	}
	defer release()
	err := r.tenants.ForEachActiveTenantConcurrent(ctx, tenantConcurrency, tenantBudget, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		n, err := r.uc.PruneEngagement(tctx, id)
		if err != nil && tctx.Err() == nil {
			r.logger.Warn("contacts: poda de la interaccion incompleta", zap.String("tenant_id", tenantID), zap.Error(err))
			return
		}
		if n > 0 {
			r.logger.Info("contacts: interaccion podada", zap.String("tenant_id", tenantID), zap.Int64("filas", n))
		}
	})
	if err != nil && ctx.Err() == nil {
		r.logger.Warn("contacts: no se pudo listar las empresas para la poda", zap.Error(err))
	}
}
