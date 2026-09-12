package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type SchedulerUseCase struct {
	jobs       ports.JobDefinitionRepository
	executions ports.JobExecutionRepository
	tasks      ports.ScheduledTaskRepository
	schedules  ports.JobScheduleRepository
	events     ports.EventPublisher
	logger     *zap.Logger
}

type SchedulerDeps struct {
	Jobs       ports.JobDefinitionRepository
	Executions ports.JobExecutionRepository
	Tasks      ports.ScheduledTaskRepository
	Schedules  ports.JobScheduleRepository
	Events     ports.EventPublisher
	Logger     *zap.Logger
}

func NewSchedulerUseCase(deps SchedulerDeps) *SchedulerUseCase {
	return &SchedulerUseCase{
		jobs:       deps.Jobs,
		executions: deps.Executions,
		tasks:      deps.Tasks,
		schedules:  deps.Schedules,
		events:     deps.Events,
		logger:     deps.Logger,
	}
}

func (uc *SchedulerUseCase) CreateJob(ctx context.Context, job *domain.JobDefinition) error {
	existing, _ := uc.jobs.GetByCode(ctx, job.Code)
	if existing != nil {
		return domain.ErrJobAlreadyExists
	}
	job.ID = uuid.New()
	job.IsActive = true
	job.CreatedAt = time.Now().UTC()
	job.UpdatedAt = job.CreatedAt
	if err := uc.jobs.Create(ctx, job); err != nil {
		return err
	}
	schedule := &domain.JobSchedule{
		JobID:     job.ID,
		NextRunAt: uc.calculateNextRun(job),
	}
	return uc.schedules.UpdateNextRun(ctx, schedule.JobID, schedule.NextRunAt)
}

func (uc *SchedulerUseCase) GetJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	return uc.jobs.GetByID(ctx, id, tenantID)
}

func (uc *SchedulerUseCase) ListJobs(ctx context.Context, tenantID *uuid.UUID, isActive *bool) ([]*domain.JobDefinition, error) {
	return uc.jobs.List(ctx, tenantID, isActive)
}

func (uc *SchedulerUseCase) UpdateJob(ctx context.Context, job *domain.JobDefinition) error {
	job.UpdatedAt = time.Now().UTC()
	return uc.jobs.Update(ctx, job)
}

func (uc *SchedulerUseCase) EnableJob(ctx context.Context, id, tenantID uuid.UUID) error {
	job, err := uc.jobs.GetByID(ctx, id, tenantID)
	if err != nil {
		return domain.ErrJobNotFound
	}
	job.IsActive = true
	job.UpdatedAt = time.Now().UTC()
	return uc.jobs.Update(ctx, job)
}

func (uc *SchedulerUseCase) DisableJob(ctx context.Context, id uuid.UUID) error {
	return uc.jobs.Deactivate(ctx, id)
}

func (uc *SchedulerUseCase) RunJob(ctx context.Context, tenantID, jobID uuid.UUID) (*domain.JobExecution, error) {
	job, err := uc.jobs.GetByID(ctx, jobID, tenantID)
	if err != nil {
		return nil, domain.ErrJobNotFound
	}
	exec := &domain.JobExecution{
		ID:        uuid.New(),
		JobID:     job.ID,
		TenantID:  job.TenantID,
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
	}
	if err := uc.executions.Create(ctx, exec); err != nil {
		return nil, err
	}
	tid := ""
	if job.TenantID != nil {
		tid = job.TenantID.String()
	}
	_ = uc.events.PublishJobStarted(tid, job.ID.String(), exec.ID.String())
	return exec, nil
}

func (uc *SchedulerUseCase) CancelExecution(ctx context.Context, id, tenantID uuid.UUID) error {
	exec, err := uc.executions.GetByID(ctx, id, tenantID)
	if err != nil {
		return domain.ErrExecutionNotFound
	}
	exec.Status = "cancelled"
	now := time.Now().UTC()
	exec.CompletedAt = &now
	return uc.executions.Update(ctx, exec)
}

func (uc *SchedulerUseCase) ScheduleTask(ctx context.Context, task *domain.ScheduledTask) error {
	task.ID = uuid.New()
	task.Status = "scheduled"
	task.CreatedAt = time.Now().UTC()
	return uc.tasks.Create(ctx, task)
}

func (uc *SchedulerUseCase) CancelTask(ctx context.Context, id uuid.UUID) error {
	return uc.tasks.Cancel(ctx, id)
}

