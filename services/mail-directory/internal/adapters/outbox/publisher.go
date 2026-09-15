// Package outbox emite los eventos del directorio por la outbox de la celda
// (platform.event_outbox, pkg/outbox): cada evento se encola en la misma transaccion que
// el cambio que lo origina, y el rele de la celda que arranca main.go lo entrega despues
// al stream MAIL_DIRECTORY. Un proceso que cae entre el commit y NATS ya no pierde el
// evento: billing cuenta buzones y dominios con ellos y mail-security mantiene las claves
// de Redis de los motores.
//
// Subjects, sobre y payloads son los que ya consumen billing, mail-security y el webmail;
// cambiarlos rompe a esos consumidores en tiempo de ejecucion.
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
)

const (
	StreamName = "MAIL_DIRECTORY"
	// StreamSubjects es el comodin del stream: un solo dueno para todo mail.>.
	StreamSubjects = "mail.>"

	SubjectDomainCreated      = "mail.domain.created"
	SubjectDomainUpdated      = "mail.domain.updated"
	SubjectDomainDeleted      = "mail.domain.deleted"
	SubjectDomainActivated    = "mail.domain.activated"
	SubjectAliasDomainCreated = "mail.alias_domain.created"
	SubjectAliasDomainUpdated = "mail.alias_domain.updated"
	SubjectAliasDomainDeleted = "mail.alias_domain.deleted"
	SubjectMailboxCreated     = "mail.mailbox.created"
	SubjectMailboxUpdated     = "mail.mailbox.updated"
	SubjectMailboxDeleted     = "mail.mailbox.deleted"
	// SubjectMailboxCredentialsChanged: una credencial del buzon dejo de valer o perdio
	// protocolos; credential dice cual (domain.Credential), y un cambio del buzon que le quita
	// inicios de sesion sale como la principal. mail-security echa al buzon de Dovecot con
	// cualquiera; el webmail revoca sus sesiones solo con la principal.
	SubjectMailboxCredentialsChanged = "mail.mailbox.credentials_changed"
	SubjectAliasCreated              = "mail.alias.created"
	SubjectAliasUpdated              = "mail.alias.updated"
	SubjectAliasDeleted              = "mail.alias.deleted"

	source = "mail-directory"
)

// Publisher implementa ports.EventPublisher. Recibe el db.ContextPool: Enqueue escribe
// por la transaccion del contexto, que es la del caso de uso.
type Publisher struct {
	exec outbox.Execer
}

func NewPublisher(exec outbox.Execer) *Publisher { return &Publisher{exec: exec} }

// Cada payload se escribe en su llamada, con la forma Publish(subject, evento): es la que
// lee ops/scaffold/eventcontracts para fijar el contrato de cada subject frente a sus
// consumidores.

func (p *Publisher) DomainCreated(ctx context.Context, d *domain.Domain) error {
	return p.in(ctx).Publish(SubjectDomainCreated, events.Event{TenantID: d.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": d.TenantID.String(), "id": d.ID.String(), "domain": d.Domain, "active": d.Active, "backupmx": d.BackupMX,
	}})
}

func (p *Publisher) DomainUpdated(ctx context.Context, d *domain.Domain) error {
	return p.in(ctx).Publish(SubjectDomainUpdated, events.Event{TenantID: d.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": d.TenantID.String(), "id": d.ID.String(), "domain": d.Domain, "active": d.Active, "backupmx": d.BackupMX,
	}})
}

func (p *Publisher) DomainDeleted(ctx context.Context, d *domain.Domain) error {
	return p.in(ctx).Publish(SubjectDomainDeleted, events.Event{TenantID: d.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": d.TenantID.String(), "id": d.ID.String(), "domain": d.Domain, "active": d.Active, "backupmx": d.BackupMX,
	}})
}

func (p *Publisher) DomainActivated(ctx context.Context, d *domain.Domain) error {
	return p.in(ctx).Publish(SubjectDomainActivated, events.Event{TenantID: d.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": d.TenantID.String(), "id": d.ID.String(), "domain": d.Domain, "active": d.Active, "backupmx": d.BackupMX,
	}})
}

func (p *Publisher) AliasDomainCreated(ctx context.Context, a *domain.AliasDomain) error {
	return p.in(ctx).Publish(SubjectAliasDomainCreated, events.Event{TenantID: a.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": a.TenantID.String(), "id": a.ID.String(), "alias_domain": a.AliasDomain,
		"target_domain": a.TargetDomain, "active": a.Active,
	}})
}

