package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository persiste en el esquema templates de la base de la empresa. El pool o la
// transaccion se resuelven desde el contexto (pkg/db.ContextPool).
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository {
	return &Repository{pool: pool}
}

const (
	uniqueViolation        = "23505"
	templateNameConstraint = "templates_tenant_name_key"

	templateColumns = `id, tenant_id, name, description, kind, status, current_version, created_by, created_at, updated_at`
	versionColumns  = `id, tenant_id, template_id, version, subject, html, text, variables, status, published_at, created_by, created_at`
)

func (r *Repository) CreateTemplate(ctx context.Context, t *domain.Template) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO templates.templates (id, tenant_id, name, description, kind, status, current_version, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING created_at, updated_at`,
		t.ID, t.TenantID, t.Name, t.Description, t.Kind, t.Status, t.CurrentVersion, t.CreatedBy,
	).Scan(&t.CreatedAt, &t.UpdatedAt)
	return translate(err)
}

func (r *Repository) GetTemplate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Template, error) {
	return r.getTemplate(ctx, tenantID, id, "")
}

func (r *Repository) GetTemplateForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Template, error) {
	return r.getTemplate(ctx, tenantID, id, " FOR UPDATE")
}

func (r *Repository) getTemplate(ctx context.Context, tenantID, id uuid.UUID, lock string) (*domain.Template, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+templateColumns+` FROM templates.templates WHERE tenant_id = $1 AND id = $2`+lock,
		tenantID, id)
	t, err := scanTemplate(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrTemplateNotFound
		}
		return nil, err
	}
	return t, nil
}

func (r *Repository) ListTemplates(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter) ([]*domain.Template, int64, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	if f.Kind != "" {
		args = append(args, f.Kind)
		where = append(where, "kind = $"+strconv.Itoa(len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, "status = $"+strconv.Itoa(len(args)))
	}
	if f.Search != "" {
		args = append(args, "%"+escapeLike(f.Search)+"%")
		n := strconv.Itoa(len(args))
		where = append(where, `(name ILIKE $`+n+` ESCAPE '\' OR description ILIKE $`+n+` ESCAPE '\')`)
	}
	filter := strings.Join(where, " AND ")

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM templates.templates WHERE `+filter, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx,
		`SELECT `+templateColumns+` FROM templates.templates WHERE `+filter+
			` ORDER BY name ASC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)),
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]*domain.Template, 0)
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// escapeLike neutraliza los comodines de ILIKE en el texto buscado.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (r *Repository) UpdateTemplate(ctx context.Context, t *domain.Template) error {
	err := r.pool.QueryRow(ctx,
		`UPDATE templates.templates
		    SET name = $3, description = $4, status = $5, current_version = $6
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING updated_at`,
		t.TenantID, t.ID, t.Name, t.Description, t.Status, t.CurrentVersion,
	).Scan(&t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrTemplateNotFound
	}
	return translate(err)
}

func (r *Repository) DeleteTemplate(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM templates.templates WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTemplateNotFound
	}
	return nil
}

func (r *Repository) CreateVersion(ctx context.Context, v *domain.Version) error {
	variables, err := json.Marshal(v.Variables)
	if err != nil {
		return fmt.Errorf("serializar variables: %w", err)
	}
	err = r.pool.QueryRow(ctx,
		`INSERT INTO templates.versions (id, tenant_id, template_id, version, subject, html, text, variables, status, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING created_at`,
		v.ID, v.TenantID, v.TemplateID, v.Version, v.Subject, v.HTML, v.Text, variables, v.Status, v.CreatedBy,
	).Scan(&v.CreatedAt)
	return translate(err)
}

func (r *Repository) GetVersion(ctx context.Context, tenantID, templateID uuid.UUID, version int) (*domain.Version, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+versionColumns+` FROM templates.versions WHERE tenant_id = $1 AND template_id = $2 AND version = $3`,
		tenantID, templateID, version)
	return scanVersionRow(row)
}

func (r *Repository) GetPublishedVersion(ctx context.Context, tenantID, templateID uuid.UUID) (*domain.Version, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+versionColumns+` FROM templates.versions WHERE tenant_id = $1 AND template_id = $2 AND status = $3`,
		tenantID, templateID, domain.VersionStatusPublished)
	return scanVersionRow(row)
}

func scanVersionRow(row pgx.Row) (*domain.Version, error) {
	v, err := scanVersion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrVersionNotFound
		}
		return nil, err
	}
	return v, nil
}

func (r *Repository) ListVersions(ctx context.Context, tenantID, templateID uuid.UUID) ([]domain.VersionSummary, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, version, status, published_at, created_by, created_at
		   FROM templates.versions WHERE tenant_id = $1 AND template_id = $2
		  ORDER BY version DESC`,
		tenantID, templateID)
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

func (r *Repository) MaxVersion(ctx context.Context, tenantID, templateID uuid.UUID) (int, error) {
	var max int
	err := r.pool.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM templates.versions WHERE tenant_id = $1 AND template_id = $2`,
		tenantID, templateID).Scan(&max)
	return max, err
}

func (r *Repository) SupersedePublished(ctx context.Context, tenantID, templateID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE templates.versions SET status = $3
		  WHERE tenant_id = $1 AND template_id = $2 AND status = $4`,
		tenantID, templateID, domain.VersionStatusSuperseded, domain.VersionStatusPublished)
	return err
}

func (r *Repository) MarkPublished(ctx context.Context, tenantID, versionID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE templates.versions SET status = $3, published_at = now()
		  WHERE tenant_id = $1 AND id = $2`,
		tenantID, versionID, domain.VersionStatusPublished)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrVersionNotFound
	}
	return nil
}

func scanTemplate(row pgx.Row) (*domain.Template, error) {
	var t domain.Template
	err := row.Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.Kind, &t.Status, &t.CurrentVersion,
		&t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func scanVersion(row pgx.Row) (*domain.Version, error) {
	var (
		v         domain.Version
		variables []byte
	)
	err := row.Scan(&v.ID, &v.TenantID, &v.TemplateID, &v.Version, &v.Subject, &v.HTML, &v.Text, &variables,
		&v.Status, &v.PublishedAt, &v.CreatedBy, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(variables, &v.Variables); err != nil {
		return nil, fmt.Errorf("leer variables de la version %s: %w", v.ID, err)
	}
	if v.Variables == nil {
		v.Variables = []domain.Variable{}
	}
	return &v, nil
}

// translate convierte la violacion del nombre unico en el error de dominio; el resto de
// fallos se devuelven tal cual para que el handler los registre.
func translate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == templateNameConstraint {
		return domain.ErrTemplateNameTaken
	}
	return err
}
