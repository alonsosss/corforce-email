package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const uniqueViolation = "23505"

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraint
}

// Transactor implementa ports.Transactor sobre la base de la empresa del contexto. Es
// reentrante: dentro de una transaccion abierta, fn corre en ella.
type Transactor struct {
	pool *db.ContextPool
}

func NewTransactor(pool *db.ContextPool) *Transactor { return &Transactor{pool: pool} }

func (t *Transactor) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	if db.HasTx(ctx) {
		return fn(ctx)
	}
	return t.pool.Transact(ctx, fn)
}

type JobDefinitionRepo struct {
	pool *db.ContextPool
}

func NewJobDefinitionRepo(pool *db.ContextPool) *JobDefinitionRepo {
	return &JobDefinitionRepo{pool: pool}
}

const jobColumns = `id,tenant_id,name,code,description,job_type,cron_expression,timezone,interval_minutes,handler,payload,is_active,max_retries,timeout_seconds,created_at,updated_at`

// jobColumnsJD son las mismas columnas con el alias jd de job_definitions.
var jobColumnsJD = "jd." + strings.ReplaceAll(jobColumns, ",", ",jd.")

// jobVisibleTo es la condicion de los trabajos que ve una empresa: los suyos y los de
// plataforma. El parametro $1 es la empresa.
const jobVisibleTo = ` WHERE (jd.tenant_id=$1 OR jd.tenant_id IS NULL)`

// overviewSelect lee cada trabajo con su calendario y su ultima ejecucion (la creada mas
// reciente, por idx_job_executions_job) en la misma consulta: un listado no hace una lectura
// por trabajo.
var overviewSelect = `SELECT ` + jobColumnsJD + `, js.next_run_at, js.last_run_at,
       le.id, le.status, le.completed_at, le.failure_reason
  FROM scheduler.job_definitions jd
  LEFT JOIN scheduler.job_schedules js ON js.job_id = jd.id
  LEFT JOIN LATERAL (
       SELECT e.id, e.status, e.completed_at, e.failure_reason
         FROM scheduler.job_executions e
        WHERE e.job_id = jd.id
        ORDER BY e.created_at DESC, e.id DESC
        LIMIT 1) le ON true`

func jobDest(j *domain.JobDefinition) []any {
	return []any{&j.ID, &j.TenantID, &j.Name, &j.Code, &j.Description, &j.JobType, &j.CronExpression,
		&j.Timezone, &j.IntervalMinutes, &j.Handler, &j.Payload, &j.IsActive, &j.MaxRetries, &j.TimeoutSeconds,
		&j.CreatedAt, &j.UpdatedAt}
}

