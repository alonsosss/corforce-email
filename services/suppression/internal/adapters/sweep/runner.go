// Package sweep programa el anuncio de las exclusiones manuales que caducan: cada
// intervalo recorre las empresas activas y publica suppression.entry.expired por cada
// caducidad pendiente, en una sola replica a la vez (cerrojo de lider sobre el registro).
// El cerrojo evita trabajo repetido; la unicidad del anuncio no depende de el
// (app.UseCase.AnnounceExpired).
package sweep

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/suppression/internal/app"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const (
	// DefaultInterval es cada cuanto se buscan caducidades pendientes
	// (SUPPRESSION_EXPIRY_SWEEP_INTERVAL): lo que puede tardar en anunciarse una exclusion
	// manual que caduco. Cada pasada es una consulta por empresa sobre un indice parcial
	// que solo guarda las caducidades aun no anunciadas.
	DefaultInterval = time.Minute

	lockKey int64 = 0x73757078 // "supx"

	tenantConcurrency = 2
	tenantBudget      = 2 * time.Minute
)

type Runner struct {
	registry *pgxpool.Pool
	tenants  *db.TenantDB
	uc       *app.UseCase
	logger   *zap.Logger
	every    time.Duration
}

func New(registry *pgxpool.Pool, tenants *db.TenantDB, uc *app.UseCase, logger *zap.Logger, every time.Duration) *Runner {
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

// pass calla el resumen de una pasada sin anuncios ni fallos: corre cada pocos segundos o
// minutos y casi nunca encuentra nada.
func (r *Runner) pass(ctx context.Context) {
	release, ok := db.TryLeaderLock(ctx, r.registry, lockKey)
	if !ok {
		r.logger.Debug("suppression: el anuncio de caducidades lo ejecuta otra replica")
		return
	}
	defer release()
	started := time.Now()
	var (
		mu                   sync.Mutex
		tenants, failed, ann int
	)
	err := r.tenants.ForEachActiveTenantConcurrent(ctx, tenantConcurrency, tenantBudget, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		rep, err := r.uc.AnnounceExpired(tctx, id)
		mu.Lock()
		tenants++
		ann += rep.Announced
		if err != nil {
			failed++
		}
		mu.Unlock()
		if err != nil && ctx.Err() == nil {
			r.logger.Warn("suppression: anuncio de caducidades incompleto; se retoma en la siguiente pasada",
				zap.String("tenant_id", tenantID), zap.Error(err))
		}
	})
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Error("suppression: anuncio de caducidades: no se pudieron listar las empresas", zap.Error(err))
		}
		return
	}
	if ann > 0 || failed > 0 {
		r.logger.Info("suppression: caducidades anunciadas",
			zap.Int("tenants", tenants), zap.Int("failed", failed), zap.Int("announced", ann),
			zap.Duration("took", time.Since(started)))
	}
}
