package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

type JobDefinitionRepo struct {
	pool *db.ContextPool
}

func NewJobDefinitionRepo(pool *db.ContextPool) *JobDefinitionRepo {
	return &JobDefinitionRepo{pool: pool}
}

func (r *JobDefinitionRepo) Create(ctx context.Context, job *domain.JobDefinition) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.job_definitions (id,tenant_id,name,code,description,job_type,cron_expression,interval_minutes,handler,payload,is_active,max_retries,timeout_seconds,created_at,updated_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		job.ID, job.TenantID, job.Name, job.Code, job.Description, job.JobType, job.CronExpression,
		job.IntervalMinutes, job.Handler, job.Payload, job.IsActive, job.MaxRetries, job.TimeoutSeconds,
		job.CreatedAt, job.UpdatedAt,
	)
	return err
}

func (r *JobDefinitionRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	j := &domain.JobDefinition{}
	err := r.pool.QueryRow(ctx,
		`SELECT id,tenant_id,name,code,description,job_type,cron_expression,interval_minutes,handler,payload,is_active,max_retries,timeout_seconds,created_at,updated_at
 FROM scheduler.job_definitions WHERE id=$1 AND (tenant_id=$2 OR tenant_id IS NULL)`, id, tenantID,
	).Scan(&j.ID, &j.TenantID, &j.Name, &j.Code, &j.Description, &j.JobType, &j.CronExpression,
		&j.IntervalMinutes, &j.Handler, &j.Payload, &j.IsActive, &j.MaxRetries, &j.TimeoutSeconds,
		&j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return nil, domain.ErrJobNotFound
	}
	return j, nil
}

func (r *JobDefinitionRepo) GetByCode(ctx context.Context, code string) (*domain.JobDefinition, error) {
	j := &domain.JobDefinition{}
	err := r.pool.QueryRow(ctx,
		`SELECT id,tenant_id,name,code,description,job_type,cron_expression,interval_minutes,handler,payload,is_active,max_retries,timeout_seconds,created_at,updated_at
 FROM scheduler.job_definitions WHERE code=$1`, code,
	).Scan(&j.ID, &j.TenantID, &j.Name, &j.Code, &j.Description, &j.JobType, &j.CronExpression,
		&j.IntervalMinutes, &j.Handler, &j.Payload, &j.IsActive, &j.MaxRetries, &j.TimeoutSeconds,
		&j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return nil, domain.ErrJobNotFound
	}
	return j, nil
}

func (r *JobDefinitionRepo) List(ctx context.Context, tenantID *uuid.UUID, isActive *bool) ([]*domain.JobDefinition, error) {
	q := `SELECT id,tenant_id,name,code,description,job_type,cron_expression,interval_minutes,handler,payload,is_active,max_retries,timeout_seconds,created_at,updated_at
      FROM scheduler.job_definitions WHERE 1=1`
	args := []interface{}{}
	n := 0
	if tenantID != nil {
		n++
		q += ` AND tenant_id=$` + strconv.Itoa(n)
		args = append(args, *tenantID)
	}
	if isActive != nil {
		n++
		q += ` AND is_active=$` + strconv.Itoa(n)
		args = append(args, *isActive)
	}
	q += ` ORDER BY name`

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*domain.JobDefinition
	for rows.Next() {
		j := &domain.JobDefinition{}
		if err := rows.Scan(&j.ID, &j.TenantID, &j.Name, &j.Code, &j.Description, &j.JobType, &j.CronExpression,
			&j.IntervalMinutes, &j.Handler, &j.Payload, &j.IsActive, &j.MaxRetries, &j.TimeoutSeconds,
			&j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, nil
}

func (r *JobDefinitionRepo) Update(ctx context.Context, job *domain.JobDefinition) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduler.job_definitions SET name=$1,description=$2,job_type=$3,cron_expression=$4,interval_minutes=$5,handler=$6,payload=$7,is_active=$8,max_retries=$9,timeout_seconds=$10,updated_at=$11 WHERE id=$12`,
		job.Name, job.Description, job.JobType, job.CronExpression, job.IntervalMinutes,
		job.Handler, job.Payload, job.IsActive, job.MaxRetries, job.TimeoutSeconds, job.UpdatedAt, job.ID,
	)
	return err
}

func (r *JobDefinitionRepo) Deactivate(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE scheduler.job_definitions SET is_active=false, updated_at=$1 WHERE id=$2`, time.Now().UTC(), id)
	return err
}

