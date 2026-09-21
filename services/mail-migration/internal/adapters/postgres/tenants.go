package postgres

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

// Tenants abre el contexto de la base de una empresa para el ejecutor, que no llega con la empresa
// resuelta por el gateway.
type Tenants struct {
	tenantDB *db.TenantDB
}

func NewTenants(tenantDB *db.TenantDB) *Tenants { return &Tenants{tenantDB: tenantDB} }

func (t *Tenants) For(ctx context.Context, tenantID uuid.UUID) (context.Context, error) {
	pool, err := t.tenantDB.ResolveForTenant(ctx, tenantID.String())
	if err != nil {
		if db.IsUnknownTenant(err) {
			return nil, domain.ErrTenantUnknown
		}
		return nil, fmt.Errorf("abrir la base de la empresa %s: %w", tenantID, err)
	}
	return db.WithTenant(ctx, pool, tenantID.String()), nil
}

func (t *Tenants) ForEach(ctx context.Context, fn func(ctx context.Context, tenantID uuid.UUID) bool) error {
	stopped := false
	return t.tenantDB.ForEachActiveTenant(ctx, func(tctx context.Context, tenantID string) {
		if stopped {
			return
		}
		id, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}
		stopped = fn(tctx, id)
	})
}
