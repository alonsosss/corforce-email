package nats

import "github.com/alonsosss/corforce-email/pkg/events"

type EventPublisher struct {
	bus *events.Bus
}

func NewEventPublisher(bus *events.Bus) *EventPublisher {
	return &EventPublisher{bus: bus}
}

func (p *EventPublisher) PublishJobStarted(tenantID, jobID, executionID string) error {
	return p.bus.Publish("scheduler.job.started", events.Event{
		Type:     "scheduler.job.started",
		Source:   "scheduler-service",
		TenantID: tenantID,
		Data:     map[string]string{"job_id": jobID, "execution_id": executionID},
	})
}

func (p *EventPublisher) PublishJobCompleted(tenantID, jobID, executionID string) error {
	return p.bus.Publish("scheduler.job.completed", events.Event{
		Type:     "scheduler.job.completed",
		Source:   "scheduler-service",
		TenantID: tenantID,
		Data:     map[string]string{"job_id": jobID, "execution_id": executionID},
	})
}

func (p *EventPublisher) PublishJobFailed(tenantID, jobID, executionID, errMsg string) error {
	return p.bus.Publish("scheduler.job.failed", events.Event{
		Type:     "scheduler.job.failed",
		Source:   "scheduler-service",
		TenantID: tenantID,
		Data:     map[string]string{"job_id": jobID, "execution_id": executionID, "error": errMsg},
	})
}
