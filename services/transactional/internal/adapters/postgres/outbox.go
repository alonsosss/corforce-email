package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/google/uuid"
)

const eventSource = "transactional-service"

// OutboxPublisher implementa ports.EventPublisher sobre platform.event_outbox de la base
// de la empresa: el evento se escribe en la misma transaccion que el dato y el rele
// (outbox.RunForTenants) lo publica en JetStream.
type OutboxPublisher struct {
	pool *db.ContextPool
}

func NewOutboxPublisher(pool *db.ContextPool) *OutboxPublisher {
	return &OutboxPublisher{pool: pool}
}

func (p *OutboxPublisher) Publish(ctx context.Context, subject string, tenantID uuid.UUID, payload map[string]any) error {
	return outbox.Enqueue(ctx, p.pool, subject, events.Event{
		Type:     subject,
		Source:   eventSource,
		TenantID: tenantID.String(),
		Data:     payload,
	})
}
