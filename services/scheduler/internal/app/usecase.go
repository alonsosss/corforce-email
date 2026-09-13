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

// checkDefinition valida la definicion, que su manejador este permitido para su tipo y que
// su plazo quepa en el del manejador.
func (uc *SchedulerUseCase) checkDefinition(job *domain.JobDefinition) error {
	if err := job.Validate(); err != nil {
		return err
	}
	spec, err := uc.catalog.Resolve(job.Handler, job.IsPlatform())
	if err != nil {
		return err
	}
	return job.CheckTimeoutFor(spec)
}

// CreateJob guarda y planifica el trabajo, y lo devuelve leido en la misma transaccion.
func (uc *SchedulerUseCase) CreateJob(ctx context.Context, job *domain.JobDefinition) (*domain.JobOverview, error) {
	if err := uc.checkDefinition(job); err != nil {
		return nil, err
	}
	existing, err := uc.jobs.GetByCode(ctx, job.Code)
	if existing != nil {
		return nil, domain.ErrJobAlreadyExists
	}
	if err != nil && !errors.Is(err, domain.ErrJobNotFound) {
		return nil, err
	}
	job.ID = uuid.New()
	job.IsActive = true
	job.CreatedAt = uc.now()
	job.UpdatedAt = job.CreatedAt
	next, err := uc.nextRun(job, nil, job.CreatedAt)
	if err != nil {
		return nil, err
	}
	var out *domain.JobOverview
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.jobs.Create(ctx, job); err != nil {
			return err
		}
		if err := uc.schedules.SetNextRun(ctx, job.ID, next); err != nil {
			return err
		}
		overview, err := uc.jobs.GetOverview(ctx, job.ID, tenantOrNil(job.TenantID))
		out = overview
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (uc *SchedulerUseCase) GetJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	return uc.jobs.GetByID(ctx, id, tenantID)
}

// GetJobOverview lee el trabajo con su calendario y su ultima ejecucion.
func (uc *SchedulerUseCase) GetJobOverview(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobOverview, error) {
	return uc.jobs.GetOverview(ctx, id, tenantID)
}

// ListJobs devuelve una pagina de los trabajos que ve la empresa y el total del filtro.
func (uc *SchedulerUseCase) ListJobs(ctx context.Context, filter domain.JobFilter) ([]*domain.JobOverview, int64, error) {
	return uc.jobs.List(ctx, filter)
}

// UpdateJob recibe el trabajo leido con GetJob y ya modificado; su empresa no cambia. Si
// cambia el calendario de un cron (pasa a cron, cambia su expresion o su zona) se
// replanifica en la misma transaccion; una edicion que no lo toca respeta la ejecucion ya
// prevista. Devuelve el trabajo leido en esa transaccion.
func (uc *SchedulerUseCase) UpdateJob(ctx context.Context, job *domain.JobDefinition) (*domain.JobOverview, error) {
	if job.IsPlatform() {
		return nil, domain.ErrPlatformJob
	}
	if err := uc.checkDefinition(job); err != nil {
		return nil, err
	}
	job.UpdatedAt = uc.now()
	var out *domain.JobOverview
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		stored, err := uc.jobs.GetByID(ctx, job.ID, *job.TenantID)
		if err != nil {
			return err
		}
		if err := uc.jobs.Update(ctx, job); err != nil {
			return err
		}
		if job.JobType == domain.JobTypeCron && (stored.JobType != job.JobType || stored.CronExpr() != job.CronExpr() || stored.Timezone != job.Timezone) {
			next, err := uc.nextRun(job, nil, job.UpdatedAt)
			if err != nil {
				return err
			}
			if err := uc.schedules.SetNextRun(ctx, job.ID, next); err != nil {
				return err
			}
		}
		overview, err := uc.jobs.GetOverview(ctx, job.ID, *job.TenantID)
		out = overview
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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

// EnableJob reactiva un trabajo. Un cron que estaba desactivado se replanifica desde ahora:
// las ocurrencias de mientras estuvo parado no se lanzan al reactivarlo.
func (uc *SchedulerUseCase) EnableJob(ctx context.Context, id, tenantID uuid.UUID) error {
	job, err := uc.tenantJob(ctx, id, tenantID)
	if err != nil {
		return err
	}
	wasActive := job.IsActive
	job.IsActive = true
	job.UpdatedAt = uc.now()
	if wasActive || job.JobType != domain.JobTypeCron {
		return uc.jobs.Update(ctx, job)
	}
	next, err := uc.nextRun(job, nil, job.UpdatedAt)
	if err != nil {
		return err
	}
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.jobs.Update(ctx, job); err != nil {
			return err
		}
		return uc.schedules.SetNextRun(ctx, job.ID, next)
	})
}

