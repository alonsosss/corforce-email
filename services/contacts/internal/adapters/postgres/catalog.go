package postgres

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ── Listas ───────────────────────────────────────────────────────────────────

type ListRepository struct {
	pool *db.ContextPool
}

func NewListRepository(pool *db.ContextPool) *ListRepository {
	return &ListRepository{pool: pool}
}

const listColumns = `l.id, l.tenant_id, l.name, l.description,
	(SELECT count(*) FROM contacts.list_members m WHERE m.list_id = l.id), l.created_at, l.updated_at`

func scanList(row pgx.Row) (*domain.List, error) {
	var l domain.List
	if err := row.Scan(&l.ID, &l.TenantID, &l.Name, &l.Description, &l.MemberCount, &l.CreatedAt, &l.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrListNotFound
		}
		return nil, err
	}
	return &l, nil
}

func collectLists(rows pgx.Rows) ([]domain.List, error) {
	defer rows.Close()
	out := []domain.List{}
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (r *ListRepository) Create(ctx context.Context, l *domain.List) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO contacts.lists (tenant_id, name, description) VALUES ($1, $2, $3)
		 RETURNING id, created_at, updated_at`,
		l.TenantID, l.Name, l.Description,
	).Scan(&l.ID, &l.CreatedAt, &l.UpdatedAt)
	if isUniqueViolation(err) {
		return domain.ErrListExists
	}
	return err
}

func (r *ListRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.List, error) {
	return scanList(r.pool.QueryRow(ctx,
		`SELECT `+listColumns+` FROM contacts.lists l WHERE l.tenant_id = $1 AND l.id = $2`, tenantID, id))
}

func (r *ListRepository) Update(ctx context.Context, l *domain.List) error {
	err := r.pool.QueryRow(ctx,
		`UPDATE contacts.lists SET name = $3, description = $4 WHERE tenant_id = $1 AND id = $2 RETURNING updated_at`,
		l.TenantID, l.ID, l.Name, l.Description,
	).Scan(&l.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrListNotFound
	case isUniqueViolation(err):
		return domain.ErrListExists
	}
	return err
}

func (r *ListRepository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM contacts.lists WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrListNotFound
	}
	return nil
}

func (r *ListRepository) List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.List, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM contacts.lists WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+listColumns+` FROM contacts.lists l WHERE l.tenant_id = $1 ORDER BY l.name, l.id LIMIT $2 OFFSET $3`,
		tenantID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	out, err := collectLists(rows)
	return out, total, err
}

func (r *ListRepository) ExistingIDs(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM contacts.lists WHERE tenant_id = $1 AND id = ANY($2::uuid[])`, tenantID, ids)
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

