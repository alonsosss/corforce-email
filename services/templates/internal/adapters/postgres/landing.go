package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	pageNameConstraint = "pages_tenant_name_key"
	pageSlugConstraint = "pages_tenant_slug_key"

	pageColumns        = `id, tenant_id, name, slug, status, noindex, current_version, created_by, created_at, updated_at`
	pageVersionColumns = `id, tenant_id, page_id, version, title, description, html, css, editor, status, published_at, created_by, created_at`
)

func translatePage(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case pageNameConstraint:
			return domain.ErrPageNameTaken
		case pageSlugConstraint:
			return domain.ErrPageSlugTaken
		}
	}
	return err
}

func scanPage(row pgx.Row) (*domain.LandingPage, error) {
	var p domain.LandingPage
	err := row.Scan(&p.ID, &p.TenantID, &p.Name, &p.Slug, &p.Status, &p.NoIndex, &p.CurrentVersion,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrPageNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repository) CreatePage(ctx context.Context, p *domain.LandingPage) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO templates.pages (id, tenant_id, name, slug, status, noindex, current_version, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at, updated_at`,
		p.ID, p.TenantID, p.Name, p.Slug, p.Status, p.NoIndex, p.CurrentVersion, p.CreatedBy,
	).Scan(&p.CreatedAt, &p.UpdatedAt)
	return translatePage(err)
}

func (r *Repository) GetPage(ctx context.Context, tenantID, id uuid.UUID) (*domain.LandingPage, error) {
	return scanPage(r.pool.QueryRow(ctx,
		`SELECT `+pageColumns+` FROM templates.pages WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *Repository) GetPageForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.LandingPage, error) {
	return scanPage(r.pool.QueryRow(ctx,
		`SELECT `+pageColumns+` FROM templates.pages WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
}

func (r *Repository) GetPageBySlug(ctx context.Context, tenantID uuid.UUID, slug string) (*domain.LandingPage, error) {
	return scanPage(r.pool.QueryRow(ctx,
		`SELECT `+pageColumns+` FROM templates.pages WHERE tenant_id = $1 AND slug = $2`, tenantID, slug))
}

func (r *Repository) ListPages(ctx context.Context, tenantID uuid.UUID, f ports.PageFilter) ([]*domain.LandingPage, int64, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, "status = $"+strconv.Itoa(len(args)))
	}
	if f.Search != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Search)+"%")
		n := strconv.Itoa(len(args))
		where = append(where, "(name ILIKE $"+n+" OR slug ILIKE $"+n+")")
	}
	cond := strings.Join(where, " AND ")
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM templates.pages WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx,
		`SELECT `+pageColumns+` FROM templates.pages WHERE `+cond+
			` ORDER BY updated_at DESC, id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]*domain.LandingPage, 0)
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (r *Repository) UpdatePage(ctx context.Context, p *domain.LandingPage) error {
	err := r.pool.QueryRow(ctx,
		`UPDATE templates.pages SET name = $3, slug = $4, status = $5, noindex = $6, current_version = $7
		  WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		p.TenantID, p.ID, p.Name, p.Slug, p.Status, p.NoIndex, p.CurrentVersion,
	).Scan(&p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrPageNotFound
	}
	return translatePage(err)
}

func (r *Repository) DeletePage(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM templates.pages WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPageNotFound
	}
	return nil
}

func (r *Repository) CreatePageVersion(ctx context.Context, v *domain.LandingVersion) error {
	var editor []byte
	if v.Content.Editor != nil {
		var err error
		if editor, err = json.Marshal(v.Content.Editor); err != nil {
			return err
		}
	}
	return r.pool.QueryRow(ctx,
		`INSERT INTO templates.page_versions (id, tenant_id, page_id, version, title, description, html, css, editor, status, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING created_at`,
		v.ID, v.TenantID, v.PageID, v.Version, v.Content.Title, v.Content.Description, v.Content.HTML,
		v.Content.CSS, editor, v.Status, v.CreatedBy,
	).Scan(&v.CreatedAt)
}

func (r *Repository) GetPageVersion(ctx context.Context, tenantID, pageID uuid.UUID, version int) (*domain.LandingVersion, error) {
	var v domain.LandingVersion
	var editor []byte
	err := r.pool.QueryRow(ctx,
		`SELECT `+pageVersionColumns+` FROM templates.page_versions WHERE tenant_id = $1 AND page_id = $2 AND version = $3`,
		tenantID, pageID, version,
	).Scan(&v.ID, &v.TenantID, &v.PageID, &v.Version, &v.Content.Title, &v.Content.Description, &v.Content.HTML,
		&v.Content.CSS, &editor, &v.Status, &v.PublishedAt, &v.CreatedBy, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrPageVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(editor) > 0 {
		var doc domain.PageEditorDocument
		if err := json.Unmarshal(editor, &doc); err != nil {
			return nil, err
		}
		v.Content.Editor = &doc
	}
	return &v, nil
}

func (r *Repository) ListPageVersions(ctx context.Context, tenantID, pageID uuid.UUID) ([]domain.VersionSummary, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, version, status, published_at, created_by, created_at
		   FROM templates.page_versions WHERE tenant_id = $1 AND page_id = $2 ORDER BY version DESC`,
		tenantID, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.VersionSummary, 0)
	for rows.Next() {
		var s domain.VersionSummary
		if err := rows.Scan(&s.ID, &s.Version, &s.Status, &s.PublishedAt, &s.CreatedBy, &s.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

func (r *Repository) MaxPageVersion(ctx context.Context, tenantID, pageID uuid.UUID) (int, error) {
	var max int
	err := r.pool.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM templates.page_versions WHERE tenant_id = $1 AND page_id = $2`,
		tenantID, pageID).Scan(&max)
	return max, err
}

func (r *Repository) SupersedePublishedPage(ctx context.Context, tenantID, pageID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE templates.page_versions SET status = 'superseded'
		  WHERE tenant_id = $1 AND page_id = $2 AND status = 'published'`, tenantID, pageID)
	return err
}

func (r *Repository) MarkPageVersionPublished(ctx context.Context, tenantID, versionID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE templates.page_versions SET status = 'published', published_at = coalesce(published_at, now())
		  WHERE tenant_id = $1 AND id = $2`, tenantID, versionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPageVersionNotFound
	}
	return nil
}
