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
	job.Version = domain.FirstJobVersion
	job.CreatedAt = uc.now()
	job.UpdatedAt = job.CreatedAt
	next, err := job.FirstRunAt(job.CreatedAt)
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

// UpdateJob recibe el trabajo leido con GetJob y ya modificado; su empresa no cambia, y su
// estado tampoco: esa lectura puede ser anterior al despacho que desactivo un one_time, y
// is_active solo lo cambian EnableJob, DisableJob y el calendario. Si la edicion cambia el
// calendario (domain.JobDefinition.ScheduleChanged: el tipo, la expresion
// o la zona de un cron, los minutos de un interval) se replanifica desde ahora con la
// definicion nueva (domain.JobDefinition.FirstRunAt) en la misma transaccion, sin anotar
// una ejecucion; si cambia el tipo, ademas se olvida la ultima pasada, como en un alta. Una
// edicion que no toca el calendario respeta la ejecucion ya prevista. Un trabajo
// inactivo guarda esa hora sin exponerla: al reactivarlo decide ResumeAt. Devuelve el
// trabajo leido en esa transaccion.
//
// job.Version es la version que leyo quien edita: si otra edicion se aplico despues, la
// suya la desharia (una zona, un calendario) y es domain.ErrJobVersionConflict sin escribir
// nada. Se compara con la version leida bajo el bloqueo, que es la que pisaria la escritura.
func (uc *SchedulerUseCase) UpdateJob(ctx context.Context, job *domain.JobDefinition) (*domain.JobOverview, error) {
	if job.IsPlatform() {
		return nil, domain.ErrPlatformJob
	}
	if job.Version < domain.FirstJobVersion {
		return nil, domain.NewFieldError(domain.FieldVersion, domain.RuleOutOfRange, domain.ErrInvalidJob,
			"version must be the version of the job that was read")
	}
	if err := uc.checkDefinition(job); err != nil {
		return nil, err
	}
	job.UpdatedAt = uc.now()
	var out *domain.JobOverview
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		stored, _, err := uc.lockTenantJob(ctx, job.ID, *job.TenantID)
		if err != nil {
			return err
		}
		if stored.Version != job.Version {
			return domain.ErrJobVersionConflict
		}
		if err := uc.jobs.Update(ctx, job); err != nil {
			return err
		}
		if job.ScheduleChanged(stored) {
			next, err := job.FirstRunAt(job.UpdatedAt)
			if err != nil {
				return err
			}
			// Otro tipo es otro calendario: su ultima pasada no cuenta, y en un one_time es la
			// que diria que ya se despacho.
			replan := uc.schedules.SetNextRun
			if stored.JobType != job.JobType {
				replan = uc.schedules.Restart
			}
			if err := replan(ctx, job.ID, next); err != nil {
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

// lockTenantJob bloquea, dentro de la transaccion del llamante, el calendario y despues el
// trabajo de la empresa, y los devuelve leidos bajo esos bloqueos (el calendario, nil si no
// tiene). Es el orden del despacho, que toma el calendario con ClaimDue y despues desactiva
// el one_time: una edicion, una reactivacion o una desactivacion espera al despacho en curso
// y lee lo que dejo, y el despacho salta el calendario mientras ellas duran. El orden inverso
// se interbloquea con el despacho. Un trabajo de plataforma se rechaza antes de bloquearlo:
// una empresa no aparta al despacho de un trabajo que no puede cambiar.
func (uc *SchedulerUseCase) lockTenantJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, *domain.JobSchedule, error) {
	if _, err := uc.tenantJob(ctx, id, tenantID); err != nil {
		return nil, nil, err
	}
	schedule, err := uc.schedules.GetForUpdate(ctx, id, tenantID)
	if err != nil {
		return nil, nil, err
	}
	job, err := uc.jobs.GetForUpdate(ctx, id, tenantID)
	if err != nil {
		return nil, nil, err
	}
	return job, schedule, nil
}

// EnableJob reactiva un trabajo de la empresa y lo replanifica desde ahora con la regla de
// domain.JobDefinition.ResumeAt: lo que no se lanzo mientras estuvo parado no se lanza al
// reactivarlo y next_run_at no queda en el pasado. Reactivar uno activo no toca su
// calendario.
func (uc *SchedulerUseCase) EnableJob(ctx context.Context, id, tenantID uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		job, schedule, err := uc.lockTenantJob(ctx, id, tenantID)
		if err != nil {
			return err
		}
		now := uc.now()
		if job.IsActive {
			return uc.jobs.Activate(ctx, job.ID, job.TenantID, now)
		}
		next, err := job.ResumeAt(schedule, now)
		if err != nil {
			return err
		}
		if err := uc.jobs.Activate(ctx, job.ID, job.TenantID, now); err != nil {
			return err
		}
		return uc.schedules.SetNextRun(ctx, job.ID, next)
	})
}