// AddMembers solo inserta contactos de la misma empresa: un id ajeno o inexistente no
// entra y no revela nada.
func (r *ListRepository) AddMembers(ctx context.Context, tenantID, listID uuid.UUID, contactIDs []uuid.UUID) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO contacts.list_members (tenant_id, list_id, contact_id)
		 SELECT $1, $2, c.id FROM contacts.contacts c WHERE c.tenant_id = $1 AND c.id = ANY($3::uuid[])
		 ON CONFLICT (list_id, contact_id) DO NOTHING`,
		tenantID, listID, contactIDs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (r *ListRepository) RemoveMembers(ctx context.Context, tenantID, listID uuid.UUID, contactIDs []uuid.UUID) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM contacts.list_members WHERE tenant_id = $1 AND list_id = $2 AND contact_id = ANY($3::uuid[])`,
		tenantID, listID, contactIDs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (r *ListRepository) MembersAmong(ctx context.Context, tenantID, listID uuid.UUID, contactIDs []uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT contact_id FROM contacts.list_members
		  WHERE tenant_id = $1 AND list_id = $2 AND contact_id = ANY($3::uuid[])
		  ORDER BY contact_id`,
		tenantID, listID, contactIDs)
	if err != nil {
		return nil, err
	}
	return collectIDs(rows)
}

func (r *ListRepository) ListsOf(ctx context.Context, tenantID, contactID uuid.UUID) ([]domain.List, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+listColumns+` FROM contacts.lists l
		   JOIN contacts.list_members lm ON lm.list_id = l.id
		  WHERE lm.tenant_id = $1 AND lm.contact_id = $2
		  ORDER BY l.name, l.id`, tenantID, contactID)
	if err != nil {
		return nil, err
	}
	return collectLists(rows)
}

func collectIDs(rows pgx.Rows) ([]uuid.UUID, error) {
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ── Atributos ────────────────────────────────────────────────────────────────

type AttributeRepository struct {
	pool *db.ContextPool
}

func NewAttributeRepository(pool *db.ContextPool) *AttributeRepository {
	return &AttributeRepository{pool: pool}
}

const attributeColumns = `id, tenant_id, key, type, label, required, created_at, updated_at`

func scanAttribute(row pgx.Row) (*domain.AttributeDefinition, error) {
	var d domain.AttributeDefinition
	if err := row.Scan(&d.ID, &d.TenantID, &d.Key, &d.Type, &d.Label, &d.Required, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrAttributeNotFound
		}
		return nil, err
	}
	return &d, nil
}

func (r *AttributeRepository) List(ctx context.Context, tenantID uuid.UUID) ([]domain.AttributeDefinition, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+attributeColumns+` FROM contacts.attribute_definitions WHERE tenant_id = $1 ORDER BY key`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AttributeDefinition{}
	for rows.Next() {
		d, err := scanAttribute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (r *AttributeRepository) Get(ctx context.Context, tenantID uuid.UUID, key string) (*domain.AttributeDefinition, error) {
	return scanAttribute(r.pool.QueryRow(ctx,
		`SELECT `+attributeColumns+` FROM contacts.attribute_definitions WHERE tenant_id = $1 AND key = $2`, tenantID, key))
}

func (r *AttributeRepository) Create(ctx context.Context, d *domain.AttributeDefinition) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO contacts.attribute_definitions (tenant_id, key, type, label, required) VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, created_at, updated_at`,
		d.TenantID, d.Key, d.Type, d.Label, d.Required,
	).Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt)
	if isUniqueViolation(err) {
		return domain.ErrAttributeExists
	}
	return err
}

func (r *AttributeRepository) Update(ctx context.Context, d *domain.AttributeDefinition) error {
	err := r.pool.QueryRow(ctx,
		`UPDATE contacts.attribute_definitions SET label = $3, required = $4 WHERE tenant_id = $1 AND key = $2
		 RETURNING updated_at`,
		d.TenantID, d.Key, d.Label, d.Required,
	).Scan(&d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrAttributeNotFound
	}
	return err
}

func (r *AttributeRepository) Delete(ctx context.Context, tenantID uuid.UUID, key string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM contacts.attribute_definitions WHERE tenant_id = $1 AND key = $2`, tenantID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAttributeNotFound
	}
	return nil
}

// ── Segmentos ────────────────────────────────────────────────────────────────

type SegmentRepository struct {
	pool *db.ContextPool
}

func NewSegmentRepository(pool *db.ContextPool) *SegmentRepository {
	return &SegmentRepository{pool: pool}
}

const segmentColumns = `id, tenant_id, name, description, definition, created_at, updated_at`

func scanSegment(row pgx.Row) (*domain.Segment, error) {
	var (
		s   domain.Segment
		def []byte
	)
	if err := row.Scan(&s.ID, &s.TenantID, &s.Name, &s.Description, &def, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrSegmentNotFound
		}
		return nil, err
	}
	s.Definition = def
	return &s, nil
}

func collectSegments(rows pgx.Rows) ([]domain.Segment, error) {
	defer rows.Close()
	out := []domain.Segment{}
	for rows.Next() {
		s, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *SegmentRepository) Create(ctx context.Context, s *domain.Segment) error {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO contacts.segments (tenant_id, name, description, definition) VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at, updated_at`,
		s.TenantID, s.Name, s.Description, []byte(s.Definition),
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if isUniqueViolation(err) {
		return domain.ErrSegmentExists
	}
	return err
}

