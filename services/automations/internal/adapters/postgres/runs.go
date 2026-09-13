package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const runColumns = `id, tenant_id, workflow_id, contact_id, trigger_event_id, entry_key, step_index, status,
	next_run_at, attempts, error_code, last_error, lease_token, lease_until, finished_at, created_at, updated_at`

// runColumnsR son las mismas columnas con el alias r (RETURNING de la reserva).
const runColumnsR = `r.id, r.tenant_id, r.workflow_id, r.contact_id, r.trigger_event_id, r.entry_key, r.step_index,
	r.status, r.next_run_at, r.attempts, r.error_code, r.last_error, r.lease_token, r.lease_until, r.finished_at,
	r.created_at, r.updated_at`

const (
	statusWaiting = `'` + string(domain.RunWaiting) + `'`
	statusRunning = `'` + string(domain.RunRunning) + `'`
)

// claimSQL reserva las debidas. El predicado de estado va como literal para que el
// planificador use el indice parcial idx_automations_runs_due. SKIP LOCKED salta las filas
// que otra replica esta reservando en ese instante; las que ya reservo (running con la
// reserva viva) no cumplen el WHERE. La reserva queda confirmada al terminar la sentencia,
// antes de que nadie llame a un vecino.
const claimSQL = `WITH due AS (
	SELECT r.id FROM automations.runs r
	 WHERE r.tenant_id = $1 AND r.next_run_at <= $2
	   AND (r.status = ` + statusWaiting + ` OR (r.status = ` + statusRunning + ` AND r.lease_until < $2))
	   AND EXISTS (SELECT 1 FROM automations.workflows w WHERE w.id = r.workflow_id AND w.` + activeStatus + `)
	 ORDER BY r.next_run_at
	 LIMIT $4
	 FOR UPDATE OF r SKIP LOCKED)
UPDATE automations.runs r
   SET status = ` + statusRunning + `, lease_token = gen_random_uuid(), lease_until = $3
  FROM due
 WHERE r.id = due.id
RETURNING ` + runColumnsR

type RunRepository struct {
	pool *db.ContextPool
}

func NewRunRepository(pool *db.ContextPool) *RunRepository {
	return &RunRepository{pool: pool}
}

func scanRun(row pgx.Row) (*domain.Run, error) {
	var (
		r      domain.Run
		status string
	)
	err := row.Scan(&r.ID, &r.TenantID, &r.WorkflowID, &r.ContactID, &r.TriggerEventID, &r.EntryKey, &r.StepIndex,
		&status, &r.NextRunAt, &r.Attempts, &r.ErrorCode, &r.LastError, &r.LeaseToken, &r.LeaseUntil, &r.FinishedAt,
		&r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Status = domain.RunStatus(status)
	return &r, nil
}

func collectRuns(rows pgx.Rows) ([]domain.Run, error) {
	defer rows.Close()
	out := []domain.Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Enroll inserta con ON CONFLICT DO NOTHING sobre las dos restricciones UNIQUE (por evento
// y por clave de entrada). Sin reentrada, ademas, no entra si el contacto ya recorrio el
// flujo con otra clave (runs anteriores a desactivar la reentrada).
func (r *RunRepository) Enroll(ctx context.Context, run *domain.Run, reEntry bool) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO automations.runs (id, tenant_id, workflow_id, contact_id, trigger_event_id, entry_key,
		        step_index, status, next_run_at)
		 SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::text, $6::text, 0, $7::text, $8::timestamptz
		  WHERE $9::boolean
		     OR NOT EXISTS (SELECT 1 FROM automations.runs x WHERE x.workflow_id = $3::uuid AND x.contact_id = $4::uuid)
		 ON CONFLICT DO NOTHING`,
		run.ID, run.TenantID, run.WorkflowID, run.ContactID, run.TriggerEventID, run.EntryKey,
		string(domain.RunWaiting), run.NextRunAt, reEntry)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *RunRepository) ClaimDue(ctx context.Context, tenantID uuid.UUID, now, leaseUntil time.Time, limit int) ([]domain.Run, error) {
	rows, err := r.pool.Query(ctx, claimSQL, tenantID, now, leaseUntil, limit)
	if err != nil {
		return nil, err
	}
	return collectRuns(rows)
}

func (r *RunRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Run, error) {
	return scanRun(r.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM automations.runs WHERE tenant_id = $1 AND id = $2`, tenantID, id))
}

