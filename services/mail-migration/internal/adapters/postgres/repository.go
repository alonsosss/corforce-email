package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Repository persiste en el esquema mail_migration de la base de la empresa. El pool llega en el
// contexto (TenantPoolMiddleware en las peticiones, TenantDB en el ejecutor) y toda consulta filtra
// ademas por tenant_id.
type Repository struct {
	pool *db.ContextPool
}

func NewRepository(pool *db.ContextPool) *Repository { return &Repository{pool: pool} }

// Transact permite al caso de uso confirmar el cambio de un trabajo junto con su evento.
func (r *Repository) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	if db.HasTx(ctx) {
		return fn(ctx)
	}
	return r.pool.Transact(ctx, fn)
}

// tenantLockClass es el espacio de los cerrojos consultivos por empresa: serializa el conteo del
// limite y el alta de un trabajo, que de otro modo dos altas simultaneas podrian superar juntas.
const tenantLockClass int32 = 0x6d69676a // "migj"

const uniqueViolation = "23505"

const jobColumns = `id, tenant_id, mailbox_id, mailbox_username, source_host, source_port, source_tls, source_username,
 source_password_enc, status, phase, progress, attempt, last_error_code, last_error_message, requested_by, runner_id,
 lease_id, lease_expires_at, heartbeat_at, cancel_requested_at, started_at, finished_at, created_at, updated_at`

func scanJob(row pgx.Row) (*domain.Job, error) {
	j := &domain.Job{}
	var errCode, errMessage string
	err := row.Scan(&j.ID, &j.TenantID, &j.MailboxID, &j.MailboxUsername, &j.SourceHost, &j.SourcePort, &j.SourceTLS,
		&j.SourceUsername, &j.SourcePasswordEnc, &j.Status, &j.Phase, &j.Progress, &j.Attempt, &errCode, &errMessage,
		&j.RequestedBy, &j.RunnerID, &j.LeaseID, &j.LeaseExpiresAt, &j.HeartbeatAt, &j.CancelRequestedAt,
		&j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	if errCode != "" {
		j.LastError = &domain.JobError{Code: domain.ErrorCode(errCode), Message: errMessage}
	}
	if j.Progress.Folders == nil {
		j.Progress.Folders = []domain.FolderProgress{}
	}
	return j, nil
}

func errorFields(e *domain.JobError) (string, string) {
	if e == nil {
		return "", ""
	}
	return string(e.Code), e.Message
}

func (r *Repository) Insert(ctx context.Context, j *domain.Job, maxActive int) error {
	if _, err := r.pool.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, tenantLockClass, j.TenantID.String()); err != nil {
		return err
	}
	active, err := r.CountActive(ctx, j.TenantID)
	if err != nil {
		return err
	}
	if active >= maxActive {
		return domain.ErrTenantLimitReached
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO mail_migration.jobs (id, tenant_id, mailbox_id, mailbox_username, source_host, source_port, source_tls,
 source_username, source_password_enc, status, progress, requested_by, created_at, updated_at)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)`,
		j.ID, j.TenantID, j.MailboxID, j.MailboxUsername, j.SourceHost, j.SourcePort, string(j.SourceTLS),
		j.SourceUsername, j.SourcePasswordEnc, string(j.Status), j.Progress, j.RequestedBy, j.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return domain.ErrJobAlreadyActive
		}
		return err
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Job, error) {
	return scanJob(r.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM mail_migration.jobs WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *Repository) List(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter, p ports.Page) ([]domain.Job, int64, error) {
	mailbox := f.MailboxID
	var status *string
	if f.Status != nil {
		s := string(*f.Status)
		status = &s
	}
	const where = ` WHERE tenant_id = $1 AND ($2::uuid IS NULL OR mailbox_id = $2) AND ($3::text IS NULL OR status = $3)`
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM mail_migration.jobs`+where, tenantID, mailbox, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+jobColumns+` FROM mail_migration.jobs`+where+` ORDER BY created_at DESC, id LIMIT $4 OFFSET $5`,
		tenantID, mailbox, status, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *j)
	}
	return out, total, rows.Err()
}

