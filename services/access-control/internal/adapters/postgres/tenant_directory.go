package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantDirectory lee el estado de una empresa por la vista que publica organization
// (organization.v_tenants), nunca por su tabla.
type TenantDirectory struct {
	pool *pgxpool.Pool
}

func NewTenantDirectory(pool *pgxpool.Pool) *TenantDirectory {
	return &TenantDirectory{pool: pool}
}

// IsActive responde false para una empresa que no esta en el registro: ya no hay nada suyo
// que proteger.
func (d *TenantDirectory) IsActive(ctx context.Context, tenantID uuid.UUID) (bool, error) {
	var active bool
	err := d.pool.QueryRow(ctx,
		`SELECT status = 'active' FROM organization.v_tenants WHERE tenant_id = $1`, tenantID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return active, err
}
