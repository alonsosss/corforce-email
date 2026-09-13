package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository persiste en el esquema domains de la base de la empresa. El pool llega en
// el contexto (TenantPoolMiddleware en las peticiones, ForEachActiveTenant en el
// barrido); todas las consultas filtran ademas por tenant_id.
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository {
	return &Repository{pool: pool}
}

const domainColumns = `id, tenant_id, domain, purpose, status, verification_token, verified_at, last_checked_at,
 dkim_selector, dkim_private_key_enc, dkim_public_key, dkim_key_bits,
 dkim_previous_selector, dkim_previous_private_key_enc, dkim_previous_public_key, dkim_rotated_at,
 dmarc_policy, directory_deactivation_pending, created_at, updated_at`

func scanDomain(row pgx.Row) (*domain.Domain, error) {
	d := &domain.Domain{}
	var prevSelector, prevPublic *string
	err := row.Scan(
		&d.ID, &d.TenantID, &d.Domain, &d.Purpose, &d.Status, &d.VerificationToken, &d.VerifiedAt, &d.LastCheckedAt,
		&d.DKIMSelector, &d.DKIMPrivateKeyEnc, &d.DKIMPublicKey, &d.DKIMKeyBits,
		&prevSelector, &d.DKIMPreviousPrivateKeyEnc, &prevPublic, &d.DKIMRotatedAt,
		&d.DMARCPolicy, &d.DirectoryDeactivationPending, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrDomainNotFound
		}
		return nil, err
	}
	if prevSelector != nil {
		d.DKIMPreviousSelector = *prevSelector
	}
	if prevPublic != nil {
		d.DKIMPreviousPublicKey = *prevPublic
	}
	return d, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// isUniqueViolation reconoce el choque con domains_tenant_domain_key.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (r *Repository) Create(ctx context.Context, d *domain.Domain) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO domains.domains (id, tenant_id, domain, purpose, status, verification_token,
 dkim_selector, dkim_private_key_enc, dkim_public_key, dkim_key_bits, dmarc_policy)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING created_at, updated_at`,
		d.ID, d.TenantID, d.Domain, d.Purpose, d.Status, d.VerificationToken,
		d.DKIMSelector, d.DKIMPrivateKeyEnc, d.DKIMPublicKey, d.DKIMKeyBits, d.DMARCPolicy,
	).Scan(&d.CreatedAt, &d.UpdatedAt)
	if isUniqueViolation(err) {
		return domain.ErrDomainAlreadyExists
	}
	return err
}

func (r *Repository) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Domain, error) {
	return scanDomain(r.pool.QueryRow(ctx,
		`SELECT `+domainColumns+` FROM domains.domains WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *Repository) GetByName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	return scanDomain(r.pool.QueryRow(ctx,
		`SELECT `+domainColumns+` FROM domains.domains WHERE tenant_id = $1 AND domain = $2`, tenantID, name))
}

func (r *Repository) List(ctx context.Context, tenantID uuid.UUID, offset, limit int) ([]*domain.Domain, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM domains.domains WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains WHERE tenant_id = $1 ORDER BY domain LIMIT $2 OFFSET $3`,
		tenantID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	domains, err := collect(rows)
	return domains, total, err
}

func collect(rows pgx.Rows) ([]*domain.Domain, error) {
	var out []*domain.Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repository) Update(ctx context.Context, d *domain.Domain) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE domains.domains SET purpose = $3, status = $4, verified_at = $5, last_checked_at = $6,
 dkim_selector = $7, dkim_private_key_enc = $8, dkim_public_key = $9, dkim_key_bits = $10,
 dkim_previous_selector = $11, dkim_previous_private_key_enc = $12, dkim_previous_public_key = $13,
 dkim_rotated_at = $14, dmarc_policy = $15, directory_deactivation_pending = $16
 WHERE tenant_id = $1 AND id = $2`,
		d.TenantID, d.ID, d.Purpose, d.Status, d.VerifiedAt, d.LastCheckedAt,
		d.DKIMSelector, d.DKIMPrivateKeyEnc, d.DKIMPublicKey, d.DKIMKeyBits,
		nullable(d.DKIMPreviousSelector), d.DKIMPreviousPrivateKeyEnc, nullable(d.DKIMPreviousPublicKey),
		d.DKIMRotatedAt, d.DMARCPolicy, d.DirectoryDeactivationPending,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDomainNotFound
	}
	return nil
}

// Delete borra el dominio; las comprobaciones caen por ON DELETE CASCADE.
func (r *Repository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM domains.domains WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrDomainNotFound
	}
	return nil
}

func (r *Repository) ListForRecheck(ctx context.Context, tenantID uuid.UUID, pendingSince time.Time) ([]*domain.Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains
 WHERE tenant_id = $1 AND (status = $2 OR (status = $3 AND created_at >= $4))
 ORDER BY last_checked_at NULLS FIRST, domain`,
		tenantID, domain.StatusVerified, domain.StatusPending, pendingSince)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

func (r *Repository) ListWithExpiredPreviousDKIM(ctx context.Context, tenantID uuid.UUID, rotatedBefore time.Time) ([]*domain.Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains
 WHERE tenant_id = $1 AND dkim_previous_selector IS NOT NULL AND dkim_rotated_at < $2
 ORDER BY dkim_rotated_at`,
		tenantID, rotatedBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

func (r *Repository) ListPendingDeactivation(ctx context.Context, tenantID uuid.UUID) ([]*domain.Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM domains.domains
 WHERE tenant_id = $1 AND directory_deactivation_pending
 ORDER BY domain`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

// SaveChecks inserta las comprobaciones de una verificacion en una sola transaccion:
// o queda la foto completa o no queda nada.
func (r *Repository) SaveChecks(ctx context.Context, checks []domain.DNSCheck) error {
	if len(checks) == 0 {
		return nil
	}
	return r.pool.Transact(ctx, func(ctx context.Context) error {
		for i := range checks {
			c := &checks[i]
			if c.ID == uuid.Nil {
				c.ID = uuid.New()
			}
			if _, err := r.pool.Exec(ctx,
				`INSERT INTO domains.dns_checks (id, tenant_id, domain_id, checked_at, record, expected, observed, ok, detail)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				c.ID, c.TenantID, c.DomainID, c.CheckedAt, c.Record, c.Expected, c.Observed, c.OK, c.Detail,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// LatestChecks devuelve la comprobacion mas reciente de cada registro del dominio.
func (r *Repository) LatestChecks(ctx context.Context, tenantID, domainID uuid.UUID) ([]domain.DNSCheck, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT ON (record) id, tenant_id, domain_id, checked_at, record, expected, observed, ok, detail
 FROM domains.dns_checks WHERE tenant_id = $1 AND domain_id = $2
 ORDER BY record, checked_at DESC`,
		tenantID, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DNSCheck
	for rows.Next() {
		var c domain.DNSCheck
		if err := rows.Scan(&c.ID, &c.TenantID, &c.DomainID, &c.CheckedAt, &c.Record, &c.Expected, &c.Observed, &c.OK, &c.Detail); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) PruneChecks(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM domains.dns_checks WHERE tenant_id = $1 AND checked_at < $2`, tenantID, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
