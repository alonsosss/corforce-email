package nats

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
)

// Eventos persistentes del directorio de correo. Los consume mail-security para alimentar
// DOMAIN_MAP y las etiquetas de entrega en Redis. El payload lleva lo que un consumidor
// necesita para materializar el cambio; nunca contrasenas ni hashes.
const (
	StreamName = "MAIL_DIRECTORY"
	// StreamSubjects es el comodin del stream: un solo dueno para todo mail.>.
	StreamSubjects = "mail.>"

	SubjectDomainCreated      = "mail.domain.created"
	SubjectDomainUpdated      = "mail.domain.updated"
	SubjectDomainDeleted      = "mail.domain.deleted"
	SubjectDomainActivated    = "mail.domain.activated"
	SubjectAliasDomainCreated = "mail.alias_domain.created"
	SubjectAliasDomainDeleted = "mail.alias_domain.deleted"
	SubjectMailboxCreated     = "mail.mailbox.created"
	SubjectMailboxUpdated     = "mail.mailbox.updated"
	SubjectMailboxDeleted     = "mail.mailbox.deleted"
	SubjectAliasCreated       = "mail.alias.created"
	SubjectAliasUpdated       = "mail.alias.updated"
	SubjectAliasDeleted       = "mail.alias.deleted"

	eventSource = "mail-directory"
)

// Publisher implementa ports.EventPublisher. Con bus nil no publica: el servicio arranca
// sin NATS y el directorio sigue operativo.
type Publisher struct {
	bus *events.Bus
}

func NewPublisher(bus *events.Bus) *Publisher { return &Publisher{bus: bus} }

func (p *Publisher) emit(subject, tenantID string, data map[string]interface{}) error {
	if p == nil || p.bus == nil {
		return nil
	}
	return p.bus.PublishPersistent(subject, events.Event{
		Type: subject, Source: eventSource, TenantID: tenantID, Data: data,
	})
}

func domainData(d *domain.Domain) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id": d.TenantID.String(),
		"id":        d.ID.String(),
		"domain":    d.Domain,
		"active":    d.Active,
		"backupmx":  d.BackupMX,
	}
}

func (p *Publisher) DomainCreated(_ context.Context, d *domain.Domain) error {
	return p.emit(SubjectDomainCreated, d.TenantID.String(), domainData(d))
}

func (p *Publisher) DomainUpdated(_ context.Context, d *domain.Domain) error {
	return p.emit(SubjectDomainUpdated, d.TenantID.String(), domainData(d))
}

func (p *Publisher) DomainDeleted(_ context.Context, d *domain.Domain) error {
	return p.emit(SubjectDomainDeleted, d.TenantID.String(), domainData(d))
}

func (p *Publisher) DomainActivated(_ context.Context, d *domain.Domain) error {
	return p.emit(SubjectDomainActivated, d.TenantID.String(), domainData(d))
}

func aliasDomainData(a *domain.AliasDomain) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id":     a.TenantID.String(),
		"id":            a.ID.String(),
		"alias_domain":  a.AliasDomain,
		"target_domain": a.TargetDomain,
		"active":        a.Active,
	}
}

func (p *Publisher) AliasDomainCreated(_ context.Context, a *domain.AliasDomain) error {
	return p.emit(SubjectAliasDomainCreated, a.TenantID.String(), aliasDomainData(a))
}

func (p *Publisher) AliasDomainDeleted(_ context.Context, a *domain.AliasDomain) error {
	return p.emit(SubjectAliasDomainDeleted, a.TenantID.String(), aliasDomainData(a))
}

func mailboxData(m *domain.Mailbox) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id": m.TenantID.String(),
		"id":        m.ID.String(),
		"username":  m.Username,
		"domain":    m.Domain,
		"active":    m.Active,
		"kind":      m.Kind,
	}
}

func (p *Publisher) MailboxCreated(_ context.Context, m *domain.Mailbox) error {
	return p.emit(SubjectMailboxCreated, m.TenantID.String(), mailboxData(m))
}

func (p *Publisher) MailboxUpdated(_ context.Context, m *domain.Mailbox) error {
	return p.emit(SubjectMailboxUpdated, m.TenantID.String(), mailboxData(m))
}

func (p *Publisher) MailboxDeleted(_ context.Context, m *domain.Mailbox) error {
	return p.emit(SubjectMailboxDeleted, m.TenantID.String(), mailboxData(m))
}

func aliasData(a *domain.Alias) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id": a.TenantID.String(),
		"id":        a.ID.String(),
		"address":   a.Address,
		"goto":      a.Goto,
		"domain":    a.Domain,
		"active":    a.Active,
		"internal":  a.Internal,
	}
}

func (p *Publisher) AliasCreated(_ context.Context, a *domain.Alias) error {
	return p.emit(SubjectAliasCreated, a.TenantID.String(), aliasData(a))
}

func (p *Publisher) AliasUpdated(_ context.Context, a *domain.Alias) error {
	return p.emit(SubjectAliasUpdated, a.TenantID.String(), aliasData(a))
}

func (p *Publisher) AliasDeleted(_ context.Context, a *domain.Alias) error {
	return p.emit(SubjectAliasDeleted, a.TenantID.String(), aliasData(a))
}
