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
	IntervalMinutes *int       `json:"interval_minutes"`
	Handler         string     `json:"handler"`
	Payload         *string    `json:"payload"`
	IsActive        bool       `json:"is_active"`
	MaxRetries      int        `json:"max_retries"`
	TimeoutSeconds  int        `json:"timeout_seconds"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
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

func jobResponse(j *domain.JobDefinition) jobDTO {
	return jobDTO{
		ID: j.ID, TenantID: j.TenantID, Name: j.Name, Code: j.Code, Description: j.Description,
		JobType: j.JobType, CronExpression: j.CronExpression, IntervalMinutes: j.IntervalMinutes,
		Handler: j.Handler, Payload: j.Payload, IsActive: j.IsActive, MaxRetries: j.MaxRetries,
		TimeoutSeconds: j.TimeoutSeconds, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
}

func jobsResponse(jobs []*domain.JobDefinition) []jobDTO {
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
