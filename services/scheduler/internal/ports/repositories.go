package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

type JobDefinitionRepository interface {
	Create(ctx context.Context, job *domain.JobDefinition) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error)
	GetByCode(ctx context.Context, code string) (*domain.JobDefinition, error)
	List(ctx context.Context, tenantID *uuid.UUID, isActive *bool) ([]*domain.JobDefinition, error)
	Update(ctx context.Context, job *domain.JobDefinition) error
	Deactivate(ctx context.Context, id uuid.UUID) error
}

type JobExecutionRepository interface {
	Create(ctx context.Context, exec *domain.JobExecution) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error)
	GetByJob(ctx context.Context, jobID uuid.UUID, page, pageSize int) ([]*domain.JobExecution, int64, error)
	Update(ctx context.Context, exec *domain.JobExecution) error
	GetLastByJob(ctx context.Context, jobID uuid.UUID) (*domain.JobExecution, error)
	ListRunning(ctx context.Context) ([]*domain.JobExecution, error)
}

type ScheduledTaskRepository interface {
	Create(ctx context.Context, task *domain.ScheduledTask) error
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error)
	ListPending(ctx context.Context, before time.Time) ([]*domain.ScheduledTask, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status string) error
	Cancel(ctx context.Context, id uuid.UUID) error
}

type JobScheduleRepository interface {
	GetByJob(ctx context.Context, jobID uuid.UUID) (*domain.JobSchedule, error)
	UpdateNextRun(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time) error
	Lock(ctx context.Context, jobID uuid.UUID, lockerID string) (bool, error)
	Unlock(ctx context.Context, jobID uuid.UUID) error
	GetDue(ctx context.Context, now time.Time) ([]*domain.JobSchedule, error)
}

type EventPublisher interface {
	PublishJobStarted(tenantID, jobID, executionID string) error
	PublishJobCompleted(tenantID, jobID, executionID string) error
	PublishJobFailed(tenantID, jobID, executionID, errMsg string) error
}
