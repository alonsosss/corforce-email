// Package tenants da acceso a la base de cualquier empresa a partir del registro.
package tenants

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

// Directory implementa ports.TenantDirectory sobre el enrutado por empresa de pkg/db.
type Directory struct {
	tdb         *db.TenantDB
	concurrency int
}

// NewDirectory recibe cuantas empresas se recorren a la vez en los recorridos completos.
func NewDirectory(tdb *db.TenantDB, concurrency int) *Directory {
	return &Directory{tdb: tdb, concurrency: concurrency}
}

// Scope apunta el contexto a la base de la empresa y la marca como la empresa en curso.
// Distingue la empresa que no existe (definitivo) de la base que no responde (transitorio).
func (d *Directory) Scope(ctx context.Context, tenantID uuid.UUID) (context.Context, error) {
	pool, err := d.tdb.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			return nil, domain.ErrTenantNotFound
		}
		return nil, err
	}
	return db.WithTenant(ctx, pool, tenantID.String()), nil
}

func (d *Directory) ForEachActive(ctx context.Context, perTenant time.Duration, fn func(ctx context.Context, tenantID uuid.UUID)) error {
	return d.tdb.ForEachActiveTenantConcurrent(ctx, d.concurrency, perTenant, func(tctx context.Context, id string) {
		tenantID, err := uuid.Parse(id)
		if err != nil {
			return
		}
		fn(tctx, tenantID)
	})
}