func (r *RunRepository) List(ctx context.Context, tenantID uuid.UUID, f ports.RunFilter) ([]domain.Run, int64, error) {
	const where = `tenant_id = $1 AND workflow_id = $2 AND ($3 = '' OR status = $3)`
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM automations.runs WHERE `+where,
		tenantID, f.WorkflowID, string(f.Status)).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+runColumns+` FROM automations.runs WHERE `+where+`
		  ORDER BY created_at DESC, id LIMIT $4 OFFSET $5`,
		tenantID, f.WorkflowID, string(f.Status), f.PerPage, offset(f.Page, f.PerPage))
	if err != nil {
		return nil, 0, err
	}
	out, err := collectRuns(rows)
	return out, total, err
}

// held es la condicion de toda escritura del ejecutor: la ejecucion sigue running y con
// la reserva de quien escribe.
const held = `tenant_id = $1 AND id = $2 AND lease_token = $3 AND status = ` + statusRunning

func (r *RunRepository) Advance(ctx context.Context, run *domain.Run, nextIndex int, nextRunAt time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE automations.runs
		    SET step_index = $4, status = $5, next_run_at = $6, attempts = 0, error_code = '', last_error = '',
		        lease_token = NULL, lease_until = NULL
		  WHERE `+held+` AND step_index = $7`,
		run.TenantID, run.ID, run.LeaseToken, nextIndex, string(domain.RunWaiting), nextRunAt, run.StepIndex)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *RunRepository) Reschedule(ctx context.Context, run *domain.Run, nextRunAt time.Time, attempts int, code, reason string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE automations.runs
		    SET status = $4, next_run_at = $5, attempts = $6, error_code = $7, last_error = $8,
		        lease_token = NULL, lease_until = NULL
		  WHERE `+held,
		run.TenantID, run.ID, run.LeaseToken, string(domain.RunWaiting), nextRunAt, attempts, code, domain.TruncateReason(reason))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *RunRepository) Finish(ctx context.Context, run *domain.Run, status domain.RunStatus, code, reason string, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE automations.runs
		    SET status = $4, error_code = $5, last_error = $6, finished_at = $7, lease_token = NULL, lease_until = NULL
		  WHERE `+held,
		run.TenantID, run.ID, run.LeaseToken, string(status), code, domain.TruncateReason(reason), at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *RunRepository) Release(ctx context.Context, run *domain.Run) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE automations.runs SET status = $4, lease_token = NULL, lease_until = NULL WHERE `+held,
		run.TenantID, run.ID, run.LeaseToken, string(domain.RunWaiting))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *RunRepository) CancelByWorkflow(ctx context.Context, tenantID, workflowID uuid.UUID, at time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE automations.runs
		    SET status = $3, error_code = $4, finished_at = $5, lease_token = NULL, lease_until = NULL
		  WHERE tenant_id = $1 AND workflow_id = $2 AND status IN (`+statusWaiting+`, `+statusRunning+`)`,
		tenantID, workflowID, string(domain.RunCancelled), domain.CodeWorkflowArchived, at)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *RunRepository) CountRecentFailures(ctx context.Context, tenantID, workflowID uuid.UUID, codes []string, since time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM automations.runs
		  WHERE tenant_id = $1 AND workflow_id = $2 AND status = $3 AND error_code = ANY($4) AND finished_at >= $5`,
		tenantID, workflowID, string(domain.RunFailed), codes, since).Scan(&n)
	return n, err
}