type JobExecutionRepo struct {
	pool *db.ContextPool
}

func NewJobExecutionRepo(pool *db.ContextPool) *JobExecutionRepo {
	return &JobExecutionRepo{pool: pool}
}

func (r *JobExecutionRepo) Create(ctx context.Context, exec *domain.JobExecution) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.job_executions (id,job_id,tenant_id,status,started_at,completed_at,duration,result,error_message,retry_count,created_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		exec.ID, exec.JobID, exec.TenantID, exec.Status, exec.StartedAt, exec.CompletedAt,
		exec.Duration, exec.Result, exec.ErrorMessage, exec.RetryCount, exec.CreatedAt,
	)
	return err
}

func (r *JobExecutionRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	e := &domain.JobExecution{}
	err := r.pool.QueryRow(ctx,
		`SELECT id,job_id,tenant_id,status,started_at,completed_at,duration,result,error_message,retry_count,created_at
 FROM scheduler.job_executions WHERE id=$1 AND (tenant_id=$2 OR tenant_id IS NULL)`, id, tenantID,
	).Scan(&e.ID, &e.JobID, &e.TenantID, &e.Status, &e.StartedAt, &e.CompletedAt,
		&e.Duration, &e.Result, &e.ErrorMessage, &e.RetryCount, &e.CreatedAt)
	if err != nil {
		return nil, domain.ErrExecutionNotFound
	}
	return e, nil
}

func (r *JobExecutionRepo) GetByJob(ctx context.Context, jobID uuid.UUID, page, pageSize int) ([]*domain.JobExecution, int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM scheduler.job_executions WHERE job_id=$1`, jobID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * pageSize
	rows, err := r.pool.Query(ctx,
		`SELECT id,job_id,tenant_id,status,started_at,completed_at,duration,result,error_message,retry_count,created_at
 FROM scheduler.job_executions WHERE job_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, jobID, pageSize, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []*domain.JobExecution
	for rows.Next() {
		e := &domain.JobExecution{}
		if err := rows.Scan(&e.ID, &e.JobID, &e.TenantID, &e.Status, &e.StartedAt, &e.CompletedAt,
			&e.Duration, &e.Result, &e.ErrorMessage, &e.RetryCount, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		list = append(list, e)
	}
	return list, total, nil
}

func (r *JobExecutionRepo) Update(ctx context.Context, exec *domain.JobExecution) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduler.job_executions SET status=$1,started_at=$2,completed_at=$3,duration=$4,result=$5,error_message=$6,retry_count=$7 WHERE id=$8`,
		exec.Status, exec.StartedAt, exec.CompletedAt, exec.Duration, exec.Result, exec.ErrorMessage, exec.RetryCount, exec.ID,
	)
	return err
}

func (r *JobExecutionRepo) GetLastByJob(ctx context.Context, jobID uuid.UUID) (*domain.JobExecution, error) {
	e := &domain.JobExecution{}
	err := r.pool.QueryRow(ctx,
		`SELECT id,job_id,tenant_id,status,started_at,completed_at,duration,result,error_message,retry_count,created_at
 FROM scheduler.job_executions WHERE job_id=$1 ORDER BY created_at DESC LIMIT 1`, jobID,
	).Scan(&e.ID, &e.JobID, &e.TenantID, &e.Status, &e.StartedAt, &e.CompletedAt,
		&e.Duration, &e.Result, &e.ErrorMessage, &e.RetryCount, &e.CreatedAt)
	if err != nil {
		return nil, domain.ErrExecutionNotFound
	}
	return e, nil
}

func (r *JobExecutionRepo) ListRunning(ctx context.Context) ([]*domain.JobExecution, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id,job_id,tenant_id,status,started_at,completed_at,duration,result,error_message,retry_count,created_at
 FROM scheduler.job_executions WHERE status IN ('pending','running') ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*domain.JobExecution
	for rows.Next() {
		e := &domain.JobExecution{}
		if err := rows.Scan(&e.ID, &e.JobID, &e.TenantID, &e.Status, &e.StartedAt, &e.CompletedAt,
			&e.Duration, &e.Result, &e.ErrorMessage, &e.RetryCount, &e.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, nil
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

func (r *ScheduledTaskRepo) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	t := &domain.ScheduledTask{}
	err := r.pool.QueryRow(ctx,
		`SELECT id,tenant_id,name,description,trigger_at,handler,payload,status,executed_at,created_at
 FROM scheduler.scheduled_tasks WHERE id=$1 AND tenant_id=$2`, id, tenantID,
	).Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.TriggerAt, &t.Handler, &t.Payload, &t.Status, &t.ExecutedAt, &t.CreatedAt)
	if err != nil {
		return nil, domain.ErrTaskNotFound
	}
	return t, nil
}

func (r *ScheduledTaskRepo) ListPending(ctx context.Context, before time.Time) ([]*domain.ScheduledTask, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id,tenant_id,name,description,trigger_at,handler,payload,status,executed_at,created_at
 FROM scheduler.scheduled_tasks WHERE status='scheduled' AND trigger_at <= $1 ORDER BY trigger_at`, before,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*domain.ScheduledTask
	for rows.Next() {
		t := &domain.ScheduledTask{}
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.TriggerAt, &t.Handler, &t.Payload, &t.Status, &t.ExecutedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, nil
}

// UpdateStatus fija executed_at al pasar a 'executed': es la unica escritura de
// esa columna, y sin ella la fecha de ejecucion quedaba solo en memoria.
func (r *ScheduledTaskRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduler.scheduled_tasks
		    SET status=$1,
		        executed_at = CASE WHEN $1 = 'executed' THEN NOW() ELSE executed_at END
		  WHERE id=$2`, status, id)
	return err
}

func (r *ScheduledTaskRepo) Cancel(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE scheduler.scheduled_tasks SET status='cancelled' WHERE id=$1 AND status='scheduled'`, id)
	return err
}