func (r *SegmentRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Segment, error) {
	return scanSegment(r.pool.QueryRow(ctx,
		`SELECT `+segmentColumns+` FROM contacts.segments WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *SegmentRepository) Update(ctx context.Context, s *domain.Segment) error {
	err := r.pool.QueryRow(ctx,
		`UPDATE contacts.segments SET name = $3, description = $4, definition = $5 WHERE tenant_id = $1 AND id = $2
		 RETURNING updated_at`,
		s.TenantID, s.ID, s.Name, s.Description, []byte(s.Definition),
	).Scan(&s.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrSegmentNotFound
	case isUniqueViolation(err):
		return domain.ErrSegmentExists
	}
	return err
}

func (r *SegmentRepository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM contacts.segments WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrSegmentNotFound
	}
	return nil
}

func (r *SegmentRepository) List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Segment, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM contacts.segments WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+segmentColumns+` FROM contacts.segments WHERE tenant_id = $1 ORDER BY name, id LIMIT $2 OFFSET $3`,
		tenantID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	out, err := collectSegments(rows)
	return out, total, err
}

func (r *SegmentRepository) ListAll(ctx context.Context, tenantID uuid.UUID) ([]domain.Segment, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+segmentColumns+` FROM contacts.segments WHERE tenant_id = $1 ORDER BY created_at, id`, tenantID)
	if err != nil {
		return nil, err
	}
	return collectSegments(rows)
}

func (r *SegmentRepository) GetMany(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Segment, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+segmentColumns+` FROM contacts.segments WHERE tenant_id = $1 AND id = ANY($2::uuid[]) ORDER BY id`,
		tenantID, ids)
	if err != nil {
		return nil, err
	}
	return collectSegments(rows)
}

// ── Importaciones ────────────────────────────────────────────────────────────

type ImportRepository struct {
	pool *db.ContextPool
}

func NewImportRepository(pool *db.ContextPool) *ImportRepository {
	return &ImportRepository{pool: pool}
}

const importColumns = `id, tenant_id, status, total, created, updated, skipped, errors, consent_basis, list_id, created_by, created_at`

func scanImport(row pgx.Row) (*domain.Import, error) {
	var (
		imp    domain.Import
		errs   []byte
		parsed []domain.ImportError
	)
	if err := row.Scan(&imp.ID, &imp.TenantID, &imp.Status, &imp.Total, &imp.Created, &imp.Updated, &imp.Skipped,
		&errs, &imp.ConsentBasis, &imp.ListID, &imp.CreatedBy, &imp.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrImportNotFound
		}
		return nil, err
	}
	if err := unmarshalImportErrors(errs, &parsed); err != nil {
		return nil, err
	}
	imp.Errors = parsed
	return &imp, nil
}

func (r *ImportRepository) Create(ctx context.Context, imp *domain.Import) error {
	errs, err := marshalImportErrors(imp.Errors)
	if err != nil {
		return err
	}
	return r.pool.QueryRow(ctx,
		`INSERT INTO contacts.imports (id, tenant_id, status, total, created, updated, skipped, errors, consent_basis, list_id, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING created_at`,
		imp.ID, imp.TenantID, imp.Status, imp.Total, imp.Created, imp.Updated, imp.Skipped, errs,
		imp.ConsentBasis, imp.ListID, imp.CreatedBy,
	).Scan(&imp.CreatedAt)
}

func (r *ImportRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Import, error) {
	return scanImport(r.pool.QueryRow(ctx,
		`SELECT `+importColumns+` FROM contacts.imports WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *ImportRepository) List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Import, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM contacts.imports WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+importColumns+` FROM contacts.imports WHERE tenant_id = $1 ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`,
		tenantID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.Import{}
	for rows.Next() {
		imp, err := scanImport(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *imp)
	}
	return out, total, rows.Err()
}
