package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// contactColumns lee siempre la tabla con el alias c, tambien en las consultas de
// segmento, cuyo WHERE compilado se refiere a c.
const contactColumns = `c.id, c.tenant_id, c.email, c.first_name, c.last_name, c.locale, c.timezone,
	c.attributes, c.tags, c.status, c.marketing_consent, c.source, c.created_at, c.updated_at`

type ContactRepository struct {
	pool *db.ContextPool
}

func NewContactRepository(pool *db.ContextPool) *ContactRepository {
	return &ContactRepository{pool: pool}
}

func scanContact(row pgx.Row) (*domain.Contact, error) {
	var (
		c     domain.Contact
		attrs []byte
	)
	if err := row.Scan(&c.ID, &c.TenantID, &c.Email, &c.FirstName, &c.LastName, &c.Locale, &c.Timezone,
		&attrs, &c.Tags, &c.Status, &c.ConsentStatus, &c.Source, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrContactNotFound
		}
		return nil, err
	}
	var err error
	if c.Attributes, err = readObject(attrs); err != nil {
		return nil, err
	}
	c.Tags = nonNilStrings(c.Tags)
	return &c, nil
}

func collectContacts(rows pgx.Rows) ([]domain.Contact, error) {
	defer rows.Close()
	out := []domain.Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *ContactRepository) Insert(ctx context.Context, c *domain.Contact) error {
	attrs, err := jsonObject(c.Attributes)
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx,
		`INSERT INTO contacts.contacts (tenant_id, email, first_name, last_name, locale, timezone, attributes, tags, status, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, marketing_consent, created_at, updated_at`,
		c.TenantID, c.Email, c.FirstName, c.LastName, c.Locale, c.Timezone, attrs, nonNilStrings(c.Tags), c.Status, c.Source,
	).Scan(&c.ID, &c.ConsentStatus, &c.CreatedAt, &c.UpdatedAt)
	if isUniqueViolation(err) {
		return domain.ErrContactExists
	}
	if c.Attributes == nil {
		c.Attributes = map[string]any{}
	}
	c.Tags = nonNilStrings(c.Tags)
	return err
}

func (r *ContactRepository) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.Contact, error) {
	return scanContact(r.pool.QueryRow(ctx,
		`SELECT `+contactColumns+` FROM contacts.contacts c WHERE c.tenant_id = $1 AND c.id = $2`, tenantID, id))
}

func (r *ContactRepository) GetForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Contact, error) {
	return scanContact(r.pool.QueryRow(ctx,
		`SELECT `+contactColumns+` FROM contacts.contacts c WHERE c.tenant_id = $1 AND c.id = $2 FOR UPDATE`, tenantID, id))
}

func (r *ContactRepository) GetByEmailForUpdate(ctx context.Context, tenantID uuid.UUID, email string) (*domain.Contact, error) {
	return scanContact(r.pool.QueryRow(ctx,
		`SELECT `+contactColumns+` FROM contacts.contacts c WHERE c.tenant_id = $1 AND c.email = $2 FOR UPDATE`, tenantID, email))
}

func (r *ContactRepository) Update(ctx context.Context, c *domain.Contact) error {
	attrs, err := jsonObject(c.Attributes)
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx,
		`UPDATE contacts.contacts
		    SET first_name = $3, last_name = $4, locale = $5, timezone = $6, attributes = $7, tags = $8, status = $9
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING updated_at`,
		c.TenantID, c.ID, c.FirstName, c.LastName, c.Locale, c.Timezone, attrs, nonNilStrings(c.Tags), c.Status,
	).Scan(&c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrContactNotFound
	}
	return err
}