type JobScheduleRepo struct {
	pool *db.ContextPool
}

func NewJobScheduleRepo(pool *db.ContextPool) *JobScheduleRepo {
	return &JobScheduleRepo{pool: pool}
}

func (r *JobScheduleRepo) GetByJob(ctx context.Context, jobID uuid.UUID) (*domain.JobSchedule, error) {
	s := &domain.JobSchedule{}
	err := r.pool.QueryRow(ctx,
		`SELECT job_id,next_run_at,last_run_at,is_locked,locked_by,locked_at
 FROM scheduler.job_schedules WHERE job_id=$1`, jobID,
	).Scan(&s.JobID, &s.NextRunAt, &s.LastRunAt, &s.IsLocked, &s.LockedBy, &s.LockedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (r *JobScheduleRepo) UpdateNextRun(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO scheduler.job_schedules (job_id,next_run_at,is_locked) VALUES ($1,$2,false)
 ON CONFLICT (job_id) DO UPDATE SET next_run_at=$2, last_run_at=NOW()`,
		jobID, nextRunAt,
	)
	return err
}

func (r *JobScheduleRepo) Lock(ctx context.Context, jobID uuid.UUID, lockerID string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE scheduler.job_schedules SET is_locked=true, locked_by=$1, locked_at=NOW() WHERE job_id=$2 AND is_locked=false`,
		lockerID, jobID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *JobScheduleRepo) Unlock(ctx context.Context, jobID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduler.job_schedules SET is_locked=false, locked_by=NULL, locked_at=NULL WHERE job_id=$1`, jobID,
	)
	return err
}

func (r *JobScheduleRepo) GetDue(ctx context.Context, now time.Time) ([]*domain.JobSchedule, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT js.job_id,jd.tenant_id,js.next_run_at,js.last_run_at,js.is_locked,js.locked_by,js.locked_at
 FROM scheduler.job_schedules js
 JOIN scheduler.job_definitions jd ON jd.id=js.job_id
 WHERE js.next_run_at <= $1 AND js.is_locked=false ORDER BY js.next_run_at`, now,
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
	return list, nil
}
