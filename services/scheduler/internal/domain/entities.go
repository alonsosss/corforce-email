package domain

import (
	"time"

	"github.com/google/uuid"
)

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
	ID              uuid.UUID
	TenantID        *uuid.UUID
	Name            string
	Code            string
	Description     *string
	JobType         string
	CronExpression  *string
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

type JobSchedule struct {
	JobID     uuid.UUID
	TenantID  *uuid.UUID
	NextRunAt time.Time
	LastRunAt *time.Time
	IsLocked  bool
	LockedBy  *string
	LockedAt  *time.Time
}

// IsPlatform indica un trabajo de plataforma: sin empresa, definido por la plataforma.
func (j *JobDefinition) IsPlatform() bool { return j.TenantID == nil }