func (r *Repository) CountActive(ctx context.Context, tenantID uuid.UUID) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM mail_migration.jobs WHERE tenant_id = $1 AND status IN ('pending', 'running')`, tenantID).Scan(&n)
	return n, err
}

func (r *Repository) RequestCancel(ctx context.Context, tenantID, id uuid.UUID, at time.Time) (*domain.Job, error) {
	j, err := scanJob(r.pool.QueryRow(ctx,
		`UPDATE mail_migration.jobs SET
   status = CASE WHEN status = 'pending' THEN 'cancelled' ELSE status END,
   source_password_enc = CASE WHEN status = 'pending' THEN NULL ELSE source_password_enc END,
   finished_at = CASE WHEN status = 'pending' THEN $3 ELSE finished_at END,
   last_error_code = CASE WHEN status = 'pending' THEN $4 ELSE last_error_code END,
   cancel_requested_at = COALESCE(cancel_requested_at, $3)
 WHERE tenant_id = $1 AND id = $2 AND status IN ('pending', 'running')
 RETURNING `+jobColumns, tenantID, id, at, string(domain.CodeCancelled)))
	if errors.Is(err, domain.ErrNotFound) {
		if _, gerr := r.Get(ctx, tenantID, id); gerr == nil {
			return nil, domain.ErrNotCancellable
		}
		return nil, domain.ErrNotFound
	}
	return j, err
}

func (r *Repository) Claim(ctx context.Context, tenantID uuid.UUID, p ports.ClaimParams) (*domain.Job, error) {
	j, err := scanJob(r.pool.QueryRow(ctx,
		`WITH candidate AS (
   SELECT id AS candidate_id FROM mail_migration.jobs
    WHERE tenant_id = $1 AND cancel_requested_at IS NULL
      AND (status = 'pending' OR (status = 'running' AND lease_expires_at < $2 AND attempt < $3))
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1)
 UPDATE mail_migration.jobs j SET status = 'running', attempt = j.attempt + 1, lease_id = $4, lease_expires_at = $5,
   runner_id = $6, heartbeat_at = $2, started_at = COALESCE(j.started_at, $2), phase = 'initial'
  FROM candidate WHERE j.id = candidate.candidate_id
 RETURNING `+jobColumns,
		tenantID, p.Now, p.MaxAttempts, p.LeaseID, p.LeaseUntil, p.RunnerID))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return j, err
}

func (r *Repository) Heartbeat(ctx context.Context, tenantID, id uuid.UUID, p ports.HeartbeatParams) (bool, error) {
	var cancel bool
	err := r.pool.QueryRow(ctx,
		`UPDATE mail_migration.jobs SET phase = $4, progress = $5, heartbeat_at = $6, lease_expires_at = $7
 WHERE tenant_id = $1 AND id = $2 AND lease_id = $3 AND status = 'running'
 RETURNING cancel_requested_at IS NOT NULL`,
		tenantID, id, p.LeaseID, string(p.Phase), p.Progress, p.Now, p.LeaseUntil).Scan(&cancel)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrLeaseLost
	}
	return cancel, err
}

func (r *Repository) Finish(ctx context.Context, tenantID, id uuid.UUID, p ports.FinishParams) (*domain.Job, error) {
	code, message := errorFields(p.Error)
	j, err := scanJob(r.pool.QueryRow(ctx,
		`UPDATE mail_migration.jobs SET status = $4, progress = $5, last_error_code = $6, last_error_message = $7,
   finished_at = $8, source_password_enc = NULL, lease_id = NULL, lease_expires_at = NULL
 WHERE tenant_id = $1 AND id = $2 AND lease_id = $3 AND status = 'running'
 RETURNING `+jobColumns,
		tenantID, id, p.LeaseID, string(p.Status), p.Progress, code, message, p.Now))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, domain.ErrLeaseLost
	}
	return j, err
}

func (r *Repository) ExpireLost(ctx context.Context, tenantID uuid.UUID, now time.Time, maxAttempts int) ([]domain.Job, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE mail_migration.jobs SET
   status = CASE WHEN cancel_requested_at IS NOT NULL THEN 'cancelled' ELSE 'failed' END,
   last_error_code = CASE WHEN cancel_requested_at IS NOT NULL THEN $4 ELSE $5 END,
   last_error_message = CASE WHEN cancel_requested_at IS NOT NULL THEN '' ELSE $6 END,
   finished_at = $2, source_password_enc = NULL, lease_id = NULL, lease_expires_at = NULL
 WHERE tenant_id = $1 AND status = 'running' AND lease_expires_at < $2
   AND (attempt >= $3 OR cancel_requested_at IS NOT NULL)
 RETURNING `+jobColumns,
		tenantID, now, maxAttempts, string(domain.CodeCancelled), string(domain.CodeRunnerLost), domain.RunnerLostError().Message)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}
