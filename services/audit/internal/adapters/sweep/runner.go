// Package sweep programa el anclaje de la cabeza de las cadenas de hash: cada intervalo recorre
// las empresas activas y registra y publica la cabeza de sus cadenas, en una sola replica a la
// vez (cerrojo de lider sobre el registro). El cerrojo evita trabajo repetido; que dos replicas
// anclen la misma posicion no duplica nada (audit.chain_anchors es unica por cadena y posicion).
package sweep

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	lockKey int64 = 0x6175616e // "auan"

	tenantConcurrency = 2
	tenantBudget      = 2 * time.Minute
)

type Runner struct {
	registry *pgxpool.Pool
	tenants  *db.TenantDB
	uc       *app.AuditUseCase
	logger   *zap.Logger
	every    time.Duration
}

func New(registry *pgxpool.Pool, tenants *db.TenantDB, uc *app.AuditUseCase, logger *zap.Logger, every time.Duration) *Runner {
	return &Runner{registry: registry, tenants: tenants, uc: uc, logger: logger, every: every}
}

// Run bloquea hasta que el contexto termina.
func (r *Runner) Run(ctx context.Context) {
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

func (r *Runner) pass(ctx context.Context) {
	release, ok := db.TryLeaderLock(ctx, r.registry, lockKey)
	if !ok {
		r.logger.Debug("audit: el anclaje de las cadenas lo ejecuta otra replica")
		return
	}
	defer release()
	var (
		mu                       sync.Mutex
		tenants, failed, flagged int
	)
	err := r.tenants.ForEachActiveTenantConcurrent(ctx, tenantConcurrency, tenantBudget, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		results, err := r.uc.AnchorChains(tctx, id)
		mu.Lock()
		tenants++
		if err != nil {
			failed++
		}
		for _, res := range results {
			if res.Reason != "" {
				flagged++
			}
		}
		mu.Unlock()
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("audit: anclaje de cadenas incompleto; se retoma en la siguiente pasada",
				zap.String("tenant_id", tenantID), zap.Error(err))
		}
	})
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Error("audit: anclaje de cadenas: no se pudieron listar las empresas", zap.Error(err))
		}
		return
	}
	if failed > 0 || flagged > 0 {
		r.logger.Warn("audit: pasada de anclaje con incidencias",
			zap.Int("tenants", tenants), zap.Int("failed", failed), zap.Int("chains_missing_an_anchor", flagged))
	}
}
