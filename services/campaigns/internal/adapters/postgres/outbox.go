package postgres

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/google/uuid"
)

const eventSource = "campaigns"

// OutboxPublisher implementa ports.EventPublisher sobre platform.event_outbox de la base
// de la empresa: el evento se escribe en la misma transaccion que el cambio de estado y
// el rele (outbox.RunForTenants) lo publica en el stream CAMPAIGNS.
type OutboxPublisher struct {
	pool *db.ContextPool
}

func NewOutboxPublisher(pool *db.ContextPool) *OutboxPublisher {
	return &OutboxPublisher{pool: pool}
}

// Publish deja en UserID a quien actuo cuando la transicion la pidio una persona; las
// del orquestador van sin usuario.
func (p *OutboxPublisher) Publish(ctx context.Context, subject string, tenantID uuid.UUID, payload map[string]any) error {
	return outbox.Enqueue(ctx, p.pool, subject, events.Event{
		Type:     subject,
		Source:   eventSource,
		TenantID: tenantID.String(),
		UserID:   middleware.GetUserID(ctx),
		Data:     payload,
	})
}