func scanJob(row pgx.Row) (*domain.JobDefinition, error) {
	j := &domain.JobDefinition{}
	err := row.Scan(jobDest(j)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

func scanOverview(row pgx.Row) (*domain.JobOverview, error) {
	var j domain.JobDefinition
	var nextRunAt, lastRunAt, completedAt *time.Time
	var execID *uuid.UUID
	var status, failureReason *string
	err := row.Scan(append(jobDest(&j), &nextRunAt, &lastRunAt, &execID, &status, &completedAt, &failureReason)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	var last *domain.ExecutionSummary
	if execID != nil && status != nil {
		last = &domain.ExecutionSummary{ID: *execID, Status: *status, CompletedAt: completedAt, FailureReason: failureReason}
	}
	return domain.NewJobOverview(j, nextRunAt, lastRunAt, last), nil
}

func (r *JobDefinitionRepo) Create(ctx context.Context, job *domain.JobDefinition) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.job_definitions (id,tenant_id,name,code,description,job_type,cron_expression,timezone,interval_minutes,handler,payload,is_active,max_retries,timeout_seconds,created_at,updated_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		job.ID, job.TenantID, job.Name, job.Code, job.Description, job.JobType, job.CronExpression,
		job.Timezone, job.IntervalMinutes, job.Handler, job.Payload, job.IsActive, job.MaxRetries, job.TimeoutSeconds,
		job.CreatedAt, job.UpdatedAt,
	)
	if isUniqueViolation(err, "job_definitions_code_key") {
		return domain.ErrJobAlreadyExists
	}
	return err
}

func (r *JobDefinitionRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	return scanJob(r.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM scheduler.job_definitions WHERE id=$1 AND (tenant_id=$2 OR tenant_id IS NULL)`, id, tenantID))
}

func (r *JobDefinitionRepo) GetByCode(ctx context.Context, code string) (*domain.JobDefinition, error) {
	return scanJob(r.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM scheduler.job_definitions WHERE code=$1`, code))
}

func (r *JobDefinitionRepo) GetOverview(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobOverview, error) {
	return scanOverview(r.pool.QueryRow(ctx, overviewSelect+jobVisibleTo+` AND jd.id=$2`, tenantID, id))
}

// List cuenta y lee la pagina con dos consultas, sea cual sea el numero de trabajos. El orden
// por nombre desempata por id para que las paginas no se solapen.
func (r *JobDefinitionRepo) List(ctx context.Context, f domain.JobFilter) ([]*domain.JobOverview, int64, error) {
	where := jobVisibleTo
	args := []any{f.TenantID}
	if f.IsActive != nil {
		args = append(args, *f.IsActive)
		where += ` AND jd.is_active=$` + strconv.Itoa(len(args))
	}
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM scheduler.job_definitions jd`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	n := len(args)
	rows, err := r.pool.Query(ctx,
		overviewSelect+where+` ORDER BY jd.name, jd.id LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2),
		append(args, f.PerPage, f.Offset())...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []*domain.JobOverview
	for rows.Next() {
		o, err := scanOverview(rows)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, o)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// Update y Deactivate escriben por id y empresa: una fila de otra empresa no se toca aunque
// alguien llegue con su id. IS NOT DISTINCT FROM iguala tambien la de plataforma (NULL).
func (r *JobDefinitionRepo) Update(ctx context.Context, job *domain.JobDefinition) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE scheduler.job_definitions SET name=$1,description=$2,job_type=$3,cron_expression=$4,timezone=$5,interval_minutes=$6,handler=$7,payload=$8,is_active=$9,max_retries=$10,timeout_seconds=$11,updated_at=$12
		  WHERE id=$13 AND tenant_id IS NOT DISTINCT FROM $14`,
		job.Name, job.Description, job.JobType, job.CronExpression, job.Timezone, job.IntervalMinutes,
		job.Handler, job.Payload, job.IsActive, job.MaxRetries, job.TimeoutSeconds, job.UpdatedAt, job.ID, job.TenantID,
	)
	return affectedOr(tag, err, domain.ErrJobNotFound)
}

func (r *JobDefinitionRepo) Deactivate(ctx context.Context, id uuid.UUID, owner *uuid.UUID, updatedAt time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE scheduler.job_definitions SET is_active=false, updated_at=$1 WHERE id=$2 AND tenant_id IS NOT DISTINCT FROM $3`,
		updatedAt, id, owner)
	return affectedOr(tag, err, domain.ErrJobNotFound)
}

// affectedOr traduce una escritura que no encontro su fila en notFound.
func affectedOr(tag pgconn.CommandTag, err error, notFound error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFound
	}
	return nil
}

type JobExecutionRepo struct {
	pool *db.ContextPool
}

func NewJobExecutionRepo(pool *db.ContextPool) *JobExecutionRepo {
	return &JobExecutionRepo{pool: pool}
}

const executionColumns = `id,job_id,tenant_id,status,started_at,completed_at,duration,result,error_message,retry_count,created_at,deadline_at,next_attempt_at,retry_of,failure_reason`

// retryOfUnique es el indice que garantiza un solo reintento por ejecucion.
const retryOfUnique = "uq_job_executions_retry_of"

