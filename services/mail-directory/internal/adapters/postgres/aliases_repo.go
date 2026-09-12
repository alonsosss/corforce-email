package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AliasRepo struct {
	pool *db.ContextPool
}

func NewAliasRepo(pool *db.ContextPool) *AliasRepo { return &AliasRepo{pool: pool} }

const aliasColumns = `id, tenant_id, address, goto, domain, sender_allowed, internal, active, private_comment,
 public_comment, created_at, updated_at`

func scanAlias(row pgx.Row) (domain.Alias, error) {
	var a domain.Alias
	err := row.Scan(&a.ID, &a.TenantID, &a.Address, &a.Goto, &a.Domain, &a.SenderAllowed, &a.Internal, &a.Active,
		&a.PrivateComment, &a.PublicComment, &a.CreatedAt, &a.UpdatedAt)
	return a, mapErr(err)
}

func (r *AliasRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.Alias, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.aliases WHERE tenant_id = $1`,
		`SELECT `+aliasColumns+` FROM mail.aliases WHERE tenant_id = $1 ORDER BY address LIMIT $2 OFFSET $3`,
		scanAlias, page.Limit, page.Offset, tenantID)
}

func (r *AliasRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Alias, error) {
	a, err := scanAlias(r.pool.QueryRow(ctx,
		`SELECT `+aliasColumns+` FROM mail.aliases WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AliasRepo) Create(ctx context.Context, a *domain.Alias) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.aliases (id, tenant_id, address, goto, domain, sender_allowed, internal, active, private_comment, public_comment)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING created_at, updated_at`,
		a.ID, a.TenantID, a.Address, a.Goto, a.Domain, a.SenderAllowed, a.Internal, a.Active, a.PrivateComment, a.PublicComment,
	).Scan(&a.CreatedAt, &a.UpdatedAt))
}

func (r *AliasRepo) Update(ctx context.Context, a *domain.Alias) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.aliases SET goto = $3, sender_allowed = $4, internal = $5, active = $6, private_comment = $7,
 public_comment = $8 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		a.TenantID, a.ID, a.Goto, a.SenderAllowed, a.Internal, a.Active, a.PrivateComment, a.PublicComment,
	).Scan(&a.UpdatedAt))
}

func (r *AliasRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.aliases WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *AliasRepo) CountByDomain(ctx context.Context, tenantID uuid.UUID, name string) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM mail.aliases WHERE tenant_id = $1 AND domain = $2`, tenantID, name).Scan(&n)
	return n, err
}

// ── Aliases temporales ────────────────────────────────────────────────────────

type SpamAliasRepo struct {
	pool *db.ContextPool
}

func NewSpamAliasRepo(pool *db.ContextPool) *SpamAliasRepo { return &SpamAliasRepo{pool: pool} }

const spamAliasColumns = `id, tenant_id, address, goto, description, valid_until, permanent, created_at, updated_at`

func scanSpamAlias(row pgx.Row) (domain.SpamAlias, error) {
	var a domain.SpamAlias
	err := row.Scan(&a.ID, &a.TenantID, &a.Address, &a.Goto, &a.Description, &a.ValidUntil, &a.Permanent, &a.CreatedAt, &a.UpdatedAt)
	return a, mapErr(err)
}

func (r *SpamAliasRepo) List(ctx context.Context, tenantID uuid.UUID, page ports.Page) ([]domain.SpamAlias, int64, error) {
	return listPage(ctx, r.pool,
		`SELECT COUNT(*) FROM mail.spam_aliases WHERE tenant_id = $1`,
		`SELECT `+spamAliasColumns+` FROM mail.spam_aliases WHERE tenant_id = $1 ORDER BY address LIMIT $2 OFFSET $3`,
		scanSpamAlias, page.Limit, page.Offset, tenantID)
}

func (r *SpamAliasRepo) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.SpamAlias, error) {
	a, err := scanSpamAlias(r.pool.QueryRow(ctx,
		`SELECT `+spamAliasColumns+` FROM mail.spam_aliases WHERE tenant_id = $1 AND id = $2`, tenantID, id))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *SpamAliasRepo) Create(ctx context.Context, a *domain.SpamAlias) error {
	return mapErr(r.pool.QueryRow(ctx,
		`INSERT INTO mail.spam_aliases (id, tenant_id, address, goto, description, valid_until, permanent)
 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`,
		a.ID, a.TenantID, a.Address, a.Goto, a.Description, a.ValidUntil, a.Permanent,
	).Scan(&a.CreatedAt, &a.UpdatedAt))
}

func (r *SpamAliasRepo) Update(ctx context.Context, a *domain.SpamAlias) error {
	return mapErr(r.pool.QueryRow(ctx,
		`UPDATE mail.spam_aliases SET description = $3, valid_until = $4, permanent = $5
 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		a.TenantID, a.ID, a.Description, a.ValidUntil, a.Permanent,
	).Scan(&a.UpdatedAt))
}

func (r *SpamAliasRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx, `DELETE FROM mail.spam_aliases WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *SpamAliasRepo) DeleteByGoto(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.spam_aliases WHERE tenant_id = $1 AND goto = $2`, tenantID, username)
	return err
}
