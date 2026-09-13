package domain

import (
	"math"
	"time"

	"github.com/google/uuid"
)

// PendingTasksWindow es cuanto hacia adelante mira el listado de tareas puntuales pendientes.
const PendingTasksWindow = 24 * time.Hour

// Estados de una ejecucion. pending espera su intento (un reintento con espera todavia no
// despachado); running esta despachada y corre el plazo de su manejador; los otros tres
// son terminales.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

const (
	JobTypeCron     = "cron"
	JobTypeInterval = "interval"
	JobTypeOneTime  = "one_time"
)

// Motivos de una ejecucion fallida (columna failure_reason).
const (
	// FailureExecutor: el servicio ejecutor informo el fallo.
	FailureExecutor = "executor"
	// FailureTimeout: nadie cerro la ejecucion antes de su plazo.
	FailureTimeout = "timeout"
	// FailureHandlerNotAllowed: el manejador del trabajo ya no esta en el catalogo, asi que
	// no hay a quien despacharla.
	FailureHandlerNotAllowed = "handler_not_allowed"
)

type JobDefinition struct {
	ID             uuid.UUID
	TenantID       *uuid.UUID
	Name           string
	Code           string
	Description    *string
	JobType        string
	CronExpression *string
	// Timezone es la zona IANA en la que se evalua CronExpression. No afecta a @every, a
	// interval ni a one_time, que cuentan tiempo transcurrido.
	Timezone        string
	IntervalMinutes *int
	Handler         string
	Payload         *string
	IsActive        bool
	MaxRetries      int
	TimeoutSeconds  int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type JobExecution struct {
	ID           uuid.UUID
	JobID        uuid.UUID
	TenantID     *uuid.UUID
	Status       string
	StartedAt    *time.Time
	CompletedAt  *time.Time
	Duration     *int64
	Result       *string
	ErrorMessage *string
	RetryCount   int
	CreatedAt    time.Time
	// DeadlineAt es el limite para recibir el cierre de una ejecucion despachada.
	DeadlineAt *time.Time
	// NextAttemptAt es cuando se despacha un reintento que espera en pending.
	NextAttemptAt *time.Time
	// RetryOf es la ejecucion fallida de la que esta es el reintento.
	RetryOf       *uuid.UUID
	FailureReason *string
}

// Estados de una tarea puntual (scheduled_tasks_status_check).
const (
	TaskStatusScheduled = "scheduled"
	TaskStatusExecuted  = "executed"
	TaskStatusCancelled = "cancelled"
)

type ScheduledTask struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Description *string
	TriggerAt   time.Time
	Handler     string
	Payload     *string
	Status      string
	ExecutedAt  *time.Time
	CreatedAt   time.Time
}

// Cancel detiene una tarea programada. Cancelar dos veces no cambia nada, como con una
// ejecucion; una tarea ya ejecutada no se cancela.
func (t *ScheduledTask) Cancel() (bool, error) {
	switch t.Status {
	case TaskStatusScheduled:
		t.Status = TaskStatusCancelled
		return true, nil
	case TaskStatusCancelled:
		return false, nil
	default:
		return false, ErrTaskNotCancellable
	}
}

// TaskFilter es una pagina de las tareas pendientes de una empresa que vencen hasta Before.
type TaskFilter struct {
	TenantID uuid.UUID
	Before   time.Time
	Page     int
	PerPage  int
}

// Offset es el desplazamiento de la pagina (ver PageOffset).
func (f TaskFilter) Offset() int64 { return PageOffset(f.Page, f.PerPage) }

type JobSchedule struct {
	JobID     uuid.UUID
	TenantID  *uuid.UUID
	NextRunAt time.Time
	LastRunAt *time.Time
	IsLocked  bool
	LockedBy  *string
	LockedAt  *time.Time
}

// CronJobSchedule es el calendario de un trabajo cron activo con la expresion y la zona de
// las que deberia salir su next_run_at.
type CronJobSchedule struct {
	JobID uuid.UUID
	// TenantID es la empresa del trabajo; nil si es de plataforma.
	TenantID   *uuid.UUID
	Expression string
	Timezone   string
	NextRunAt  time.Time
}

// JobOverview es un trabajo tal como se lee: con su calendario y su ultima ejecucion.
type JobOverview struct {
	Job JobDefinition
	// NextRunAt es la proxima ejecucion que preve el calendario; nil si el trabajo esta
	// inactivo o no tiene calendario.
	NextRunAt *time.Time
	// LastRunAt es la ultima vez que el calendario lo despacho; lanzarlo a mano no la cambia.
	LastRunAt *time.Time
	// LastExecution es su ejecucion creada mas reciente: programada, manual o reintento.
	LastExecution *ExecutionSummary
}

// NewJobOverview arma la lectura de un trabajo. Un trabajo inactivo conserva la fila de su
// calendario, pero esa hora ya no es una ejecucion prevista.
func NewJobOverview(job JobDefinition, nextRunAt, lastRunAt *time.Time, last *ExecutionSummary) *JobOverview {
	if !job.IsActive {
		nextRunAt = nil
	}
	return &JobOverview{Job: job, NextRunAt: nextRunAt, LastRunAt: lastRunAt, LastExecution: last}
}

// ExecutionSummary es lo que el listado de trabajos muestra de una ejecucion.
type ExecutionSummary struct {
	ID            uuid.UUID
	Status        string
	CompletedAt   *time.Time
	FailureReason *string
}

// JobFilter es una pagina del listado de trabajos de una empresa: los suyos y los de
// plataforma.
type JobFilter struct {
	TenantID uuid.UUID
	IsActive *bool
	Page     int
	PerPage  int
}

// Offset es el desplazamiento de la pagina (ver PageOffset).
func (f JobFilter) Offset() int64 { return PageOffset(f.Page, f.PerPage) }

// PageOffset es el desplazamiento de la pagina page de perPage filas. Una pagina que no cabe
// en un int64 satura: no tiene filas, en vez de desbordar a un desplazamiento negativo que la
// base rechaza con un 500.
func PageOffset(page, perPage int) int64 {
	if page <= 1 || perPage <= 0 {
		return 0
	}
	if int64(page-1) > math.MaxInt64/int64(perPage) {
		return math.MaxInt64
	}
	return int64(page-1) * int64(perPage)
}

// CronExpr es la expresion del trabajo, vacia si no tiene.
func (j *JobDefinition) CronExpr() string {
	if j.CronExpression == nil {
		return ""
	}
	return *j.CronExpression
}

// IsPlatform indica un trabajo de plataforma: sin empresa, definido por la plataforma.
func (j *JobDefinition) IsPlatform() bool { return j.TenantID == nil }
