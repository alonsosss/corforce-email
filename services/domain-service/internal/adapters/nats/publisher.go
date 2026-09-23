package nats

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

// Eventos persistentes del dominio domains (stream DOMAINS, subjects domains.>). Los
// consumen auditoria, notificaciones y, en fase 3, el plano transaccional. El payload
// nunca lleva claves ni el token de propiedad. Cada metodo escribe el subject y el
// payload literales para que ops/scaffold/eventcontracts pueda leer el contrato. Los de
// las claves DKIM salen por la outbox (adapters/outbox).
const (
	StreamName = "DOMAINS"

	SubjectDomainCreated  = "domains.domain.created"
	SubjectDomainVerified = "domains.domain.verified"
	SubjectDomainFailed   = "domains.domain.failed"
	SubjectDomainDeleted  = "domains.domain.deleted"

	eventSource = "domain-service"
)

// Publisher implementa ports.EventPublisher. Con bus nil no publica: el servicio arranca
// sin NATS y el alta sigue funcionando sin avisos.
type Publisher struct {
	bus *events.Bus
}

func NewPublisher(bus *events.Bus) *Publisher { return &Publisher{bus: bus} }

func (p *Publisher) enabled() bool { return p != nil && p.bus != nil }

func (p *Publisher) DomainCreated(_ context.Context, d *domain.Domain) error {
	if !p.enabled() {
		return nil
	}
	return p.bus.PublishPersistent(SubjectDomainCreated, events.Event{
		Type: SubjectDomainCreated, Source: eventSource, TenantID: d.TenantID.String(),
		Data: map[string]interface{}{
			"tenant_id": d.TenantID.String(),
			"domain_id": d.ID.String(),
			"domain":    d.Domain,
			"purpose":   string(d.Purpose),
			"status":    string(d.Status),
		},
	})
}

// DomainVerified lleva sending_ready (si SES acepta ya envios del dominio) solo cuando domain-service
// gestiona su identidad en SES; sin la integracion lo omite, porque no lo sabe.
func (p *Publisher) DomainVerified(_ context.Context, d *domain.Domain, sendingReady *bool) error {
	if !p.enabled() {
		return nil
	}
	data := map[string]interface{}{
		"tenant_id": d.TenantID.String(),
		"domain_id": d.ID.String(),
		"domain":    d.Domain,
		"purpose":   string(d.Purpose),
		"status":    string(d.Status),
	}
	if sendingReady != nil {
		data["sending_ready"] = *sendingReady
	}
	return p.bus.PublishPersistent(SubjectDomainVerified, events.Event{
		Type: SubjectDomainVerified, Source: eventSource, TenantID: d.TenantID.String(),
		Data: data,
	})
}

func (p *Publisher) DomainFailed(_ context.Context, d *domain.Domain) error {
	if !p.enabled() {
		return nil
	}
	return p.bus.PublishPersistent(SubjectDomainFailed, events.Event{
		Type: SubjectDomainFailed, Source: eventSource, TenantID: d.TenantID.String(),
		Data: map[string]interface{}{
			"tenant_id": d.TenantID.String(),
			"domain_id": d.ID.String(),
			"domain":    d.Domain,
			"purpose":   string(d.Purpose),
			"status":    string(d.Status),
		},
	})
}

func (p *Publisher) DomainDeleted(_ context.Context, d *domain.Domain) error {
	if !p.enabled() {
		return nil
	}
	return p.bus.PublishPersistent(SubjectDomainDeleted, events.Event{
		Type: SubjectDomainDeleted, Source: eventSource, TenantID: d.TenantID.String(),
		Data: map[string]interface{}{
			"tenant_id": d.TenantID.String(),
			"domain_id": d.ID.String(),
			"domain":    d.Domain,
			"purpose":   string(d.Purpose),
			"status":    string(d.Status),
		},
	})
}
