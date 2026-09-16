package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// Transactor abre una transaccion en la base de la empresa del contexto. Todo lo que fn
// escribe, tambien la outbox, se confirma o se descarta junto.
type Transactor interface {
	Transact(ctx context.Context, fn func(ctx context.Context) error) error
}

type JobDefinitionRepository interface {
	Create(ctx context.Context, job *domain.JobDefinition) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error)
	// GetForUpdate lee el trabajo como GetByID bloqueando su fila hasta el final de la
	// transaccion.
	GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error)
	GetByCode(ctx context.Context, code string) (*domain.JobDefinition, error)
	// GetOverview lee el trabajo con su calendario y su ultima ejecucion.
	GetOverview(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobOverview, error)
	// List devuelve una pagina del listado con el total de filas que cumplen el filtro, en
	// una sola consulta por pagina: sin una lectura por trabajo.
	List(ctx context.Context, filter domain.JobFilter) ([]*domain.JobOverview, int64, error)
	// Update escribe la definicion del trabajo solo si sigue siendo de job.TenantID (nil, de
	// plataforma), domain.ErrJobNotFound si no, y sigue en job.Version, que sube en uno,
	// domain.ErrJobVersionConflict si no. La empresa de un trabajo no cambia y su estado
	// tampoco: is_active solo lo escriben Activate y Deactivate, que no tocan la version.
	Update(ctx context.Context, job *domain.JobDefinition) error
	// Activate activa el trabajo de owner (nil, de plataforma) con updatedAt como hora del
	// cambio; domain.ErrJobNotFound si no es suyo.
	Activate(ctx context.Context, id uuid.UUID, owner *uuid.UUID, updatedAt time.Time) error
	// Deactivate desactiva el trabajo de owner (nil, de plataforma) con updatedAt como hora
	// del cambio, igual que Activate; domain.ErrJobNotFound si no es suyo.
	Deactivate(ctx context.Context, id uuid.UUID, owner *uuid.UUID, updatedAt time.Time) error
}

type JobExecutionRepository interface {
	// Create devuelve domain.ErrAlreadyRetried si exec.RetryOf ya tiene un reintento.
	Create(ctx context.Context, exec *domain.JobExecution) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error)
	// GetForUpdate carga la ejecucion bloqueando su fila hasta el final de la transaccion.
	GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error)
	// GetByJob pagina las ejecuciones del trabajo que ve la empresa (las suyas y las de
	// plataforma), de la mas reciente a la mas antigua, con el total.
	GetByJob(ctx context.Context, jobID, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error)
	// Update escribe la ejecucion solo si sigue siendo de exec.TenantID:
	// domain.ErrExecutionNotFound si no.
	Update(ctx context.Context, exec *domain.JobExecution) error
	// ListRunning pagina las ejecuciones activas que ve la empresa (las suyas y las de
	// plataforma), de la mas reciente a la mas antigua, con el total.
	ListRunning(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error)
	// ClaimOverdue bloquea la ejecucion activa con el plazo vencido mas antiguo, saltando
	// las que otra transaccion ya tiene; nil si no queda ninguna.
	ClaimOverdue(ctx context.Context, now time.Time) (*domain.JobExecution, error)
	// ClaimDispatchable bloquea el reintento en espera cuya hora ya llego; nil si no hay.
	ClaimDispatchable(ctx context.Context, now time.Time) (*domain.JobExecution, error)
}

// ScheduledTaskRepository: toda lectura o escritura por id lleva la empresa. Una tarea de
// otra empresa es domain.ErrTaskNotFound, igual que una que no existe.
type ScheduledTaskRepository interface {
	Create(ctx context.Context, task *domain.ScheduledTask) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error)
	// GetForUpdate carga la tarea bloqueando su fila hasta el final de la transaccion.
	GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error)
	// ListPending pagina las programadas de la empresa que vencen hasta filter.Before, por
	// hora de disparo, con el total.
	ListPending(ctx context.Context, filter domain.TaskFilter) ([]*domain.ScheduledTask, int64, error)
	// Cancel pasa a cancelada una tarea programada de la empresa.
	Cancel(ctx context.Context, id, tenantID uuid.UUID) error
	// ListDue lista las programadas vencidas de la base del contexto, sin mirar la empresa:
	// es el barrido del ticker, que ya recorre una base por empresa.
	ListDue(ctx context.Context, now time.Time) ([]*domain.ScheduledTask, error)
	// MarkExecuted pasa a ejecutada una tarea que sigue programada; false si ya no lo estaba
	// (se cancelo entre la lectura y la escritura).
	MarkExecuted(ctx context.Context, id uuid.UUID, at time.Time) (bool, error)
}

type JobScheduleRepository interface {
	// GetForUpdate lee el calendario de un trabajo que ve la empresa (el suyo o uno de
	// plataforma) bloqueando su fila hasta el final de la transaccion; nil si no tiene. Espera
	// al despacho que lo tenga tomado (ClaimDue).
	GetForUpdate(ctx context.Context, jobID, tenantID uuid.UUID) (*domain.JobSchedule, error)
	// UpdateNextRun reprograma tras lanzar el trabajo: fija next_run_at y anota ranAt en
	// last_run_at.
	UpdateNextRun(ctx context.Context, jobID uuid.UUID, nextRunAt, ranAt time.Time) error
	// SetNextRun planifica el trabajo sin marcar una ejecucion (alta, edicion, reactivacion,
	// reconciliacion): last_run_at no cambia.
	SetNextRun(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time) error
	// Restart planifica el trabajo como recien creado: fija next_run_at y deja last_run_at a
	// NULL. Es la edicion que cambia su tipo: la ultima pasada era de otra definicion.
	Restart(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time) error
	// GetDue lista los calendarios vencidos de trabajos activos, sin bloquearlos.
	GetDue(ctx context.Context, now time.Time) ([]*domain.JobSchedule, error)
	// ClaimDue bloquea el calendario del trabajo si sigue vencido y ninguna otra
	// transaccion lo tiene. Es el bloqueo por trabajo: dura lo que la transaccion. Devuelve
	// la hora prevista que quedo bloqueada, de la que se cuenta la siguiente.
	ClaimDue(ctx context.Context, jobID uuid.UUID, now time.Time) (scheduled time.Time, claimed bool, err error)
	// LockActiveCron bloquea los calendarios de los trabajos cron activos hasta el final de
	// la transaccion, saltando los que otra ya tiene.
	LockActiveCron(ctx context.Context) ([]*domain.CronJobSchedule, error)
}

// EventPublisher encola los eventos del scheduler en la outbox. Debe llamarse dentro de
// Transactor.Transact: el evento existe si y solo si el cambio de la ejecucion existe.
type EventPublisher interface {
	JobStarted(ctx context.Context, job *domain.JobDefinition, exec *domain.JobExecution, timeoutSeconds int) error
	JobCompleted(ctx context.Context, job *domain.JobDefinition, exec *domain.JobExecution) error
	// JobFailed lleva el reintento programado, o nil si no habra otro intento.
	JobFailed(ctx context.Context, job *domain.JobDefinition, exec, retry *domain.JobExecution) error
}
