package nats

import (
	"github.com/alonsosss/corforce-email/pkg/events"
	"go.uber.org/zap"
)

const eventSource = "mail-security-service"

// Publisher implementa ports.EventPublisher sobre el bus NATS. Best-effort tras el
// commit: sin bus el servicio opera igual y los avisos no salen (pendiente outbox en
// pkg/events).
type Publisher struct {
	bus    *events.Bus
	logger *zap.Logger
}

func NewPublisher(bus *events.Bus, logger *zap.Logger) *Publisher {
	return &Publisher{bus: bus, logger: logger}
}

func (p *Publisher) Publish(subject string, payload map[string]any) {
	if p == nil || p.bus == nil {
		return
	}
	evt := events.Event{Type: subject, Source: eventSource, Data: payload}
	if v, ok := payload["tenant_id"].(string); ok {
		evt.TenantID = v
	}
	if err := p.bus.PublishPersistent(subject, evt); err != nil {
		p.logger.Warn("evento no publicado", zap.String("subject", subject), zap.Error(err))
	}
}