func scanExecution(row pgx.Row) (*domain.JobExecution, error) {
	e := &domain.JobExecution{}
	err := row.Scan(&e.ID, &e.JobID, &e.TenantID, &e.Status, &e.StartedAt, &e.CompletedAt,
		&e.Duration, &e.Result, &e.ErrorMessage, &e.RetryCount, &e.CreatedAt,
		&e.DeadlineAt, &e.NextAttemptAt, &e.RetryOf, &e.FailureReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrExecutionNotFound
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (r *JobExecutionRepo) Create(ctx context.Context, exec *domain.JobExecution) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.job_executions (id,job_id,tenant_id,status,started_at,completed_at,duration,result,error_message,retry_count,created_at,deadline_at,next_attempt_at,retry_of,failure_reason)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		exec.ID, exec.JobID, exec.TenantID, exec.Status, exec.StartedAt, exec.CompletedAt,
		exec.Duration, exec.Result, exec.ErrorMessage, exec.RetryCount, exec.CreatedAt,
		exec.DeadlineAt, exec.NextAttemptAt, exec.RetryOf, exec.FailureReason,
	)
	if isUniqueViolation(err, retryOfUnique) {
		return domain.ErrAlreadyRetried
	}
	return err
}

func (r *JobExecutionRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	return scanExecution(r.pool.QueryRow(ctx,
		`SELECT `+executionColumns+` FROM scheduler.job_executions WHERE id=$1 AND (tenant_id=$2 OR tenant_id IS NULL)`, id, tenantID))
}

func (r *JobExecutionRepo) GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	return scanExecution(r.pool.QueryRow(ctx,
		`SELECT `+executionColumns+` FROM scheduler.job_executions WHERE id=$1 AND (tenant_id=$2 OR tenant_id IS NULL) FOR UPDATE`, id, tenantID))
}

// executionVisibleTo son las ejecuciones que ve la empresa $2: las suyas y las de plataforma.
const executionVisibleTo = ` AND (tenant_id=$2 OR tenant_id IS NULL)`

// GetByJob cuenta y lee la pagina con dos consultas; el desempate por id evita que dos
// paginas se solapen cuando varias ejecuciones comparten created_at.
func (r *JobExecutionRepo) GetByJob(ctx context.Context, jobID, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM scheduler.job_executions WHERE job_id=$1`+executionVisibleTo, jobID, tenantID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	list, err := r.list(ctx,
		`SELECT `+executionColumns+` FROM scheduler.job_executions WHERE job_id=$1`+executionVisibleTo+`
		  ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4`,
		jobID, tenantID, perPage, domain.PageOffset(page, perPage))
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *JobExecutionRepo) Update(ctx context.Context, exec *domain.JobExecution) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE scheduler.job_executions SET status=$1,started_at=$2,completed_at=$3,duration=$4,result=$5,error_message=$6,retry_count=$7,deadline_at=$8,next_attempt_at=$9,failure_reason=$10
		  WHERE id=$11 AND tenant_id IS NOT DISTINCT FROM $12`,
		exec.Status, exec.StartedAt, exec.CompletedAt, exec.Duration, exec.Result, exec.ErrorMessage, exec.RetryCount,
		exec.DeadlineAt, exec.NextAttemptAt, exec.FailureReason, exec.ID, exec.TenantID,
	)
	return affectedOr(tag, err, domain.ErrExecutionNotFound)
}

// ListRunning cuenta y lee la pagina con dos consultas (idx_job_executions_active); el
// desempate por id evita que dos paginas se solapen cuando varias comparten created_at.
func (r *JobExecutionRepo) ListRunning(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error) {
	const where = ` FROM scheduler.job_executions WHERE status IN ('pending','running') AND (tenant_id=$1 OR tenant_id IS NULL)`
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*)`+where, tenantID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	list, err := r.list(ctx, `SELECT `+executionColumns+where+` ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`,
		tenantID, perPage, domain.PageOffset(page, perPage))
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *JobExecutionRepo) ClaimOverdue(ctx context.Context, now time.Time) (*domain.JobExecution, error) {
	return claim(r.pool.QueryRow(ctx,
		`SELECT `+executionColumns+` FROM scheduler.job_executions
		  WHERE status IN ('pending','running') AND deadline_at <= $1
		  ORDER BY deadline_at LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
}