func (uc *SchedulerUseCase) DisableJob(ctx context.Context, id, tenantID uuid.UUID) error {
	if _, err := uc.tenantJob(ctx, id, tenantID); err != nil {
		return err
	}
	return uc.jobs.Deactivate(ctx, id, uc.now())
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
	if err := task.Validate(); err != nil {
		return err
	}
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
		scheduled, claimed, err := uc.schedules.ClaimDue(ctx, s.JobID, now)
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
		next, err := uc.nextRun(job, &scheduled, now)
		if errors.Is(err, domain.ErrInvalidCron) || errors.Is(err, domain.ErrInvalidTimezone) {
			return uc.deactivateUnschedulable(ctx, job.ID, now, err)
		}
		if err != nil {
			return err
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
		if err := uc.schedules.UpdateNextRun(ctx, job.ID, next, now); err != nil {
			return err
		}
		if job.JobType == domain.JobTypeOneTime {
			return uc.jobs.Deactivate(ctx, job.ID, now)
		}
		return nil
	})
}

// deactivateUnschedulable desactiva un cron cuya expresion o zona guardadas no se pueden
// evaluar (escritas antes de que se validaran, o una zona que la base de zonas ya no
// carga): lanzarlo seria hacerlo a deshora. Reactivarlo exige corregirlas antes. Que falte
// la base de zonas entera no llega aqui: el proceso no arranca sin ella.
func (uc *SchedulerUseCase) deactivateUnschedulable(ctx context.Context, jobID uuid.UUID, now time.Time, cause error) error {
	uc.logger.Error("scheduler: trabajo cron con expresion o zona invalida; se desactiva",
		zap.String("job_id", jobID.String()), zap.Error(cause))
	return uc.jobs.Deactivate(ctx, jobID, now)
}

// ReconcileCronSchedules corrige en la base del contexto los calendarios cron que no salen
// de su expresion, como los escritos cuando el scheduler no la evaluaba (cada hora desde la
// ultima pasada), y desactiva los cron con una expresion invalida. Devuelve cuantos cambio.
// Es idempotente. Salta los calendarios que otra transaccion tiene bloqueados: son
// despachos en curso, que ya dejan la siguiente ejecucion bien calculada.
func (uc *SchedulerUseCase) ReconcileCronSchedules(ctx context.Context) (int, error) {
	fixed := 0
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		fixed = 0
		schedules, err := uc.schedules.LockActiveCron(ctx)
		if err != nil {
			return err
		}
		now := uc.now()
		for _, s := range schedules {
			spec, err := domain.ParseCron(s.Expression, s.Timezone)
			if err != nil {
				if err := uc.deactivateUnschedulable(ctx, s.JobID, now, err); err != nil {
					return err
				}
				fixed++
				continue
			}
			want, err := spec.Reconcile(s.NextRunAt, now)
			if err != nil {
				return err
			}
			if want.Equal(s.NextRunAt) {
				continue
			}
			if err := uc.schedules.SetNextRun(ctx, s.JobID, want); err != nil {
				return err
			}
			fixed++
		}
		return nil
	})
	return fixed, err
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

// nextRun es la proxima ejecucion del trabajo vista en now. scheduled es la ejecucion
// prevista que se acaba de despachar, o nil al crear, editar o reactivar el trabajo; un
// cron cuenta desde ella (ver domain.CronSpec.NextAfterDispatch).
func (uc *SchedulerUseCase) nextRun(job *domain.JobDefinition, scheduled *time.Time, now time.Time) (time.Time, error) {
	switch job.JobType {
	case domain.JobTypeCron:
		spec, err := domain.ParseCron(job.CronExpr(), job.Timezone)
		if err != nil {
			return time.Time{}, err
		}
		if scheduled == nil {
			return spec.Next(now)
		}
		return spec.NextAfterDispatch(*scheduled, now)
	case domain.JobTypeInterval:
		if job.IntervalMinutes != nil {
			return now.Add(time.Duration(*job.IntervalMinutes) * time.Minute), nil
		}
	case domain.JobTypeOneTime:
		return now, nil
	}
	return now.Add(time.Hour), nil
}

// ListPendingTasks lista las tareas puntuales pendientes que vencen dentro de
// domain.PendingTasksWindow, vistas desde el reloj del caso de uso.
func (uc *SchedulerUseCase) ListPendingTasks(ctx context.Context) ([]*domain.ScheduledTask, error) {
	return uc.tasks.ListPending(ctx, uc.now().Add(domain.PendingTasksWindow))
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
