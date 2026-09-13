package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MailDomainRepo es el indice global de los dominios de correo activos
// (organization.mail_domain_cells, migracion 029).
type MailDomainRepo struct {
	pool *pgxpool.Pool
}

func NewMailDomainRepo(pool *pgxpool.Pool) *MailDomainRepo {
	return &MailDomainRepo{pool: pool}
}

// Claim inserta el dominio para la empresa o, si ya es suyo, solo sella updated_at. Si es de otra
// empresa la condicion del ON CONFLICT no se cumple y no vuelve ninguna fila: una sola sentencia,
// sin carrera entre dos empresas que lo reclaman a la vez.
func (r *MailDomainRepo) Claim(ctx context.Context, name string, tenantID uuid.UUID) error {
	var owner uuid.UUID
	err := r.pool.QueryRow(ctx,
		`INSERT INTO organization.mail_domain_cells (domain, tenant_id) VALUES ($1, $2)
 ON CONFLICT (domain) DO UPDATE SET updated_at = NOW()
 WHERE organization.mail_domain_cells.tenant_id = EXCLUDED.tenant_id
 RETURNING tenant_id`,
		name, tenantID,
	).Scan(&owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrMailDomainClaimed
	case pgErrorCode(err) == pgForeignKeyViolation:
		return domain.ErrTenantNotFound
	case err != nil:
		return fmt.Errorf("reclamar el dominio %s: %w", name, err)
	}
	return nil
}

func (r *MailDomainRepo) Release(ctx context.Context, name string, tenantID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM organization.mail_domain_cells WHERE domain = $1 AND tenant_id = $2`,
		name, tenantID,
	)
	if err != nil {
		return false, fmt.Errorf("retirar el dominio %s: %w", name, err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *MailDomainRepo) TenantOf(ctx context.Context, name string) (uuid.UUID, error) {
	var tenantID uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id FROM organization.mail_domain_cells WHERE domain = $1`, name,
	).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrMailDomainNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("empresa del dominio %s: %w", name, err)
	}
	return tenantID, nil
}