// DisableJob desactiva un trabajo de la empresa con los bloqueos de lockTenantJob, en el
// orden del despacho. Si llega antes que el despacho, este salta el calendario mientras dura
// y, confirmada, ya no ve el trabajo activo: la pasada vencida no sale. Si un despacho ya
// tiene el calendario, espera a que confirme y desactiva despues: la ejecucion que ese
// despacho reclamo sigue su curso, porque nacio con su evento de inicio en la misma
// transaccion y el ejecutor ya puede tenerla. Pararla es cancelarla (CancelExecution, con
// executions/cancel); desactivar corta las siguientes.
func (uc *SchedulerUseCase) DisableJob(ctx context.Context, id, tenantID uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		job, _, err := uc.lockTenantJob(ctx, id, tenantID)
		if err != nil {
			return err
		}
		return uc.jobs.Deactivate(ctx, job.ID, job.TenantID, uc.now())
	})
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
	task.Status = domain.TaskStatusScheduled
	task.CreatedAt = uc.now()
	return uc.tasks.Create(ctx, task)
}

// CancelTask cancela una tarea programada de la empresa. La de otra empresa responde igual
// que una que no existe (domain.ErrTaskNotFound) y no toca nada; cancelar dos veces no cambia
// nada; una ya ejecutada es domain.ErrTaskNotCancellable.
func (uc *SchedulerUseCase) CancelTask(ctx context.Context, id, tenantID uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		task, err := uc.tasks.GetForUpdate(ctx, id, tenantID)
		if err != nil {
			return err
		}
		changed, err := task.Cancel()
		if err != nil || !changed {
			return err
		}
		return uc.tasks.Cancel(ctx, task.ID, tenantID)
	})
}

func (uc *SchedulerUseCase) GetTask(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	return uc.tasks.GetByID(ctx, id, tenantID)
}

// GetJobHistory pagina las ejecuciones de un trabajo que ve la empresa; el de otra empresa,
// o uno que no existe, es domain.ErrJobNotFound.
func (uc *SchedulerUseCase) GetJobHistory(ctx context.Context, jobID, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error) {
	if _, err := uc.jobs.GetByID(ctx, jobID, tenantID); err != nil {
		return nil, 0, err
	}
	return uc.executions.GetByJob(ctx, jobID, tenantID, page, perPage)
}

// GetRunningJobs pagina las ejecuciones activas que ve la empresa (las suyas y las de
// plataforma) con el total.
func (uc *SchedulerUseCase) GetRunningJobs(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error) {
	return uc.executions.ListRunning(ctx, tenantID, page, perPage)
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
		next, err := job.NextAfterDispatch(scheduled, now)
		if unschedulable(err) {
			return uc.deactivateUnschedulable(ctx, job.ID, job.TenantID, now, err)
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
			return uc.jobs.Deactivate(ctx, job.ID, job.TenantID, now)
		}
		return nil
	})
}

// unschedulable indica que la definicion guardada no da una proxima ejecucion.
func unschedulable(err error) bool {
	var ferr *domain.FieldError
	return errors.As(err, &ferr) || errors.Is(err, domain.ErrInvalidCron) || errors.Is(err, domain.ErrInvalidTimezone)
}

// deactivateUnschedulable desactiva un trabajo cuya definicion guardada no se puede
// planificar: la expresion o la zona de un cron que no se evaluan, los minutos de un
// interval fuera de rango o un tipo desconocido, escritos antes de que se validaran, o una
// zona que la base de zonas ya no carga. Lanzarlo seria hacerlo a deshora, o en cada pasada
// con unos minutos a cero. Reactivarlo exige corregirla antes. Que falte la base de zonas
// entera no llega aqui: el proceso no arranca sin ella.
func (uc *SchedulerUseCase) deactivateUnschedulable(ctx context.Context, jobID uuid.UUID, owner *uuid.UUID, now time.Time, cause error) error {
	uc.logger.Error("scheduler: trabajo con una definicion que no se puede planificar; se desactiva",
		zap.String("job_id", jobID.String()), zap.Error(cause))
	return uc.jobs.Deactivate(ctx, jobID, owner, now)
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
				if err := uc.deactivateUnschedulable(ctx, s.JobID, s.TenantID, now, err); err != nil {
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

// ProcessPendingTasks marca ejecutadas las tareas vencidas de la base del contexto. Una que
// se cancelo entre la lectura y la escritura sigue cancelada.
func (uc *SchedulerUseCase) ProcessPendingTasks(ctx context.Context) {
	now := uc.now()
	tasks, err := uc.tasks.ListDue(ctx, now)
	if err != nil {
		uc.logger.Error("scheduler: no se listaron las tareas vencidas", zap.Error(err))
		return
	}
	for _, t := range tasks {
		if ctx.Err() != nil {
			return
		}
		if _, err := uc.tasks.MarkExecuted(ctx, t.ID, now); err != nil {
			uc.logger.Error("scheduler: no se marco ejecutada la tarea", zap.String("task_id", t.ID.String()), zap.Error(err))
		}
	}
}

// ListPendingTasks pagina las tareas puntuales pendientes de la empresa que vencen dentro de
// domain.PendingTasksWindow, vistas desde el reloj del caso de uso, con el total.
func (uc *SchedulerUseCase) ListPendingTasks(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]*domain.ScheduledTask, int64, error) {
	return uc.tasks.ListPending(ctx, domain.TaskFilter{
		TenantID: tenantID, Before: uc.now().Add(domain.PendingTasksWindow), Page: page, PerPage: perPage,
	})
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
