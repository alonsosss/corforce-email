package app

import (
	"context"
	"errors"
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
	tx         ports.Transactor
	catalog    *domain.HandlerCatalog
	retry      domain.RetryPolicy
	now        func() time.Time
	logger     *zap.Logger
}

type SchedulerDeps struct {
	Jobs       ports.JobDefinitionRepository
	Executions ports.JobExecutionRepository
	Tasks      ports.ScheduledTaskRepository
	Schedules  ports.JobScheduleRepository
	Events     ports.EventPublisher
	Tx         ports.Transactor
	// Catalog es la lista blanca de manejadores; nil equivale a un catalogo vacio.
	Catalog *domain.HandlerCatalog
	Retry   domain.RetryPolicy
	// Now es el reloj; nil usa la hora del sistema en UTC.
	Now    func() time.Time
	Logger *zap.Logger
}

func NewSchedulerUseCase(deps SchedulerDeps) *SchedulerUseCase {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SchedulerUseCase{
		jobs:       deps.Jobs,
		executions: deps.Executions,
		tasks:      deps.Tasks,
		schedules:  deps.Schedules,
		events:     deps.Events,
		tx:         deps.Tx,
		catalog:    deps.Catalog,
		retry:      deps.Retry,
		now:        now,
		logger:     deps.Logger,
	}
}

// ListHandlers devuelve el catalogo de manejadores a los que puede apuntar un trabajo.
func (uc *SchedulerUseCase) ListHandlers() []domain.HandlerSpec {
	return uc.catalog.List()
}

// checkDefinition valida la definicion y que su manejador este permitido para su tipo.
func (uc *SchedulerUseCase) checkDefinition(job *domain.JobDefinition) error {
	if err := job.Validate(); err != nil {
		return err
	}
	_, err := uc.catalog.Resolve(job.Handler, job.IsPlatform())
	return err
}

func (uc *SchedulerUseCase) CreateJob(ctx context.Context, job *domain.JobDefinition) error {
	if err := uc.checkDefinition(job); err != nil {
		return err
	}
	existing, err := uc.jobs.GetByCode(ctx, job.Code)
	if existing != nil {
		return domain.ErrJobAlreadyExists
	}
	if err != nil && !errors.Is(err, domain.ErrJobNotFound) {
		return err
	}
	job.ID = uuid.New()
	job.IsActive = true
	job.CreatedAt = uc.now()
	job.UpdatedAt = job.CreatedAt
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.jobs.Create(ctx, job); err != nil {
			return err
		}
		return uc.schedules.UpdateNextRun(ctx, job.ID, uc.calculateNextRun(job))
	})
}

func (uc *SchedulerUseCase) GetJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	return uc.jobs.GetByID(ctx, id, tenantID)
}

func (uc *SchedulerUseCase) ListJobs(ctx context.Context, tenantID *uuid.UUID, isActive *bool) ([]*domain.JobDefinition, error) {
	return uc.jobs.List(ctx, tenantID, isActive)
}

// UpdateJob recibe el trabajo leido con GetJob y ya modificado; su empresa no cambia.
func (uc *SchedulerUseCase) UpdateJob(ctx context.Context, job *domain.JobDefinition) error {
	if job.IsPlatform() {
		return domain.ErrPlatformJob
	}
	if err := uc.checkDefinition(job); err != nil {
		return err
	}
	job.UpdatedAt = uc.now()
	return uc.jobs.Update(ctx, job)
}

// tenantJob carga un trabajo que la empresa puede cambiar: el suyo, nunca uno de plataforma.
func (uc *SchedulerUseCase) tenantJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	job, err := uc.jobs.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if job.IsPlatform() {
		return nil, domain.ErrPlatformJob
	}
	return job, nil
}

func (uc *SchedulerUseCase) EnableJob(ctx context.Context, id, tenantID uuid.UUID) error {
	job, err := uc.tenantJob(ctx, id, tenantID)
	if err != nil {
		return err
	}
	job.IsActive = true
	job.UpdatedAt = uc.now()
	return uc.jobs.Update(ctx, job)
}

func (uc *SchedulerUseCase) DisableJob(ctx context.Context, id, tenantID uuid.UUID) error {
	if _, err := uc.tenantJob(ctx, id, tenantID); err != nil {
		return err
	}
	return uc.jobs.Deactivate(ctx, id)
}

