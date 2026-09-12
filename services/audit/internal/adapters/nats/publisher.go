package nats

import (
	"github.com/alonsosss/corforce-email/pkg/events"
)

type EventPublisher struct {
	bus *events.Bus
}

func NewEventPublisher(bus *events.Bus) *EventPublisher {
	return &EventPublisher{bus: bus}
}

// PublishSecurityAlert publica la alerta en JetStream: si el servicio de
// notificaciones esta caido en ese momento, la alerta se reentrega en vez de
// perderse (una alerta de seguridad no puede ser fire-and-forget).
func (p *EventPublisher) PublishSecurityAlert(tenantID, eventType, detail, riskLevel, ip, userID string) error {
	if err := p.bus.EnsureStream("AUDIT_SECURITY", []string{"audit.security.>"}); err != nil {
		return err
	}
	return p.bus.PublishPersistent("audit.security.alert", events.Event{
		Type:     "security.alert",
		Source:   "audit-service",
		TenantID: tenantID,
		Data: map[string]string{
			"event_type": eventType,
			"detail":     detail,
			"risk_level": riskLevel,
			"ip":         ip,
			"user_id":    userID,
		},
	})
}
