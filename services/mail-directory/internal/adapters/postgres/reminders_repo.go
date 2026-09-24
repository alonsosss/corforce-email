package postgres

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReminderRepo guarda mail.mailbox_reminders con el mismo reparto que ScheduledSendRepo: lo que pide un
// buzon filtra por tenant_id y nombre; la reclamacion y el cierre del trabajador recorren la celda.
type ReminderRepo struct{ pool *db.ContextPool }

func NewReminderRepo(pool *db.ContextPool) *ReminderRepo { return &ReminderRepo{pool: pool} }

const reminderColumns = `id, tenant_id, username, kind, message_id, folder, uid_validity, uid, return_folder, subject, addresses,
	due_at, status, result, attempts, lease_until, last_error, done_at, created_at, updated_at`

func scanReminder(row pgx.Row) (domain.Reminder, error) {
	var r domain.Reminder
	var uidValidity, uid *int64
	err := row.Scan(&r.ID, &r.TenantID, &r.Username, &r.Kind, &r.MessageID, &r.Folder, &uidValidity, &uid, &r.ReturnFolder,
		&r.Subject, &r.Addresses, &r.DueAt, &r.Status, &r.Result, &r.Attempts, &r.LeaseUntil, &r.LastError, &r.DoneAt,
		&r.CreatedAt, &r.UpdatedAt)
	if uidValidity != nil && uid != nil {
		r.UIDValidity, r.UID = uint32(*uidValidity), uint32(*uid)
	}
	return r, err
}

func (r *ReminderRepo) one(ctx context.Context, sql string, args ...any) (*domain.Reminder, error) {
	rem, err := scanReminder(r.pool.QueryRow(ctx, sql, args...))
	if err != nil {
		return nil, mapErr(err)
	}
	return &rem, nil
}

func (r *ReminderRepo) many(ctx context.Context, sql string, args ...any) ([]domain.Reminder, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.Reminder{}
	for rows.Next() {
		rem, err := scanReminder(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, rem)
	}
	return out, mapErr(rows.Err())
}

// nullableUID guarda como NULL la referencia que aun no existe (0).
func nullableUID(v uint32) *int64 {
	if v == 0 {
		return nil
	}
	n := int64(v)
	return &n
}

// Create guarda la fila. El indice unico parcial (username, kind, message_id) de los activos convierte
// un reintento del webmail en ErrAlreadyExists en vez de un segundo recordatorio.
func (r *ReminderRepo) Create(ctx context.Context, rem *domain.Reminder) error {
	created, err := r.one(ctx,
		`INSERT INTO mail.mailbox_reminders (id, tenant_id, username, kind, message_id, folder, uid_validity, uid, return_folder,
		                                     subject, addresses, due_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING `+reminderColumns,
		rem.ID, rem.TenantID, rem.Username, rem.Kind, rem.MessageID, rem.Folder, nullableUID(rem.UIDValidity), nullableUID(rem.UID),
		rem.ReturnFolder, rem.Subject, rem.Addresses, rem.DueAt)
	if err != nil {
		return err
	}
	*rem = *created
	return nil
}

func (r *ReminderRepo) ListByUsername(ctx context.Context, tenantID uuid.UUID, username, kind string, limit int) ([]domain.Reminder, error) {
	return r.many(ctx,
		`SELECT `+reminderColumns+` FROM mail.mailbox_reminders
		  WHERE tenant_id = $1 AND username = $2 AND kind = $3 AND status IN ('pending', 'running', 'failed')
		  ORDER BY due_at, id LIMIT $4`,
		tenantID, username, kind, limit)
}