func (uc *SchedulerUseCase) GetTask(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	return uc.tasks.GetByID(ctx, id, tenantID)
}

func (uc *SchedulerUseCase) GetJobHistory(ctx context.Context, jobID uuid.UUID, page, pageSize int) ([]*domain.JobExecution, int64, error) {
	return uc.executions.GetByJob(ctx, jobID, page, pageSize)
}

func (uc *SchedulerUseCase) GetRunningJobs(ctx context.Context) ([]*domain.JobExecution, error) {
	return uc.executions.ListRunning(ctx)
}

func (uc *SchedulerUseCase) GetExecution(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	return uc.executions.GetByID(ctx, id, tenantID)
}

func (uc *SchedulerUseCase) RetryFailedExecution(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	exec, err := uc.executions.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, domain.ErrExecutionNotFound
	}
	var execTenantID uuid.UUID
	if exec.TenantID != nil {
		execTenantID = *exec.TenantID
	}
	job, err := uc.jobs.GetByID(ctx, exec.JobID, execTenantID)
	if err != nil {
		return nil, domain.ErrJobNotFound
	}
	if exec.RetryCount >= job.MaxRetries {
		return nil, domain.ErrMaxRetriesExceeded
	}
	newExec := &domain.JobExecution{
		ID:         uuid.New(),
		JobID:      exec.JobID,
		TenantID:   exec.TenantID,
		Status:     "pending",
		RetryCount: exec.RetryCount + 1,
		CreatedAt:  time.Now().UTC(),
	}
	if err := uc.executions.Create(ctx, newExec); err != nil {
		return nil, err
	}
	tid := ""
	if job.TenantID != nil {
		tid = job.TenantID.String()
	}
	_ = uc.events.PublishJobStarted(tid, job.ID.String(), newExec.ID.String())
	return newExec, nil
}

func (uc *SchedulerUseCase) ProcessDueJobs(ctx context.Context, lockerID string) {
	now := time.Now().UTC()
	due, err := uc.schedules.GetDue(ctx, now)
	if err != nil {
		uc.logger.Error("get due jobs", zap.Error(err))
		return
	}
	for _, s := range due {
		locked, err := uc.schedules.Lock(ctx, s.JobID, lockerID)
		if err != nil || !locked {
			continue
		}
		var jobTenantID uuid.UUID
		if s.TenantID != nil {
			jobTenantID = *s.TenantID
		}
		job, err := uc.jobs.GetByID(ctx, s.JobID, jobTenantID)
		if err != nil || !job.IsActive {
			_ = uc.schedules.Unlock(ctx, s.JobID)
			continue
		}
		exec := &domain.JobExecution{
			ID:        uuid.New(),
			JobID:     job.ID,
			TenantID:  job.TenantID,
			Status:    "running",
			CreatedAt: now,
		}
		startedAt := now
		exec.StartedAt = &startedAt
		if err := uc.executions.Create(ctx, exec); err != nil {
			_ = uc.schedules.Unlock(ctx, s.JobID)
			continue
		}
		tid := ""
		if job.TenantID != nil {
			tid = job.TenantID.String()
		}
		_ = uc.events.PublishJobStarted(tid, job.ID.String(), exec.ID.String())
		nextRun := uc.calculateNextRun(job)
		_ = uc.schedules.UpdateNextRun(ctx, s.JobID, nextRun)
		_ = uc.schedules.Unlock(ctx, s.JobID)
	}
}

func (uc *SchedulerUseCase) ProcessPendingTasks(ctx context.Context) {
	now := time.Now().UTC()
	tasks, err := uc.tasks.ListPending(ctx, now)
	if err != nil {
		uc.logger.Error("list pending tasks", zap.Error(err))
		return
	}
	for _, t := range tasks {
		_ = uc.tasks.UpdateStatus(ctx, t.ID, "executed")
		executedAt := now
		t.ExecutedAt = &executedAt
	}
}

func (uc *SchedulerUseCase) calculateNextRun(job *domain.JobDefinition) time.Time {
	now := time.Now().UTC()
	switch job.JobType {
	case "interval":
		if job.IntervalMinutes != nil {
			return now.Add(time.Duration(*job.IntervalMinutes) * time.Minute)
		}
	case "one_time":
		return now
	}
	return now.Add(time.Hour)
}

func (uc *SchedulerUseCase) ListPendingTasks(ctx context.Context, before time.Time) ([]*domain.ScheduledTask, error) {
	return uc.tasks.ListPending(ctx, before)
}