// AliasDomainUpdated cubre el cambio de destino o de estado: sin el, apagar un dominio
// alias no salia de DOMAIN_MAP hasta la reconciliacion periodica de mail-security.
func (p *Publisher) AliasDomainUpdated(ctx context.Context, a *domain.AliasDomain) error {
	return p.in(ctx).Publish(SubjectAliasDomainUpdated, events.Event{TenantID: a.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": a.TenantID.String(), "id": a.ID.String(), "alias_domain": a.AliasDomain,
		"target_domain": a.TargetDomain, "active": a.Active,
	}})
}

func (p *Publisher) AliasDomainDeleted(ctx context.Context, a *domain.AliasDomain) error {
	return p.in(ctx).Publish(SubjectAliasDomainDeleted, events.Event{TenantID: a.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": a.TenantID.String(), "id": a.ID.String(), "alias_domain": a.AliasDomain,
		"target_domain": a.TargetDomain, "active": a.Active,
	}})
}

func (p *Publisher) MailboxCreated(ctx context.Context, m *domain.Mailbox) error {
	return p.in(ctx).Publish(SubjectMailboxCreated, events.Event{TenantID: m.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": m.TenantID.String(), "id": m.ID.String(), "username": m.Username, "domain": m.Domain,
		"active": m.Active, "kind": m.Kind,
	}})
}

func (p *Publisher) MailboxUpdated(ctx context.Context, m *domain.Mailbox) error {
	return p.in(ctx).Publish(SubjectMailboxUpdated, events.Event{TenantID: m.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": m.TenantID.String(), "id": m.ID.String(), "username": m.Username, "domain": m.Domain,
		"active": m.Active, "kind": m.Kind,
	}})
}

func (p *Publisher) MailboxDeleted(ctx context.Context, m *domain.Mailbox) error {
	return p.in(ctx).Publish(SubjectMailboxDeleted, events.Event{TenantID: m.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": m.TenantID.String(), "id": m.ID.String(), "username": m.Username, "domain": m.Domain,
		"active": m.Active, "kind": m.Kind,
	}})
}

// MailboxCredentialsChanged rechaza una credencial desconocida: un consumidor que la leyera
// como la principal cerraria sesiones que no debia, y al reves dejaria abiertas las que si.
func (p *Publisher) MailboxCredentialsChanged(ctx context.Context, m *domain.Mailbox, credential domain.Credential) error {
	if !credential.Valid() {
		return fmt.Errorf("credencial %q desconocida en %s", credential, SubjectMailboxCredentialsChanged)
	}
	return p.in(ctx).Publish(SubjectMailboxCredentialsChanged, events.Event{TenantID: m.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": m.TenantID.String(), "id": m.ID.String(), "username": m.Username,
		"changed_at": time.Now().UTC().Format(time.RFC3339), "credential": string(credential),
	}})
}

func (p *Publisher) AliasCreated(ctx context.Context, a *domain.Alias) error {
	return p.in(ctx).Publish(SubjectAliasCreated, events.Event{TenantID: a.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": a.TenantID.String(), "id": a.ID.String(), "address": a.Address, "goto": a.Goto,
		"domain": a.Domain, "active": a.Active, "internal": a.Internal,
	}})
}

func (p *Publisher) AliasUpdated(ctx context.Context, a *domain.Alias) error {
	return p.in(ctx).Publish(SubjectAliasUpdated, events.Event{TenantID: a.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": a.TenantID.String(), "id": a.ID.String(), "address": a.Address, "goto": a.Goto,
		"domain": a.Domain, "active": a.Active, "internal": a.Internal,
	}})
}

func (p *Publisher) AliasDeleted(ctx context.Context, a *domain.Alias) error {
	return p.in(ctx).Publish(SubjectAliasDeleted, events.Event{TenantID: a.TenantID.String(), Data: map[string]interface{}{
		"tenant_id": a.TenantID.String(), "id": a.ID.String(), "address": a.Address, "goto": a.Goto,
		"domain": a.Domain, "active": a.Active, "internal": a.Internal,
	}})
}

// in liga el publicador a la transaccion del contexto.
func (p *Publisher) in(ctx context.Context) transactional {
	return transactional{ctx: ctx, exec: p.exec}
}

// transactional vive solo durante la llamada que lo crea; el contexto que guarda es el de
// esa transaccion.
type transactional struct {
	ctx  context.Context
	exec outbox.Execer
}

// Publish completa el sobre como lo hacia el publicador directo (tipo y fuente; empresa
// ya fijada por el llamante) y encola. El id lo asigna Enqueue y el rele lo conserva en
// cada reintento: es la clave de deduplicacion de JetStream y de los consumidores.
func (t transactional) Publish(subject string, evt events.Event) error {
	evt.Type = subject
	evt.Source = source
	return outbox.Enqueue(t.ctx, t.exec, subject, evt)
}