// RunJob lanza a mano un trabajo de la empresa: la ejecucion nace despachada.
func (uc *SchedulerUseCase) RunJob(ctx context.Context, tenantID, jobID uuid.UUID) (*domain.JobExecution, error) {
	job, err := uc.tenantJob(ctx, jobID, tenantID)
	if err != nil {
		return nil, err
	}
	spec, err := uc.catalog.Resolve(job.Handler, job.IsPlatform())
	if err != nil {
		return nil, err
	}
	now := uc.now()
	exec := newExecution(job, now)
	if err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		return uc.dispatch(ctx, job, spec, exec, now, true)
	}); err != nil {
		return nil, err
	}
	return exec, nil
}

func (uc *SchedulerUseCase) ScheduleTask(ctx context.Context, task *domain.ScheduledTask) error {
	task.ID = uuid.New()
	task.Status = "scheduled"
	task.CreatedAt = uc.now()
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

// ProcessDueJobs despacha los trabajos vencidos de la base del contexto. Cada trabajo va en
// su propia transaccion, que bloquea su calendario (ClaimDue): dos replicas no lanzan el
// mismo trabajo, y si el proceso cae a medias el bloqueo se suelta con el rollback.
func (uc *SchedulerUseCase) ProcessDueJobs(ctx context.Context) {
	due, err := uc.schedules.GetDue(ctx, uc.now())
	if err != nil {
		uc.logger.Error("get due jobs", zap.Error(err))
		return
	}
	for _, s := range due {
		if ctx.Err() != nil {
			return
		}
		if err := uc.runDue(ctx, s); err != nil {
			uc.logger.Error("scheduler: no se lanzo el trabajo vencido", zap.String("job_id", s.JobID.String()), zap.Error(err))
		}
	}
}

func (uc *SchedulerUseCase) runDue(ctx context.Context, s *domain.JobSchedule) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		now := uc.now()
		claimed, err := uc.schedules.ClaimDue(ctx, s.JobID, now)
		if err != nil || !claimed {
			return err
		}
		job, err := uc.jobs.GetByID(ctx, s.JobID, tenantOrNil(s.TenantID))
		if err != nil {
			return err
		}
		if !job.IsActive {
			return nil
		}
		exec := newExecution(job, now)
		if spec, rerr := uc.catalog.Resolve(job.Handler, job.IsPlatform()); rerr != nil {
			// El manejador salio del catalogo despues de crearse el trabajo: la ejecucion
			// queda fallida en el historial en vez de despacharse sin nadie que la cierre.
			if err := uc.failUndispatched(ctx, job, exec, now, rerr, true); err != nil {
				return err
			}
		} else if err := uc.dispatch(ctx, job, spec, exec, now, true); err != nil {
			return err
		}
		if err := uc.schedules.UpdateNextRun(ctx, job.ID, uc.calculateNextRun(job)); err != nil {
			return err
		}
		if job.JobType == domain.JobTypeOneTime {
			return uc.jobs.Deactivate(ctx, job.ID)
		}
		return nil
	})
}

func (uc *SchedulerUseCase) ProcessPendingTasks(ctx context.Context) {
	now := uc.now()
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
	now := uc.now()
	switch job.JobType {
	case domain.JobTypeInterval:
		if job.IntervalMinutes != nil {
			return now.Add(time.Duration(*job.IntervalMinutes) * time.Minute)
		}
	case domain.JobTypeOneTime:
		return now
	}
	return now.Add(time.Hour)
}

func (uc *SchedulerUseCase) ListPendingTasks(ctx context.Context, before time.Time) ([]*domain.ScheduledTask, error) {
	return uc.tasks.ListPending(ctx, before)
}

func newExecution(job *domain.JobDefinition, now time.Time) *domain.JobExecution {
	return &domain.JobExecution{
		ID:        uuid.New(),
		JobID:     job.ID,
		TenantID:  job.TenantID,
		Status:    domain.StatusPending,
		CreatedAt: now,
	}
}

// tenantOrNil es la empresa con la que se busca un trabajo: la suya, o ninguna si es de
// plataforma (las consultas por empresa aceptan tambien tenant_id NULL).
func tenantOrNil(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}
