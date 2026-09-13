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
	GetByCode(ctx context.Context, code string) (*domain.JobDefinition, error)
	List(ctx context.Context, tenantID *uuid.UUID, isActive *bool) ([]*domain.JobDefinition, error)
	Update(ctx context.Context, job *domain.JobDefinition) error
	// Deactivate desactiva el trabajo con updatedAt como hora del cambio, igual que Update.
	Deactivate(ctx context.Context, id uuid.UUID, updatedAt time.Time) error
}

type JobExecutionRepository interface {
	// Create devuelve domain.ErrAlreadyRetried si exec.RetryOf ya tiene un reintento.
	Create(ctx context.Context, exec *domain.JobExecution) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error)
	// GetForUpdate carga la ejecucion bloqueando su fila hasta el final de la transaccion.
	GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error)
	GetByJob(ctx context.Context, jobID uuid.UUID, page, pageSize int) ([]*domain.JobExecution, int64, error)
	Update(ctx context.Context, exec *domain.JobExecution) error
	ListRunning(ctx context.Context) ([]*domain.JobExecution, error)
	// ClaimOverdue bloquea la ejecucion activa con el plazo vencido mas antiguo, saltando
	// las que otra transaccion ya tiene; nil si no queda ninguna.
	ClaimOverdue(ctx context.Context, now time.Time) (*domain.JobExecution, error)
	// ClaimDispatchable bloquea el reintento en espera cuya hora ya llego; nil si no hay.
	ClaimDispatchable(ctx context.Context, now time.Time) (*domain.JobExecution, error)
}

type ScheduledTaskRepository interface {
	Create(ctx context.Context, task *domain.ScheduledTask) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error)
	ListPending(ctx context.Context, before time.Time) ([]*domain.ScheduledTask, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status string) error
	Cancel(ctx context.Context, id uuid.UUID) error
}

type JobScheduleRepository interface {
	// UpdateNextRun reprograma tras lanzar el trabajo: fija next_run_at y marca last_run_at.
	UpdateNextRun(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time) error
	// SetNextRun planifica el trabajo sin marcar una ejecucion (alta, edicion, reactivacion,
	// reconciliacion): last_run_at no cambia.
	SetNextRun(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time) error
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