func (r *JobExecutionRepo) ClaimDispatchable(ctx context.Context, now time.Time) (*domain.JobExecution, error) {
	return claim(r.pool.QueryRow(ctx,
		`SELECT `+executionColumns+` FROM scheduler.job_executions
		  WHERE status = 'pending' AND deadline_at IS NULL AND next_attempt_at <= $1
		  ORDER BY next_attempt_at LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
}

// claim traduce "no hay fila" en nil: para un barrido, no encontrar nada no es un error.
func claim(row pgx.Row) (*domain.JobExecution, error) {
	e, err := scanExecution(row)
	if errors.Is(err, domain.ErrExecutionNotFound) {
		return nil, nil
	}
	return e, err
}

func (r *JobExecutionRepo) list(ctx context.Context, sql string, args ...interface{}) ([]*domain.JobExecution, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*domain.JobExecution
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, rows.Err()
}

type ScheduledTaskRepo struct {
	pool *db.ContextPool
}

func NewScheduledTaskRepo(pool *db.ContextPool) *ScheduledTaskRepo {
	return &ScheduledTaskRepo{pool: pool}
}

func (r *ScheduledTaskRepo) Create(ctx context.Context, task *domain.ScheduledTask) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.scheduled_tasks (id,tenant_id,name,description,trigger_at,handler,payload,status,executed_at,created_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		task.ID, task.TenantID, task.Name, task.Description, task.TriggerAt, task.Handler, task.Payload, task.Status, task.ExecutedAt, task.CreatedAt,
	)
	return err
}

const taskColumns = `id,tenant_id,name,description,trigger_at,handler,payload,status,executed_at,created_at`

// scanTask distingue la tarea que no esta (domain.ErrTaskNotFound) de un fallo de la base,
// que sube tal cual y acaba en un 500 registrado.
func scanTask(row pgx.Row) (*domain.ScheduledTask, error) {
	t := &domain.ScheduledTask{}
	err := row.Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.TriggerAt, &t.Handler, &t.Payload, &t.Status, &t.ExecutedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (r *ScheduledTaskRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	return scanTask(r.pool.QueryRow(ctx,
		`SELECT `+taskColumns+` FROM scheduler.scheduled_tasks WHERE id=$1 AND tenant_id=$2`, id, tenantID))
}

func (r *ScheduledTaskRepo) GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	return scanTask(r.pool.QueryRow(ctx,
		`SELECT `+taskColumns+` FROM scheduler.scheduled_tasks WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, id, tenantID))
}

// ListPending cuenta y lee la pagina con dos consultas (idx_scheduled_tasks_tenant); el
// desempate por id evita que dos paginas se solapen.
func (r *ScheduledTaskRepo) ListPending(ctx context.Context, f domain.TaskFilter) ([]*domain.ScheduledTask, int64, error) {
	const where = ` FROM scheduler.scheduled_tasks WHERE tenant_id=$1 AND status='scheduled' AND trigger_at <= $2`
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*)`+where, f.TenantID, f.Before).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	list, err := r.list(ctx, `SELECT `+taskColumns+where+` ORDER BY trigger_at, id LIMIT $3 OFFSET $4`,
		f.TenantID, f.Before, f.PerPage, f.Offset())
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *ScheduledTaskRepo) ListDue(ctx context.Context, now time.Time) ([]*domain.ScheduledTask, error) {
	return r.list(ctx, `SELECT `+taskColumns+` FROM scheduler.scheduled_tasks
		  WHERE status='scheduled' AND trigger_at <= $1 ORDER BY trigger_at, id`, now)
}

func (r *ScheduledTaskRepo) list(ctx context.Context, sql string, args ...any) ([]*domain.ScheduledTask, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*domain.ScheduledTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// MarkExecuted es la unica escritura de executed_at, con la hora del caso de uso. Solo pasa
// una tarea que sigue programada: la cancelada mientras tanto no se reabre.
func (r *ScheduledTaskRepo) MarkExecuted(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE scheduler.scheduled_tasks SET status='executed', executed_at=$1 WHERE id=$2 AND status='scheduled'`, at, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *ScheduledTaskRepo) Cancel(ctx context.Context, id, tenantID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE scheduler.scheduled_tasks SET status='cancelled' WHERE id=$1 AND tenant_id=$2 AND status='scheduled'`, id, tenantID)
	return affectedOr(tag, err, domain.ErrTaskNotFound)
}

type JobScheduleRepo struct {
	pool *db.ContextPool
}

func NewJobScheduleRepo(pool *db.ContextPool) *JobScheduleRepo {
	return &JobScheduleRepo{pool: pool}
}

// UpdateNextRun anota last_run_at con la hora del caso de uso y no con la de la base: es la
// misma que lleva la ejecucion despachada.
func (r *JobScheduleRepo) UpdateNextRun(ctx context.Context, jobID uuid.UUID, nextRunAt, ranAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.job_schedules (job_id,next_run_at,last_run_at,is_locked) VALUES ($1,$2,$3,false)
 ON CONFLICT (job_id) DO UPDATE SET next_run_at=EXCLUDED.next_run_at, last_run_at=EXCLUDED.last_run_at`,
		jobID, nextRunAt, ranAt,
	)
	return err
}

