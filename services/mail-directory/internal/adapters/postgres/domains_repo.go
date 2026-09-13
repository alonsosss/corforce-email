package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type DomainRepo struct {
	pool *db.ContextPool
}

func NewDomainRepo(pool *db.ContextPool) *DomainRepo { return &DomainRepo{pool: pool} }

const domainColumns = `id, tenant_id, domain, description, active, backupmx, relay_all_recipients,
 relay_unknown_only, relayhost_id, max_aliases, max_mailboxes, default_quota_bytes, max_quota_bytes,
 quota_bytes, created_at, updated_at`

func scanDomain(row pgx.Row) (domain.Domain, error) {
	var d domain.Domain
	err := row.Scan(&d.ID, &d.TenantID, &d.Domain, &d.Description, &d.Active, &d.BackupMX, &d.RelayAllRecipients,
		&d.RelayUnknownOnly, &d.RelayhostID, &d.MaxAliases, &d.MaxMailboxes, &d.DefaultQuotaBytes, &d.MaxQuotaBytes,
		&d.QuotaBytes, &d.CreatedAt, &d.UpdatedAt)
	return d, mapErr(err)
}

func (r *DomainRepo) List(ctx context.Context, tenantID uuid.UUID, filter ports.DomainFilter, page ports.Page) ([]domain.Domain, int64, error) {
	const where = ` FROM mail.domains WHERE tenant_id = $1 AND ($2 = '' OR domain ILIKE $2 ESCAPE '\')`
	return listPage(ctx, r.pool,
		`SELECT COUNT(*)`+where,
		`SELECT `+domainColumns+where+` ORDER BY domain LIMIT $3 OFFSET $4`,
		scanDomain, page.Limit, page.Offset, tenantID, likePattern(filter.Search))
}

func (r *DomainRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Domain, error) {
	d, err := scanDomain(r.pool.QueryRow(ctx,
		`SELECT `+domainColumns+` FROM mail.domains WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DomainRepo) GetByName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	d, err := scanDomain(r.pool.QueryRow(ctx,
		`SELECT `+domainColumns+` FROM mail.domains WHERE tenant_id = $1 AND domain = $2`, tenantID, name))
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *DomainRepo) Create(ctx context.Context, d *domain.Domain) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.domains (id, tenant_id, domain, description, active, backupmx, relay_all_recipients,
 relay_unknown_only, relayhost_id, max_aliases, max_mailboxes, default_quota_bytes, max_quota_bytes, quota_bytes)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING created_at, updated_at`,
		d.ID, d.TenantID, d.Domain, d.Description, d.Active, d.BackupMX, d.RelayAllRecipients,
		d.RelayUnknownOnly, d.RelayhostID, d.MaxAliases, d.MaxMailboxes, d.DefaultQuotaBytes, d.MaxQuotaBytes, d.QuotaBytes,
	).Scan(&d.CreatedAt, &d.UpdatedAt))
}

func (r *DomainRepo) Update(ctx context.Context, d *domain.Domain) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.domains SET description = $3, active = $4, backupmx = $5, relay_all_recipients = $6,
 relay_unknown_only = $7, relayhost_id = $8, max_aliases = $9, max_mailboxes = $10, default_quota_bytes = $11,
 max_quota_bytes = $12, quota_bytes = $13
 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		d.TenantID, d.ID, d.Description, d.Active, d.BackupMX, d.RelayAllRecipients, d.RelayUnknownOnly,
		d.RelayhostID, d.MaxAliases, d.MaxMailboxes, d.DefaultQuotaBytes, d.MaxQuotaBytes, d.QuotaBytes,
	).Scan(&d.UpdatedAt))
}

func (r *DomainRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.domains WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

// NameInUse consulta la funcion SECURITY DEFINER de la 03: responde si/no sobre toda la
// celda sin exponer filas de otras empresas.
func (r *DomainRepo) NameInUse(ctx context.Context, name string) (bool, error) {
	var inUse bool
	err := r.pool.QueryRow(ctx, `SELECT mail.name_in_use($1)`, name).Scan(&inUse)
	return inUse, err
}

func (r *DomainRepo) Usage(ctx context.Context, tenantID uuid.UUID, name string) (mailboxes, aliases, aliasDomains int64, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT (SELECT COUNT(*) FROM mail.mailboxes WHERE tenant_id = $1 AND domain = $2),
        (SELECT COUNT(*) FROM mail.aliases WHERE tenant_id = $1 AND domain = $2),
        (SELECT COUNT(*) FROM mail.alias_domains WHERE tenant_id = $1 AND target_domain = $2)`,
		tenantID, name).Scan(&mailboxes, &aliases, &aliasDomains)
	return mailboxes, aliases, aliasDomains, err
}

// ── Dominios alias ────────────────────────────────────────────────────────────

type AliasDomainRepo struct {
	pool *db.ContextPool
}

func NewAliasDomainRepo(pool *db.ContextPool) *AliasDomainRepo { return &AliasDomainRepo{pool: pool} }

const aliasDomainColumns = `id, tenant_id, alias_domain, target_domain, active, created_at, updated_at`

func scanAliasDomain(row pgx.Row) (domain.AliasDomain, error) {
	var a domain.AliasDomain
	err := row.Scan(&a.ID, &a.TenantID, &a.AliasDomain, &a.TargetDomain, &a.Active, &a.CreatedAt, &a.UpdatedAt)
	return a, mapErr(err)
}

func (r *AliasDomainRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.AliasDomain, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.alias_domains WHERE tenant_id = $1`,
		`SELECT `+aliasDomainColumns+` FROM mail.alias_domains WHERE tenant_id = $1 ORDER BY alias_domain LIMIT $2 OFFSET $3`,
		scanAliasDomain, page.Limit, page.Offset, tenantID)
}

func (r *AliasDomainRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.AliasDomain, error) {
	a, err := scanAliasDomain(r.pool.QueryRow(ctx,
		`SELECT `+aliasDomainColumns+` FROM mail.alias_domains WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AliasDomainRepo) Create(ctx context.Context, a *domain.AliasDomain) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.alias_domains (id, tenant_id, alias_domain, target_domain, active)
 VALUES ($1, $2, $3, $4, $5) RETURNING created_at, updated_at`,
		a.ID, a.TenantID, a.AliasDomain, a.TargetDomain, a.Active,
	).Scan(&a.CreatedAt, &a.UpdatedAt))
}

func (r *AliasDomainRepo) Update(ctx context.Context, a *domain.AliasDomain) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.alias_domains SET target_domain = $3, active = $4 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		a.TenantID, a.ID, a.TargetDomain, a.Active,
	).Scan(&a.UpdatedAt))
}

func (r *AliasDomainRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.alias_domains WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *AliasDomainRepo) ExistsByName(ctx context.Context, tenantID uuid.UUID, name string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM mail.alias_domains WHERE tenant_id = $1 AND alias_domain = $2)`,
		tenantID, name).Scan(&exists)
	return exists, err
}