func (r *ContactRepository) List(ctx context.Context, tenantID uuid.UUID, f ports.ContactFilter) ([]domain.Contact, int64, error) {
	where := `WHERE c.tenant_id = $1
	    AND ($2::text = '' OR c.status = $2::text)
	    AND ($3::text = '' OR c.tags @> ARRAY[$3::text])
	    AND ($4::uuid IS NULL OR EXISTS (SELECT 1 FROM contacts.list_members lm WHERE lm.list_id = $4::uuid AND lm.contact_id = c.id))
	    AND ($5::text = '' OR c.email LIKE $6 ESCAPE '\' OR lower(c.first_name) LIKE $6 ESCAPE '\' OR lower(c.last_name) LIKE $6 ESCAPE '\')`
	args := []any{tenantID, string(f.Status), f.Tag, f.ListID, f.Search, "%" + escapeLike(f.Search) + "%"}

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM contacts.contacts c `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+contactColumns+` FROM contacts.contacts c `+where+` ORDER BY c.created_at DESC, c.id LIMIT $7 OFFSET $8`,
		append(args, f.PerPage, (f.Page-1)*f.PerPage)...)
	if err != nil {
		return nil, 0, err
	}
	out, err := collectContacts(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ListAfter pagina por keyset sobre id. Con estado es una consulta aparte y no un filtro
// opcional: el barrido de caducidad corre a menudo sobre un conjunto pequeno y tiene que
// poder usar idx_contacts_contacts_tenant_status, que un plan generico con el estado como
// filtro opcional (vacio o igual) no usaria.
func (r *ContactRepository) ListAfter(ctx context.Context, tenantID uuid.UUID, status domain.Status, after uuid.UUID, limit int) ([]domain.Contact, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if status == "" {
		rows, err = r.pool.Query(ctx,
			`SELECT `+contactColumns+` FROM contacts.contacts c
			  WHERE c.tenant_id = $1 AND c.id > $2
			  ORDER BY c.id LIMIT $3`, tenantID, after, limit)
	} else {
		rows, err = r.pool.Query(ctx,
			`SELECT `+contactColumns+` FROM contacts.contacts c
			  WHERE c.tenant_id = $1 AND c.status = $2 AND c.id > $3
			  ORDER BY c.id LIMIT $4`, tenantID, string(status), after, limit)
	}
	if err != nil {
		return nil, err
	}
	return collectContacts(rows)
}

func (r *ContactRepository) FindByEmailsForUpdate(ctx context.Context, tenantID uuid.UUID, emails []string) ([]domain.Contact, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+contactColumns+` FROM contacts.contacts c
		  WHERE c.tenant_id = $1 AND c.email = ANY($2::text[])
		  ORDER BY c.id FOR UPDATE`, tenantID, emails)
	if err != nil {
		return nil, err
	}
	return collectContacts(rows)
}

// batchContact es la forma de una fila en el JSON que recorre jsonb_to_recordset: un
// lote entero viaja en un solo argumento y se escribe en una sola sentencia.
type batchContact struct {
	ID         uuid.UUID      `json:"id"`
	Email      string         `json:"email"`
	FirstName  string         `json:"first_name"`
	LastName   string         `json:"last_name"`
	Locale     *string        `json:"locale"`
	Timezone   *string        `json:"timezone"`
	Attributes map[string]any `json:"attributes"`
	Tags       []string       `json:"tags"`
	Status     domain.Status  `json:"status"`
	Source     domain.Source  `json:"source"`
}

func toBatch(contacts []domain.Contact) ([]byte, error) {
	rows := make([]batchContact, len(contacts))
	for i, c := range contacts {
		attrs := c.Attributes
		if attrs == nil {
			attrs = map[string]any{}
		}
		rows[i] = batchContact{
			ID: c.ID, Email: c.Email, FirstName: c.FirstName, LastName: c.LastName,
			Locale: c.Locale, Timezone: c.Timezone, Attributes: attrs, Tags: nonNilStrings(c.Tags),
			Status: c.Status, Source: c.Source,
		}
	}
	return json.Marshal(rows)
}

func (r *ContactRepository) InsertMany(ctx context.Context, contacts []domain.Contact) ([]domain.Contact, error) {
	if len(contacts) == 0 {
		return []domain.Contact{}, nil
	}
	batch, err := toBatch(contacts)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx,
		`INSERT INTO contacts.contacts AS c (tenant_id, email, first_name, last_name, locale, timezone, attributes, tags, status, source)
		 SELECT $1, b.email, b.first_name, b.last_name, b.locale, b.timezone, b.attributes,
		        ARRAY(SELECT jsonb_array_elements_text(b.tags)), b.status, b.source
		   FROM jsonb_to_recordset($2::jsonb) AS b(email text, first_name text, last_name text, locale text,
		        timezone text, attributes jsonb, tags jsonb, status text, source text)
		 ON CONFLICT (tenant_id, email) DO NOTHING
		 RETURNING `+contactColumns,
		contacts[0].TenantID, batch)
	if err != nil {
		return nil, err
	}
	return collectContacts(rows)
}

func (r *ContactRepository) UpdateMany(ctx context.Context, contacts []domain.Contact) error {
	if len(contacts) == 0 {
		return nil
	}
	batch, err := toBatch(contacts)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx,
		`UPDATE contacts.contacts c
		    SET first_name = b.first_name, last_name = b.last_name, locale = b.locale, timezone = b.timezone,
		        attributes = b.attributes, tags = ARRAY(SELECT jsonb_array_elements_text(b.tags))
		   FROM jsonb_to_recordset($2::jsonb) AS b(id uuid, first_name text, last_name text, locale text,
		        timezone text, attributes jsonb, tags jsonb)
		  WHERE c.tenant_id = $1 AND c.id = b.id`,
		contacts[0].TenantID, batch)
	return err
}

// Erase es la unica escritura sobre contacts.consents fuera de un INSERT. El trigger de
// la tabla solo la permite con app.erasure = 'on' en la transaccion, y solo si se
// limita a seudonimizar; se apaga en cuanto termina para que nada mas en la transaccion
// herede el permiso.
func (r *ContactRepository) Erase(ctx context.Context, tenantID, id, pseudonym uuid.UUID, emailSHA256 string) error {
	if !db.HasTx(ctx) {
		return fmt.Errorf("el borrado del titular debe correr dentro de una transaccion")
	}
	if _, err := r.pool.Exec(ctx, `SET LOCAL app.erasure = 'on'`); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx,
		`UPDATE contacts.consents
		    SET contact_id = $3, ip = NULL, user_agent = NULL,
		        evidence = evidence || jsonb_build_object('email_sha256', $4::text, 'erased_at', now())
		  WHERE tenant_id = $1 AND contact_id = $2`,
		tenantID, id, pseudonym, emailSHA256); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `SET LOCAL app.erasure = 'off'`); err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM contacts.contacts WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrContactNotFound
	}
	return nil
}

func (r *ContactRepository) StripAttribute(ctx context.Context, tenantID uuid.UUID, key string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE contacts.contacts SET attributes = attributes - $2::text
		  WHERE tenant_id = $1 AND attributes ? $2::text`, tenantID, key)
	return err
}