func (r *JobScheduleRepo) Get(ctx context.Context, jobID uuid.UUID) (*domain.JobSchedule, error) {
	s := &domain.JobSchedule{JobID: jobID}
	err := r.pool.QueryRow(ctx,
		`SELECT next_run_at, last_run_at FROM scheduler.job_schedules WHERE job_id=$1`, jobID,
	).Scan(&s.NextRunAt, &s.LastRunAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.NextRunAt = s.NextRunAt.UTC()
	return s, nil
}

func (r *JobScheduleRepo) SetNextRun(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.job_schedules (job_id,next_run_at,is_locked) VALUES ($1,$2,false)
 ON CONFLICT (job_id) DO UPDATE SET next_run_at=EXCLUDED.next_run_at`,
		jobID, nextRunAt,
	)
	return err
}

func (r *JobScheduleRepo) Restart(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.job_schedules (job_id,next_run_at,is_locked) VALUES ($1,$2,false)
 ON CONFLICT (job_id) DO UPDATE SET next_run_at=EXCLUDED.next_run_at, last_run_at=NULL`,
		jobID, nextRunAt,
	)
	return err
}

// ClaimDue toma la fila del calendario con FOR UPDATE SKIP LOCKED y vuelve a comprobar que
// sigue vencida: la replica que llega despues de otra la ve ya reprogramada y la salta.
// is_locked/locked_by/locked_at ya no se escriben; el cerrojo es el de la fila.
func (r *JobScheduleRepo) ClaimDue(ctx context.Context, jobID uuid.UUID, now time.Time) (time.Time, bool, error) {
	var scheduled time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT next_run_at FROM scheduler.job_schedules WHERE job_id=$1 AND next_run_at <= $2 FOR UPDATE SKIP LOCKED`,
		jobID, now,
	).Scan(&scheduled)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return scheduled.UTC(), true, nil
}

func (r *JobScheduleRepo) LockActiveCron(ctx context.Context) ([]*domain.CronJobSchedule, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT js.job_id, jd.tenant_id, COALESCE(jd.cron_expression, ''), jd.timezone, js.next_run_at
 FROM scheduler.job_schedules js
 JOIN scheduler.job_definitions jd ON jd.id = js.job_id
 WHERE jd.job_type = $1 AND jd.is_active
 ORDER BY js.job_id
 FOR UPDATE OF js SKIP LOCKED`, domain.JobTypeCron,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*domain.CronJobSchedule
	for rows.Next() {
		s := &domain.CronJobSchedule{}
		if err := rows.Scan(&s.JobID, &s.TenantID, &s.Expression, &s.Timezone, &s.NextRunAt); err != nil {
			return nil, err
		}
		s.NextRunAt = s.NextRunAt.UTC()
		list = append(list, s)
	}
	return list, rows.Err()
}

func (r *JobScheduleRepo) GetDue(ctx context.Context, now time.Time) ([]*domain.JobSchedule, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT js.job_id,jd.tenant_id,js.next_run_at,js.last_run_at,js.is_locked,js.locked_by,js.locked_at
 FROM scheduler.job_schedules js
 JOIN scheduler.job_definitions jd ON jd.id=js.job_id
 WHERE js.next_run_at <= $1 AND jd.is_active ORDER BY js.next_run_at`, now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*domain.JobSchedule
	for rows.Next() {
		s := &domain.JobSchedule{}
		if err := rows.Scan(&s.JobID, &s.TenantID, &s.NextRunAt, &s.LastRunAt, &s.IsLocked, &s.LockedBy, &s.LockedAt); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}
