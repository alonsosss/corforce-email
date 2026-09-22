package postgres

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RegistryTenants lee las empresas activas de la vista publicada de enrutado del registro, la
// unica del registro que el rol de audit puede leer (tenant_router, migracion 031). Solo id y
// slug: lo que el informe de anclas necesita para nombrar a cada empresa.
type RegistryTenants struct{ pool *pgxpool.Pool }

func NewRegistryTenants(pool *pgxpool.Pool) *RegistryTenants { return &RegistryTenants{pool: pool} }

func (r *RegistryTenants) ActiveTenants(ctx context.Context) ([]domain.TenantRef, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT tenant_id, COALESCE(slug, '') FROM organization.v_tenant_routing WHERE status = 'active' ORDER BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("listar empresas activas: %w", err)
	}
	defer rows.Close()
	var out []domain.TenantRef
	for rows.Next() {
		var t domain.TenantRef
		if err := rows.Scan(&t.ID, &t.Slug); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
