package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MTASTSRepo guarda mail.mta_sts_policies. Corre dentro de la transaccion con RLS del Transactor y
// ademas filtra por tenant_id.
type MTASTSRepo struct{ pool *db.ContextPool }

func NewMTASTSRepo(pool *db.ContextPool) *MTASTSRepo { return &MTASTSRepo{pool: pool} }

const mtaSTSColumns = `id, tenant_id, domain, mode, max_age, policy_id, created_at, updated_at`

func scanMTASTSPolicy(row pgx.Row) (domain.MTASTSPolicy, error) {
	var p domain.MTASTSPolicy
	err := row.Scan(&p.ID, &p.TenantID, &p.Domain, &p.Mode, &p.MaxAge, &p.PolicyID, &p.CreatedAt, &p.UpdatedAt)
	return p, mapErr(err)
}

func (r *MTASTSRepo) ByDomain(ctx context.Context, tenantID uuid.UUID, name string) (*domain.MTASTSPolicy, error) {
	p, err := scanMTASTSPolicy(r.pool.QueryRow(ctx,
		`SELECT `+mtaSTSColumns+` FROM mail.mta_sts_policies WHERE tenant_id = $1 AND domain = $2`, tenantID, name))
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *MTASTSRepo) States(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.MTASTSState, int64, error) {
	scan := func(row pgx.Row) (domain.MTASTSState, error) {
		var s domain.MTASTSState
		err := row.Scan(&s.Domain, &s.DomainActive, &s.Mode, &s.MaxAge, &s.PolicyID, &s.UpdatedAt)
		return s, mapErr(err)
	}
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.domains WHERE tenant_id = $1`,
		`SELECT d.domain, d.active, COALESCE(p.mode, 'none'), COALESCE(p.max_age, 0), COALESCE(p.policy_id, ''), p.updated_at
		   FROM mail.domains d
		   LEFT JOIN mail.mta_sts_policies p ON p.domain = d.domain AND p.tenant_id = d.tenant_id
		  WHERE d.tenant_id = $1 ORDER BY d.domain LIMIT $2 OFFSET $3`,
		scan, page.Limit, page.Offset, tenantID)
}

// Upsert crea la politica del dominio o la reemplaza. La restriccion UNIQUE es por dominio: solo el
// mismo tenant_id puede reemplazar su fila (RLS), y el WHERE lo repite.
func (r *MTASTSRepo) Upsert(ctx context.Context, p *domain.MTASTSPolicy) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.mta_sts_policies (id, tenant_id, domain, mode, max_age, policy_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (domain) DO UPDATE
		    SET mode = EXCLUDED.mode, max_age = EXCLUDED.max_age, policy_id = EXCLUDED.policy_id
		  WHERE mail.mta_sts_policies.tenant_id = EXCLUDED.tenant_id
		 RETURNING id, created_at, updated_at`,
		p.ID, p.TenantID, p.Domain, p.Mode, p.MaxAge, p.PolicyID,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt))
}

func (r *MTASTSRepo) DeleteByDomain(ctx context.Context, tenantID uuid.UUID, name string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.mta_sts_policies WHERE tenant_id = $1 AND domain = $2`, tenantID, name)
	return err
}

// MTASTSPublicReader lee la politica que se sirve a los remitentes. Corre fuera de TransactRLS con
// el rol de servicio, igual que MailboxLocator: quien pide es otro servidor de correo, sin empresa.
// La politica solo se sirve mientras el dominio siga activo y de la misma empresa que la escribio.
type MTASTSPublicReader struct{ pool *db.ContextPool }

func NewMTASTSPublicReader(pool *db.ContextPool) *MTASTSPublicReader {
	return &MTASTSPublicReader{pool: pool}
}

func (r *MTASTSPublicReader) Published(ctx context.Context, name string) (*domain.MTASTSPolicy, error) {
	p, err := scanMTASTSPolicy(r.pool.QueryRow(ctx,
		`SELECT p.id, p.tenant_id, p.domain, p.mode, p.max_age, p.policy_id, p.created_at, p.updated_at
		   FROM mail.mta_sts_policies p
		   JOIN mail.domains d ON d.domain = p.domain AND d.tenant_id = p.tenant_id
		  WHERE p.domain = $1 AND p.mode <> 'none' AND d.active`, name))
	if err != nil {
		return nil, err
	}
	return &p, nil
}
