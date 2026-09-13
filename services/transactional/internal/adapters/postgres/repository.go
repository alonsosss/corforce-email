package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository persiste en el esquema transactional de la base de la empresa. El pool o la
// transaccion viajan en el contexto (db.ContextPool).
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	if db.HasTx(ctx) {
		return fn(ctx)
	}
	return r.pool.Transact(ctx, fn)
}

const messageColumns = `id, tenant_id, submission_id, idempotency_key, from_email, from_name, reply_to,
	"to", cc, bcc, subject, template_id, template_version, variables, html, text, headers, tags,
	unsubscribable, class, campaign_id, contact_id, status, ses_message_id, error, attempts, scheduled_at,
	sent_at, created_by, created_at, updated_at, is_test`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row rowScanner) (*domain.Message, error) {
	var m domain.Message
	err := row.Scan(&m.ID, &m.TenantID, &m.SubmissionID, &m.IdempotencyKey, &m.FromEmail, &m.FromName, &m.ReplyTo,
		&m.To, &m.Cc, &m.Bcc, &m.Subject, &m.TemplateID, &m.TemplateVersion, &m.Variables, &m.HTML, &m.Text,
		&m.Headers, &m.Tags, &m.Unsubscribable, &m.Class, &m.CampaignID, &m.ContactID, &m.Status, &m.SESMessageID,
		&m.Error, &m.Attempts, &m.ScheduledAt, &m.SentAt, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt, &m.Test)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if m.To == nil {
		m.To = []domain.Recipient{}
	}
	if m.Cc == nil {
		m.Cc = []domain.Recipient{}
	}
	if m.Bcc == nil {
		m.Bcc = []domain.Recipient{}
	}
	return &m, nil
}

func (r *Repository) InsertMessage(ctx context.Context, m *domain.Message) error {
	// Una lista nil se serializaria como el escalar JSON null y el filtro por
	// destinatario (jsonb_array_elements) fallaria sobre esa fila.
	for _, list := range []*[]domain.Recipient{&m.To, &m.Cc, &m.Bcc} {
		if *list == nil {
			*list = []domain.Recipient{}
		}
	}
	if m.Variables == nil {
		m.Variables = map[string]any{}
	}
	if m.Headers == nil {
		m.Headers = map[string]string{}
	}
	if m.Tags == nil {
		m.Tags = map[string]string{}
	}
	m.Class = domain.ClassOrDefault(m.Class)
	_, err := r.pool.Exec(ctx, `INSERT INTO transactional.messages (
		id, tenant_id, submission_id, idempotency_key, from_email, from_name, reply_to,
		"to", cc, bcc, subject, template_id, template_version, variables, html, text, headers, tags,
		unsubscribable, class, campaign_id, contact_id, status, attempts, scheduled_at, created_by, created_at, updated_at, is_test
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29)`,
		m.ID, m.TenantID, m.SubmissionID, m.IdempotencyKey, m.FromEmail, m.FromName, m.ReplyTo,
		m.To, m.Cc, m.Bcc, m.Subject, m.TemplateID, m.TemplateVersion, m.Variables, m.HTML, m.Text, m.Headers, m.Tags,
		m.Unsubscribable, m.Class, m.CampaignID, m.ContactID, m.Status, m.Attempts, m.ScheduledAt, m.CreatedBy, m.CreatedAt, m.UpdatedAt, m.Test)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

func (r *Repository) GetMessage(ctx context.Context, tenantID, id uuid.UUID) (*domain.Message, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM transactional.messages WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanMessage(row)
}

func (r *Repository) GetAttribution(ctx context.Context, tenantID, id uuid.UUID) (*domain.MessageAttribution, error) {
	var a domain.MessageAttribution
	err := r.pool.QueryRow(ctx, `SELECT class, campaign_id, contact_id, is_test FROM transactional.messages
		WHERE tenant_id = $1 AND id = $2`, tenantID, id).Scan(&a.Class, &a.CampaignID, &a.ContactID, &a.Test)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *Repository) LockQueuedMessage(ctx context.Context, tenantID, id uuid.UUID) (*domain.Message, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM transactional.messages WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id)
	return scanMessage(row)
}

