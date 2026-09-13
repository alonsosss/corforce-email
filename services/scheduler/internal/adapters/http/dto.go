package http

import (
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// Contrato JSON del scheduler, en snake_case como el resto del API. El dominio no lleva
// etiquetas: la forma publica se decide en el adaptador.

type jobDTO struct {
	ID              uuid.UUID  `json:"id"`
	TenantID        *uuid.UUID `json:"tenant_id"`
	Name            string     `json:"name"`
	Code            string     `json:"code"`
	Description     *string    `json:"description"`
	JobType         string     `json:"job_type"`
	CronExpression  *string    `json:"cron_expression"`
	Timezone        string     `json:"timezone"`
	IntervalMinutes *int       `json:"interval_minutes"`
	Handler         string     `json:"handler"`
	Payload         *string    `json:"payload"`
	IsActive        bool       `json:"is_active"`
	MaxRetries      int        `json:"max_retries"`
	TimeoutSeconds  int        `json:"timeout_seconds"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	// NextRunAt es null si el trabajo esta inactivo; LastRunAt es la ultima vez que lo
	// despacho el calendario (no un lanzamiento manual); LastExecution, la ejecucion mas
	// reciente de cualquier origen, o null.
	NextRunAt     *time.Time        `json:"next_run_at"`
	LastRunAt     *time.Time        `json:"last_run_at"`
	LastExecution *lastExecutionDTO `json:"last_execution"`
}

type lastExecutionDTO struct {
	ID            uuid.UUID  `json:"id"`
	Status        string     `json:"status"`
	CompletedAt   *time.Time `json:"completed_at"`
	FailureReason *string    `json:"failure_reason"`
}

type executionDTO struct {
	ID          uuid.UUID  `json:"id"`
	JobID       uuid.UUID  `json:"job_id"`
	TenantID    *uuid.UUID `json:"tenant_id"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	// DurationMS es la columna duration, que se guarda en milisegundos.
	DurationMS   *int64    `json:"duration_ms"`
	Result       *string   `json:"result"`
	ErrorMessage *string   `json:"error_message"`
	RetryCount   int       `json:"retry_count"`
	CreatedAt    time.Time `json:"created_at"`
	// DeadlineAt es el limite para recibir el cierre; NextAttemptAt, la hora de un reintento
	// que espera en pending.
	DeadlineAt    *time.Time `json:"deadline_at"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
	RetryOf       *uuid.UUID `json:"retry_of"`
	FailureReason *string    `json:"failure_reason"`
}

type taskDTO struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"tenant_id"`
	Name        string     `json:"name"`
	Description *string    `json:"description"`
	TriggerAt   time.Time  `json:"trigger_at"`
	Handler     string     `json:"handler"`
	Payload     *string    `json:"payload"`
	Status      string     `json:"status"`
	ExecutedAt  *time.Time `json:"executed_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type handlerDTO struct {
	Name              string   `json:"name"`
	Service           string   `json:"service"`
	Description       string   `json:"description"`
	MaxTimeoutSeconds int      `json:"max_timeout_seconds"`
	Scopes            []string `json:"scopes"`
}

func jobResponse(o *domain.JobOverview) jobDTO {
	j := o.Job
	dto := jobDTO{
		ID: j.ID, TenantID: j.TenantID, Name: j.Name, Code: j.Code, Description: j.Description,
		JobType: j.JobType, CronExpression: j.CronExpression, Timezone: j.Timezone, IntervalMinutes: j.IntervalMinutes,
		Handler: j.Handler, Payload: j.Payload, IsActive: j.IsActive, MaxRetries: j.MaxRetries,
		TimeoutSeconds: j.TimeoutSeconds, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
		NextRunAt: o.NextRunAt, LastRunAt: o.LastRunAt,
	}
	if e := o.LastExecution; e != nil {
		dto.LastExecution = &lastExecutionDTO{ID: e.ID, Status: e.Status, CompletedAt: e.CompletedAt, FailureReason: e.FailureReason}
	}
	return dto
}

func jobsResponse(jobs []*domain.JobOverview) []jobDTO {
	out := make([]jobDTO, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobResponse(j))
	}
	return out
}

func executionResponse(e *domain.JobExecution) executionDTO {
	return executionDTO{
		ID: e.ID, JobID: e.JobID, TenantID: e.TenantID, Status: e.Status, StartedAt: e.StartedAt,
		CompletedAt: e.CompletedAt, DurationMS: e.Duration, Result: e.Result,
		ErrorMessage: e.ErrorMessage, RetryCount: e.RetryCount, CreatedAt: e.CreatedAt,
		DeadlineAt: e.DeadlineAt, NextAttemptAt: e.NextAttemptAt, RetryOf: e.RetryOf,
		FailureReason: e.FailureReason,
	}
}

func executionsResponse(execs []*domain.JobExecution) []executionDTO {
	out := make([]executionDTO, 0, len(execs))
	for _, e := range execs {
		out = append(out, executionResponse(e))
	}
	return out
}

func taskResponse(t *domain.ScheduledTask) taskDTO {
	return taskDTO{
		ID: t.ID, TenantID: t.TenantID, Name: t.Name, Description: t.Description,
		TriggerAt: t.TriggerAt, Handler: t.Handler, Payload: t.Payload, Status: t.Status,
		ExecutedAt: t.ExecutedAt, CreatedAt: t.CreatedAt,
	}
}

func tasksResponse(tasks []*domain.ScheduledTask) []taskDTO {
	out := make([]taskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskResponse(t))
	}
	return out
}

func handlersResponse(specs []domain.HandlerSpec) []handlerDTO {
	out := make([]handlerDTO, 0, len(specs))
	for _, s := range specs {
		scopes := make([]string, 0, len(s.Scopes))
		for _, sc := range s.Scopes {
			scopes = append(scopes, string(sc))
		}
		out = append(out, handlerDTO{
			Name: s.Name, Service: s.Service, Description: s.Description,
			MaxTimeoutSeconds: s.MaxTimeoutSeconds, Scopes: scopes,
		})
	}
	return out
}
