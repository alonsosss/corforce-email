package domain

import (
	"time"

	"github.com/google/uuid"
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