func (r *Repository) GetMessages(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Message, error) {
	if len(ids) == 0 {
		return []domain.Message{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+messageColumns+` FROM transactional.messages
		WHERE tenant_id = $1 AND id = ANY($2) ORDER BY created_at, id`, tenantID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectMessages(rows)
}

func collectMessages(rows pgx.Rows) ([]domain.Message, error) {
	out := []domain.Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (r *Repository) ListMessages(ctx context.Context, tenantID uuid.UUID, f domain.MessageFilter, offset, limit int) ([]domain.Message, int64, error) {
	where := []string{"tenant_id = $1"}
	args := []any{tenantID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Class != "" {
		add("class = $%d", f.Class)
	}
	if f.From != "" {
		add("from_email = $%d", f.From)
	}
	if f.To != "" {
		add(`EXISTS (SELECT 1 FROM jsonb_array_elements("to") AS rcpt WHERE lower(rcpt->>'email') = lower($%d))`, f.To)
	}
	if f.DateFrom != nil {
		add("created_at >= $%d", *f.DateFrom)
	}
	if f.DateTo != nil {
		add("created_at < $%d", *f.DateTo)
	}
	cond := strings.Join(where, " AND ")

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM transactional.messages WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM transactional.messages WHERE %s ORDER BY created_at DESC, id LIMIT $%d OFFSET $%d`,
		messageColumns, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list, err := collectMessages(rows)
	return list, total, err
}

func (r *Repository) MarkSent(ctx context.Context, tenantID, id uuid.UUID, sesMessageID string, sentAt time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE transactional.messages
		SET status = $3, ses_message_id = $4, sent_at = $5, error = NULL, attempts = attempts + 1
		WHERE tenant_id = $1 AND id = $2`, tenantID, id, domain.StatusSent, sesMessageID, sentAt)
	return err
}

func (r *Repository) MarkFailed(ctx context.Context, tenantID, id uuid.UUID, reason string) error {
	_, err := r.pool.Exec(ctx, `UPDATE transactional.messages
		SET status = $3, error = $4, attempts = attempts + 1
		WHERE tenant_id = $1 AND id = $2`, tenantID, id, domain.StatusFailed, reason)
	return err
}

func (r *Repository) RecordAttempt(ctx context.Context, tenantID, id uuid.UUID, reason string) (int, error) {
	var attempts int
	err := r.pool.QueryRow(ctx, `UPDATE transactional.messages
		SET attempts = attempts + 1, error = $3
		WHERE tenant_id = $1 AND id = $2 RETURNING attempts`, tenantID, id, reason).Scan(&attempts)
	return attempts, err
}

func (r *Repository) TransitionStatus(ctx context.Context, tenantID, id uuid.UUID, to string, from []string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE transactional.messages SET status = $3
		WHERE tenant_id = $1 AND id = $2 AND status = ANY($4)`, tenantID, id, to, from)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) ReleaseDue(ctx context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `WITH due AS (
			SELECT id FROM transactional.messages
			WHERE tenant_id = $1 AND status = $2 AND scheduled_at <= $3
			ORDER BY scheduled_at LIMIT $4
			FOR UPDATE SKIP LOCKED
		)
		UPDATE transactional.messages m SET status = $5
		FROM due WHERE m.id = due.id
		RETURNING m.id`, tenantID, domain.StatusAccepted, now, limit, domain.StatusQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) CountByStatus(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.StatusCount, error) {
	rows, err := r.pool.Query(ctx, `SELECT status, count(*) FROM transactional.messages
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY status ORDER BY status`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.StatusCount{}
	for rows.Next() {
		var c domain.StatusCount
		if err := rows.Scan(&c.Status, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) InsertEvent(ctx context.Context, e *domain.Event) (bool, error) {
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	tag, err := r.pool.Exec(ctx, `INSERT INTO transactional.events (
		id, tenant_id, message_id, type, recipient, detail, sns_message_id, occurred_at, created_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	ON CONFLICT (sns_message_id) DO NOTHING`,
		e.ID, e.TenantID, e.MessageID, e.Type, e.Recipient, e.Detail, e.SNSMessageID, e.OccurredAt, e.CreatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return false, domain.ErrNotFound
		}
		return false, fmt.Errorf("insert event: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) ListEvents(ctx context.Context, tenantID, messageID uuid.UUID) ([]domain.Event, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, tenant_id, message_id, type, recipient, detail, sns_message_id, occurred_at, created_at
		FROM transactional.events WHERE tenant_id = $1 AND message_id = $2 ORDER BY occurred_at, created_at`, tenantID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var e domain.Event
		if err := rows.Scan(&e.ID, &e.TenantID, &e.MessageID, &e.Type, &e.Recipient, &e.Detail, &e.SNSMessageID, &e.OccurredAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repository) GetSubmission(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Submission, error) {
	var s domain.Submission
	err := r.pool.QueryRow(ctx, `SELECT id, tenant_id, idempotency_key, class, message_ids, suppressed, created_at
		FROM transactional.submissions WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, key).
		Scan(&s.ID, &s.TenantID, &s.IdempotencyKey, &s.Class, &s.MessageIDs, &s.Suppressed, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) InsertSubmission(ctx context.Context, s *domain.Submission) (bool, error) {
	// pgx envia un slice nil como SQL NULL y las columnas no lo admiten.
	if s.MessageIDs == nil {
		s.MessageIDs = []uuid.UUID{}
	}
	if s.Suppressed == nil {
		s.Suppressed = []domain.SuppressedRecipient{}
	}
	s.Class = domain.ClassOrDefault(s.Class)
	tag, err := r.pool.Exec(ctx, `INSERT INTO transactional.submissions (id, tenant_id, idempotency_key, class, message_ids, suppressed, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, now()) ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
		s.ID, s.TenantID, s.IdempotencyKey, s.Class, s.MessageIDs, s.Suppressed)
	if err != nil {
		return false, fmt.Errorf("insert submission: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) UpsertSendingDomain(ctx context.Context, d *domain.SendingDomain) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO transactional.sending_domains (tenant_id, domain, status, purpose, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, domain) DO UPDATE SET status = EXCLUDED.status, purpose = EXCLUDED.purpose, updated_at = EXCLUDED.updated_at`,
		d.TenantID, d.Domain, d.Status, d.Purpose, d.UpdatedAt)
	return err
}

func (r *Repository) DeleteSendingDomain(ctx context.Context, tenantID uuid.UUID, name string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM transactional.sending_domains WHERE tenant_id = $1 AND domain = $2`, tenantID, name)
	return err
}

func (r *Repository) GetSendingDomain(ctx context.Context, tenantID uuid.UUID, name string) (*domain.SendingDomain, error) {
	var d domain.SendingDomain
	err := r.pool.QueryRow(ctx, `SELECT tenant_id, domain, status, purpose, updated_at
		FROM transactional.sending_domains WHERE tenant_id = $1 AND domain = $2`, tenantID, name).
		Scan(&d.TenantID, &d.Domain, &d.Status, &d.Purpose, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *Repository) ListSendingDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.SendingDomain, error) {
	rows, err := r.pool.Query(ctx, `SELECT tenant_id, domain, status, purpose, updated_at
		FROM transactional.sending_domains WHERE tenant_id = $1 ORDER BY domain`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SendingDomain{}
	for rows.Next() {
		var d domain.SendingDomain
		if err := rows.Scan(&d.TenantID, &d.Domain, &d.Status, &d.Purpose, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repository) InsertUnsubscribe(ctx context.Context, u *domain.Unsubscribe) (bool, error) {
	tag, err := r.pool.Exec(ctx, `INSERT INTO transactional.unsubscribes (id, tenant_id, email, message_id, created_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (tenant_id, message_id, email) DO NOTHING`,
		u.ID, u.TenantID, u.Email, u.MessageID, u.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("insert unsubscribe: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
