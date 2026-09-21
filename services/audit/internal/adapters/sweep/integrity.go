package sweep

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	integrityLockKey int64 = 0x61756976 // "auiv"

	// integrityStartDelay retrasa la primera pasada: tras un despliegue el servicio atiende antes de
	// verificar, y una verificacion completa no compite con el arranque.
	integrityStartDelay = 5 * time.Minute
	// integrityTenantBudget es lo que una pasada dedica a una empresa. Una verificacion que no termina
	// queda en curso con su punto de reanudacion y la siguiente pasada la retoma.
	integrityTenantBudget = 30 * time.Minute
)

// IntegrityRunner verifica de forma periodica la cadena de cada empresa, incremental desde el ultimo
// punto bueno (con una completa cada FullEvery), de una empresa a la vez y en una sola replica (cerrojo
// de lider sobre el registro). Una cadena rota queda en el log, en las metricas y en la propia
// verificacion.
type IntegrityRunner struct {
	registry *pgxpool.Pool
	tenants  *db.TenantDB
	uc       *app.AuditUseCase
	metrics  ports.IntegrityMetrics
	logger   *zap.Logger
	every    time.Duration
}

func NewIntegrity(registry *pgxpool.Pool, tenants *db.TenantDB, uc *app.AuditUseCase, metrics ports.IntegrityMetrics, logger *zap.Logger, every time.Duration) *IntegrityRunner {
	return &IntegrityRunner{registry: registry, tenants: tenants, uc: uc, metrics: metrics, logger: logger, every: every}
}

// Run bloquea hasta que el contexto termina.
func (r *IntegrityRunner) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(integrityStartDelay):
	}
	ticker := time.NewTicker(r.every)
	defer ticker.Stop()
	for {
		r.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *IntegrityRunner) pass(ctx context.Context) {
	release, ok := db.TryLeaderLock(ctx, r.registry, integrityLockKey)
	if !ok {
		r.logger.Debug("audit: el barrido de verificacion lo ejecuta otra replica")
		return
	}
	defer release()
	var broken, failed atomic.Int64
	err := r.tenants.ForEachActiveTenantConcurrent(ctx, 1, integrityTenantBudget, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		out, err := r.uc.SweepIntegrity(tctx, id)
		switch {
		case err != nil:
			failed.Add(1)
			if ctx.Err() == nil {
				r.logger.Warn("audit: barrido de verificacion incompleto; se retoma en la siguiente pasada",
					zap.String("tenant_id", tenantID), zap.Error(err))
			}
		case out.Broken:
			broken.Add(1)
		}
	})
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Error("audit: barrido de verificacion: no se pudieron listar las empresas", zap.Error(err))
		}
		return
	}
	if ctx.Err() != nil {
		return
	}
	r.metrics.SweepBroken(int(broken.Load()))
	if broken.Load() > 0 || failed.Load() > 0 {
		r.logger.Warn("audit: pasada de verificacion con incidencias",
			zap.Int64("tenants_with_broken_chain", broken.Load()), zap.Int64("tenants_failed", failed.Load()))
	}
}
