package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ScheduledSendRepo guarda mail.scheduled_sends. Lo que pide un buzon filtra por tenant_id y nombre;
// la reclamacion y el cierre del trabajador recorren toda la celda con el rol de servicio (sin
// usuario en la peticion el Transactor no cambia a mail_app), como MailboxLocator.
type ScheduledSendRepo struct{ pool *db.ContextPool }

func NewScheduledSendRepo(pool *db.ContextPool) *ScheduledSendRepo {
	return &ScheduledSendRepo{pool: pool}
}

const scheduledColumns = `id, tenant_id, username, message_id, folder, uid_validity, uid, send_at, subject, recipients,
	status, attempts, lease_until, last_error, sent_at, created_at, updated_at`

// prefixed califica cada columna de la lista con el alias de la tabla (UPDATE ... FROM, donde id es
// ambiguo).
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func scanScheduled(row pgx.Row) (domain.ScheduledSend, error) {
	var s domain.ScheduledSend
	var uidValidity, uid int64
	err := row.Scan(&s.ID, &s.TenantID, &s.Username, &s.MessageID, &s.Folder, &uidValidity, &uid, &s.SendAt, &s.Subject,
		&s.Recipients, &s.Status, &s.Attempts, &s.LeaseUntil, &s.LastError, &s.SentAt, &s.CreatedAt, &s.UpdatedAt)
	s.UIDValidity, s.UID = uint32(uidValidity), uint32(uid)
	return s, err
}

func (r *ScheduledSendRepo) one(ctx context.Context, sql string, args ...any) (*domain.ScheduledSend, error) {
	s, err := scanScheduled(r.pool.QueryRow(ctx, sql, args...))
	if err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

func (r *ScheduledSendRepo) many(ctx context.Context, sql string, args ...any) ([]domain.ScheduledSend, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []domain.ScheduledSend{}
	for rows.Next() {
		s, err := scanScheduled(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}

// Create guarda la fila. El indice unico parcial (username, message_id) de las activas convierte un
// reintento del webmail en ErrAlreadyExists en vez de un segundo envio.
func (r *ScheduledSendRepo) Create(ctx context.Context, s *domain.ScheduledSend) error {
	created, err := r.one(ctx,
		`INSERT INTO mail.scheduled_sends (id, tenant_id, username, message_id, folder, uid_validity, uid, send_at, subject, recipients)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING `+scheduledColumns,
		s.ID, s.TenantID, s.Username, s.MessageID, s.Folder, int64(s.UIDValidity), int64(s.UID), s.SendAt, s.Subject, s.Recipients)
	if err != nil {
		return err
	}
	*s = *created
	return nil
}

func (r *ScheduledSendRepo) ListByUsername(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.ScheduledSend, error) {
	return r.many(ctx,
		`SELECT `+scheduledColumns+` FROM mail.scheduled_sends
		  WHERE tenant_id = $1 AND username = $2 AND status IN ('pending', 'sending', 'failed')
		  ORDER BY send_at, id LIMIT $3`,
		tenantID, username, limit)
}

func (r *ScheduledSendRepo) CountPending(ctx context.Context, tenantID uuid.UUID, username string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM mail.scheduled_sends
		  WHERE tenant_id = $1 AND username = $2 AND status IN ('pending', 'sending')`,
		tenantID, username).Scan(&n)
	return n, mapErr(err)
}

func (r *ScheduledSendRepo) GetForUpdate(ctx context.Context, tenantID uuid.UUID, username string, id uuid.UUID) (*domain.ScheduledSend, error) {
	return r.one(ctx,
		`SELECT `+scheduledColumns+` FROM mail.scheduled_sends
		  WHERE tenant_id = $1 AND username = $2 AND id = $3 FOR UPDATE`,
		tenantID, username, id)
}

func (r *ScheduledSendRepo) Reschedule(ctx context.Context, tenantID, id uuid.UUID, sendAt time.Time) (*domain.ScheduledSend, error) {
	return r.one(ctx,
		`UPDATE mail.scheduled_sends SET send_at = $3
		  WHERE tenant_id = $1 AND id = $2 AND status = 'pending'
		 RETURNING `+scheduledColumns,
		tenantID, id, sendAt)
}

func (r *ScheduledSendRepo) Cancel(ctx context.Context, tenantID, id uuid.UUID) error {
	return affected(r.pool.Exec(ctx,
		`UPDATE mail.scheduled_sends SET status = 'canceled', lease_until = NULL
		  WHERE tenant_id = $1 AND id = $2 AND status IN ('pending', 'failed')`,
		tenantID, id))
}

func (r *ScheduledSendRepo) DeleteByUsername(ctx context.Context, tenantID uuid.UUID, username string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM mail.scheduled_sends WHERE tenant_id = $1 AND username = $2`, tenantID, username)
	return err
}

// Claim corre en la transaccion del caso de uso. El arriendo vencido de una fila en sending se
// reclama de nuevo mientras le queden intentos: su trabajador murio sin cerrarla.
func (r *ScheduledSendRepo) Claim(ctx context.Context, p domain.ClaimParams, maxAttempts int, retention time.Duration) ([]domain.ScheduledSend, error) {
	if _, err := r.pool.Exec(ctx,
		`UPDATE mail.scheduled_sends SET status = 'failed', lease_until = NULL, last_error = $2
		  WHERE id IN (SELECT id FROM mail.scheduled_sends
		                WHERE status = 'sending' AND lease_until < now() AND attempts >= $1
		                FOR UPDATE SKIP LOCKED)`,
		maxAttempts, domain.ScheduledLeaseExpiredError); err != nil {
		return nil, mapErr(err)
	}
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM mail.scheduled_sends
		  WHERE id IN (SELECT id FROM mail.scheduled_sends
		                WHERE status IN ('sent', 'failed', 'canceled') AND updated_at < now() - make_interval(secs => $1)
		                LIMIT 500 FOR UPDATE SKIP LOCKED)`,
		retention.Seconds()); err != nil {
		return nil, mapErr(err)
	}
	return r.many(ctx,
		`WITH due AS (
		     SELECT id FROM mail.scheduled_sends
		      WHERE (status = 'pending' AND send_at <= now())
		         OR (status = 'sending' AND lease_until < now() AND attempts < $2)
		      ORDER BY send_at, id
		      LIMIT $1
		      FOR UPDATE SKIP LOCKED
		 )
		 UPDATE mail.scheduled_sends s
		    SET status = 'sending', attempts = s.attempts + 1, lease_until = now() + make_interval(secs => $3)
		   FROM due WHERE s.id = due.id
		 RETURNING `+prefixed("s.", scheduledColumns),
		p.Limit, maxAttempts, p.Lease.Seconds())
}

func (r *ScheduledSendRepo) ClaimedForUpdate(ctx context.Context, id uuid.UUID) (*domain.ScheduledSend, error) {
	return r.one(ctx, `SELECT `+scheduledColumns+` FROM mail.scheduled_sends WHERE id = $1 FOR UPDATE`, id)
}

func (r *ScheduledSendRepo) Close(ctx context.Context, id uuid.UUID, t domain.ScheduledTransition) (*domain.ScheduledSend, error) {
	return r.one(ctx,
		`UPDATE mail.scheduled_sends
		    SET status = $2, last_error = $3, lease_until = NULL,
		        send_at = CASE WHEN $2 = 'pending' THEN now() + make_interval(secs => $4) ELSE send_at END,
		        sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END
		  WHERE id = $1 AND status = 'sending'
		 RETURNING `+scheduledColumns,
		id, t.Status, t.Error, t.RetryAfter.Seconds())
}