func (r *ReminderRepo) CountActive(ctx context.Context, tenantID uuid.UUID, username string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM mail.mailbox_reminders
		  WHERE tenant_id = $1 AND username = $2 AND status IN ('pending', 'running')`,
		tenantID, username).Scan(&n)
	return n, mapErr(err)
}

func (r *ReminderRepo) GetForUpdate(ctx context.Context, tenantID uuid.UUID, username string, id uuid.UUID) (*domain.Reminder, error) {
	return r.one(ctx,
		`SELECT `+reminderColumns+` FROM mail.mailbox_reminders
		  WHERE tenant_id = $1 AND username = $2 AND id = $3 FOR UPDATE`,
		tenantID, username, id)
}

func (r *ReminderRepo) Reschedule(ctx context.Context, tenantID, id uuid.UUID, due time.Time) (*domain.Reminder, error) {
	return r.one(ctx,
		`UPDATE mail.mailbox_reminders SET due_at = $3
		  WHERE tenant_id = $1 AND id = $2 AND status = 'pending'
		 RETURNING `+reminderColumns,
		tenantID, id, due)
}

func (r *ReminderRepo) Cancel(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx,
		`UPDATE mail.mailbox_reminders SET status = 'canceled', lease_until = NULL
		  WHERE tenant_id = $1 AND id = $2 AND status IN ('pending', 'failed')`,
		tenantID, id))
}

func (r *ReminderRepo) DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.mailbox_reminders WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	return err
}

// Claim corre en la transaccion del caso de uso, como ScheduledSendRepo.Claim.
func (r *ReminderRepo) Claim(ctx context.Context, p domain.ClaimParams, maxAttempts int, retention time.Duration) ([]domain.Reminder, error) {
	if _, err := r.pool.Exec(ctx,
		`UPDATE mail.mailbox_reminders SET status = 'failed', lease_until = NULL, last_error = $2
		  WHERE id IN (SELECT id FROM mail.mailbox_reminders
		                WHERE status = 'running' AND lease_until < now() AND attempts >= $1
		                FOR UPDATE SKIP LOCKED)`,
		maxAttempts, domain.ReminderLeaseExpiredError); err != nil {
		return nil, mapErr(err)
	}
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM mail.mailbox_reminders
		  WHERE id IN (SELECT id FROM mail.mailbox_reminders
		                WHERE status IN ('done', 'failed', 'canceled') AND updated_at < now() - make_interval(secs => $1)
		                LIMIT 500 FOR UPDATE SKIP LOCKED)`,
		retention.Seconds()); err != nil {
		return nil, mapErr(err)
	}
	return r.many(ctx,
		`WITH due AS (
		     SELECT id FROM mail.mailbox_reminders
		      WHERE (status = 'pending' AND due_at <= now())
		         OR (status = 'running' AND lease_until < now() AND attempts < $2)
		      ORDER BY due_at, id
		      LIMIT $1
		      FOR UPDATE SKIP LOCKED
		 )
		 UPDATE mail.mailbox_reminders m
		    SET status = 'running', attempts = m.attempts + 1, lease_until = now() + make_interval(secs => $3)
		   FROM due WHERE m.id = due.id
		 RETURNING `+prefixed("m.", reminderColumns),
		p.Limit, maxAttempts, p.Lease.Seconds())
}

func (r *ReminderRepo) ClaimedForUpdate(ctx context.Context, id uuid.UUID) (*domain.Reminder, error) {
	return r.one(ctx, `SELECT `+reminderColumns+` FROM mail.mailbox_reminders WHERE id = $1 FOR UPDATE`, id)
}

func (r *ReminderRepo) Close(ctx context.Context, id uuid.UUID, t domain.ReminderTransition) (*domain.Reminder, error) {
	return r.one(ctx,
		`UPDATE mail.mailbox_reminders
		    SET status = $2, result = $3, last_error = $4, lease_until = NULL,
		        due_at = CASE WHEN $2 = 'pending' THEN now() + make_interval(secs => $5) ELSE due_at END,
		        done_at = CASE WHEN $2 = 'done' THEN now() ELSE done_at END
		  WHERE id = $1 AND status = 'running'
		 RETURNING `+reminderColumns,
		id, t.Status, t.Result, t.Error, t.RetryAfter.Seconds())
}
