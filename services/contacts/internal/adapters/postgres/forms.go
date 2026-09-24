package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// foreignKeyViolation es el SQLSTATE de una clave foranea incumplida.
const foreignKeyViolation = "23503"

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation
}

type FormRepository struct {
	pool *db.ContextPool
}

func NewFormRepository(pool *db.ContextPool) *FormRepository {
	return &FormRepository{pool: pool}
}

const formColumns = `id, tenant_id, name, status, list_id, fields, title, description, submit_label,
	consent_text, success_message, redirect_url, allowed_origins, created_by, created_at, updated_at`

func scanForm(row pgx.Row) (*domain.SubscriptionForm, error) {
	var f domain.SubscriptionForm
	var fields []byte
	err := row.Scan(&f.ID, &f.TenantID, &f.Name, &f.Status, &f.ListID, &fields, &f.Texts.Title,
		&f.Texts.Description, &f.Texts.SubmitLabel, &f.Texts.ConsentText, &f.Texts.SuccessMessage,
		&f.RedirectURL, &f.AllowedOrigins, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrFormNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fields, &f.Fields); err != nil {
		return nil, fmt.Errorf("leer los campos del formulario %s: %w", f.ID, err)
	}
	f.AllowedOrigins = nonNilStrings(f.AllowedOrigins)
	return &f, nil
}

func formWriteError(err error) error {
	switch {
	case isUniqueViolation(err):
		return domain.ErrFormExists
	case isForeignKeyViolation(err):
		return fmt.Errorf("%w: list_id no es una lista de la empresa", domain.ErrInvalidForm)
	}
	return err
}

func (r *FormRepository) Create(ctx context.Context, f *domain.SubscriptionForm) error {
	fields, err := json.Marshal(f.Fields)
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx,
		`INSERT INTO contacts.subscription_forms (tenant_id, name, status, list_id, fields, title, description,
		        submit_label, consent_text, success_message, redirect_url, allowed_origins, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 RETURNING id, created_at, updated_at`,
		f.TenantID, f.Name, f.Status, f.ListID, fields, f.Texts.Title, f.Texts.Description, f.Texts.SubmitLabel,
		f.Texts.ConsentText, f.Texts.SuccessMessage, f.RedirectURL, nonNilStrings(f.AllowedOrigins), f.CreatedBy,
	).Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt)
	return formWriteError(err)
}

func (r *FormRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.SubscriptionForm, error) {
	return scanForm(r.pool.QueryRow(ctx,
		`SELECT `+formColumns+` FROM contacts.subscription_forms WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *FormRepository) Update(ctx context.Context, f *domain.SubscriptionForm) error {
	fields, err := json.Marshal(f.Fields)
	if err != nil {
		return err
	}
	err = r.pool.QueryRow(ctx,
		`UPDATE contacts.subscription_forms
		    SET name = $3, status = $4, list_id = $5, fields = $6, title = $7, description = $8,
		        submit_label = $9, consent_text = $10, success_message = $11, redirect_url = $12,
		        allowed_origins = $13
		  WHERE tenant_id = $1 AND id = $2
		  RETURNING updated_at`,
		f.TenantID, f.ID, f.Name, f.Status, f.ListID, fields, f.Texts.Title, f.Texts.Description,
		f.Texts.SubmitLabel, f.Texts.ConsentText, f.Texts.SuccessMessage, f.RedirectURL, nonNilStrings(f.AllowedOrigins),
	).Scan(&f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrFormNotFound
	}
	return formWriteError(err)
}

func (r *FormRepository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM contacts.subscription_forms WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrFormNotFound
	}
	return nil
}

func (r *FormRepository) List(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.SubscriptionForm, int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM contacts.subscription_forms WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+formColumns+` FROM contacts.subscription_forms WHERE tenant_id = $1
		  ORDER BY name, id LIMIT $2 OFFSET $3`, tenantID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.SubscriptionForm{}
	for rows.Next() {
		f, err := scanForm(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *f)
	}
	return out, total, rows.Err()
}

func (r *FormRepository) UsingList(ctx context.Context, tenantID, listID uuid.UUID) (bool, error) {
	var used bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM contacts.subscription_forms WHERE tenant_id = $1 AND list_id = $2)`,
		tenantID, listID).Scan(&used)
	return used, err
}

func (r *FormRepository) InsertSubmission(ctx context.Context, s *domain.FormSubmissionRecord) error {
	return r.pool.QueryRow(ctx,
		`INSERT INTO contacts.form_submissions (tenant_id, form_id, contact_id, token_id, list_id, outcome)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		s.TenantID, s.FormID, s.ContactID, s.TokenID, s.ListID, s.Outcome,
	).Scan(&s.ID)
}

// ConfirmSubmission solo cuenta la primera confirmacion; la lista se devuelve si sigue
// existiendo (la columna se anula al borrarla).
func (r *FormRepository) ConfirmSubmission(ctx context.Context, tenantID, tokenID uuid.UUID, at time.Time) (*uuid.UUID, error) {
	var listID *uuid.UUID
	err := r.pool.QueryRow(ctx,
		`UPDATE contacts.form_submissions SET confirmed_at = $3
		  WHERE tenant_id = $1 AND token_id = $2 AND confirmed_at IS NULL
		  RETURNING list_id`, tenantID, tokenID, at).Scan(&listID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return listID, err
}

// Stats cuenta en una sola pasada; la serie tiene un punto por dia del periodo, tambien los
// dias sin envios.
func (r *FormRepository) Stats(ctx context.Context, tenantID, formID uuid.UUID, from, to time.Time) (*domain.FormStats, error) {
	st := &domain.FormStats{From: from, To: to, Daily: []domain.FormStatsDay{}}
	err := r.pool.QueryRow(ctx,
		`SELECT count(*),
		        count(*) FILTER (WHERE outcome = 'confirmation_sent'),
		        count(*) FILTER (WHERE outcome = 'already_subscribed'),
		        count(*) FILTER (WHERE outcome = 'not_reachable'),
		        count(*) FILTER (WHERE confirmed_at IS NOT NULL)
		   FROM contacts.form_submissions
		  WHERE tenant_id = $1 AND form_id = $2 AND created_at >= $3 AND created_at < $4`,
		tenantID, formID, from, to,
	).Scan(&st.Submitted, &st.ConfirmationSent, &st.AlreadySubscribed, &st.NotReachable, &st.Confirmed)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT to_char(d.day, 'YYYY-MM-DD'),
		        count(s.id),
		        count(s.id) FILTER (WHERE s.confirmed_at IS NOT NULL)
		   FROM generate_series($3::timestamptz AT TIME ZONE 'UTC', ($4::timestamptz AT TIME ZONE 'UTC') - interval '1 day', interval '1 day') AS d(day)
		   LEFT JOIN contacts.form_submissions s
		     ON s.tenant_id = $1 AND s.form_id = $2
		    AND s.created_at >= d.day AT TIME ZONE 'UTC' AND s.created_at < (d.day + interval '1 day') AT TIME ZONE 'UTC'
		  GROUP BY d.day
		  ORDER BY d.day`, tenantID, formID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day domain.FormStatsDay
		if err := rows.Scan(&day.Date, &day.Submitted, &day.Confirmed); err != nil {
			return nil, err
		}
		st.Daily = append(st.Daily, day)
	}
	return st, rows.Err()
}
