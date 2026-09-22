package sweep

import (
	"context"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const anchorReportLockKey int64 = 0x61756172 // "auar"

// AnchorReportRunner envia el informe periodico de anclas (docs/adr/0006, seccion 8): en cada
// multiplo del intervalo desde la epoca UTC (con 24h, a las 00:00 UTC) recoge la ultima ancla de
// cada cadena de cada empresa y la manda en un solo correo, en una sola replica (cerrojo de lider
// sobre el registro). Alinear al reloj, y no al arranque, hace que un despliegue ni repita el
// informe ni lo salte, y que el asunto de cada dia sea predecible para quien lo archiva.
type AnchorReportRunner struct {
	registry  *pgxpool.Pool
	tenants   *db.TenantDB
	directory ports.TenantDirectory
	reporter  *app.AnchorReporter
	logger    *zap.Logger
	every     time.Duration
	now       func() time.Time
}

func NewAnchorReport(registry *pgxpool.Pool, tenants *db.TenantDB, directory ports.TenantDirectory, reporter *app.AnchorReporter, logger *zap.Logger, every time.Duration) *AnchorReportRunner {
	return &AnchorReportRunner{registry: registry, tenants: tenants, directory: directory, reporter: reporter, logger: logger, every: every, now: time.Now}
}

// NextReportAt es el siguiente multiplo del intervalo posterior a now, en UTC.
func NextReportAt(now time.Time, every time.Duration) time.Time {
	return now.UTC().Truncate(every).Add(every)
}

// Run bloquea hasta que el contexto termina.
func (r *AnchorReportRunner) Run(ctx context.Context) {
	for {
		next := NextReportAt(r.now(), r.every)
		r.logger.Info("audit: proximo informe de anclas", zap.Time("at", next))
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		r.pass(ctx)
	}
}

func (r *AnchorReportRunner) pass(ctx context.Context) {
	release, ok := db.TryLeaderLock(ctx, r.registry, anchorReportLockKey)
	if !ok {
		r.logger.Debug("audit: el informe de anclas lo envia otra replica")
		return
	}
	defer release()
	refs, err := r.directory.ActiveTenants(ctx)
	if err != nil {
		r.logger.Error("audit: informe de anclas: no se pudieron listar las empresas", zap.Error(err))
		return
	}
	slugs := make(map[uuid.UUID]string, len(refs))
	for _, t := range refs {
		slugs[t.ID] = t.Slug
	}
	var (
		mu        sync.Mutex
		collected []domain.TenantAnchors
		failed    int
	)
	err = r.tenants.ForEachActiveTenantConcurrent(ctx, tenantConcurrency, tenantBudget, func(tctx context.Context, tenantID string) {
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		ta, err := r.reporter.Collect(tctx, domain.TenantRef{ID: id, Slug: slugs[id]})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			failed++
			if ctx.Err() == nil {
				r.logger.Warn("audit: informe de anclas: empresa sin leer; sale sin ella", zap.String("tenant_id", tenantID), zap.Error(err))
			}
			return
		}
		collected = append(collected, ta)
	})
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Error("audit: informe de anclas: no se pudieron recorrer las empresas", zap.Error(err))
		}
		return
	}
	if failed > 0 {
		r.logger.Warn("audit: informe de anclas incompleto", zap.Int("tenants_unread", failed), zap.Int("tenants", len(collected)))
	}
	if err := r.reporter.SendScheduled(ctx, collected); err != nil && ctx.Err() == nil {
		r.logger.Error("audit: el informe de anclas no salio del servidor; se reintenta en el siguiente intervalo", zap.Error(err))
	}
}
