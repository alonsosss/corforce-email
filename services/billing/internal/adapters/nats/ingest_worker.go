// Package nats consume los hechos consumados que alimentan los contadores de consumo y el
// ciclo de vida de las suscripciones. Cada subject tiene su consumidor durable, porque el
// de JetStream filtra por un unico subject y asi el contrato de cada uno queda legible.
package nats

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/billing/internal/app"
	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Subjects consumidos. Los streams los declaran sus duenos: ORGANIZATION (organization),
// MAIL_DIRECTORY (mail-directory), DOMAINS (domain-service), TRANSACTIONAL (transactional)
// y CONTACTS (contacts).
const (
	SubjectTenantCreated       = "organization.tenant.created"
	SubjectTenantStatusChanged = "organization.tenant.status_changed"
	SubjectMailboxCreated      = "mail.mailbox.created"
	SubjectMailboxDeleted      = "mail.mailbox.deleted"
	SubjectMailDomainCreated   = "mail.domain.created"
	SubjectMailDomainDeleted   = "mail.domain.deleted"
	SubjectDomainCreated       = "domains.domain.created"
	SubjectDomainDeleted       = "domains.domain.deleted"
	SubjectEmailSent           = "transactional.email.sent"
	SubjectContactCreated      = "contacts.contact.created"
	SubjectContactDeleted      = "contacts.contact.deleted"
)

// Durables, uno por subject, con el stream de origen en el nombre.
const (
	durableTenantCreated       = "billing-organization-tenant-created"
	durableTenantStatusChanged = "billing-organization-tenant-status-changed"
	durableMailboxCreated      = "billing-mail-directory-mailbox-created"
	durableMailboxDeleted      = "billing-mail-directory-mailbox-deleted"
	durableMailDomainCreated   = "billing-mail-directory-domain-created"
	durableMailDomainDeleted   = "billing-mail-directory-domain-deleted"
	durableDomainCreated       = "billing-domains-domain-created"
	durableDomainDeleted       = "billing-domains-domain-deleted"
	durableEmailSent           = "billing-transactional-email-sent"
	durableContactCreated      = "billing-contacts-contact-created"
	durableContactDeleted      = "billing-contacts-contact-deleted"
)

const (
	// Fuentes de un dominio: el mismo nombre llega de las dos y cuenta una sola vez.
	sourceMailDirectory = "mail-directory"
	sourceDomainService = "domain-service"

	// processTimeout acota la transaccion de un evento.
	processTimeout = 15 * time.Second
	// subscribeRetry es la espera entre intentos de suscripcion: si el dueno de un stream
	// aun no arranco (o no existe todavia, como contacts), se reintenta sin bloquear el HTTP.
	subscribeRetry = 10 * time.Second
)

// binding es cada suscripcion pendiente y como hacerla.
type binding struct {
	subject string
	bind    func() (*natsgo.Subscription, error)
}

// IngestWorker mantiene las suscripciones durables y traduce cada evento en una operacion
// idempotente sobre los contadores o la suscripcion.
type IngestWorker struct {
	bus    *events.Bus
	uc     *app.UseCase
	logger *zap.Logger

	mu      sync.Mutex
	subs    []*natsgo.Subscription
	stopped bool
}

func NewIngestWorker(bus *events.Bus, uc *app.UseCase, logger *zap.Logger) *IngestWorker {
	return &IngestWorker{bus: bus, uc: uc, logger: logger}
}

func (w *IngestWorker) bindings() []binding {
	return []binding{
		{subject: SubjectTenantCreated, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectTenantCreated, durableTenantCreated, w.onTenantCreated)
		}},
		{subject: SubjectTenantStatusChanged, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectTenantStatusChanged, durableTenantStatusChanged, w.onTenantStatusChanged)
		}},
		{subject: SubjectMailboxCreated, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectMailboxCreated, durableMailboxCreated, w.onMailboxCreated)
		}},
		{subject: SubjectMailboxDeleted, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectMailboxDeleted, durableMailboxDeleted, w.onMailboxDeleted)
		}},
		{subject: SubjectMailDomainCreated, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectMailDomainCreated, durableMailDomainCreated, w.onMailDomainCreated)
		}},
		{subject: SubjectMailDomainDeleted, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectMailDomainDeleted, durableMailDomainDeleted, w.onMailDomainDeleted)
		}},
		{subject: SubjectDomainCreated, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectDomainCreated, durableDomainCreated, w.onDomainCreated)
		}},
		{subject: SubjectDomainDeleted, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectDomainDeleted, durableDomainDeleted, w.onDomainDeleted)
		}},
		{subject: SubjectEmailSent, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectEmailSent, durableEmailSent, w.onEmailSent)
		}},
		{subject: SubjectContactCreated, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectContactCreated, durableContactCreated, w.onContactCreated)
		}},
		{subject: SubjectContactDeleted, bind: func() (*natsgo.Subscription, error) {
			return w.bus.DurableQueueSubscribe(SubjectContactDeleted, durableContactDeleted, w.onContactDeleted)
		}},
	}
}

// Start suscribe en segundo plano y reintenta cada subject que aun no tiene stream hasta
// conseguirlo o hasta que el contexto termine.
func (w *IngestWorker) Start(ctx context.Context) {
	pending := w.bindings()
	go func() {
		t := time.NewTicker(subscribeRetry)
		defer t.Stop()
		for {
			pending = w.subscribeAll(pending)
			if len(pending) == 0 {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (w *IngestWorker) subscribeAll(pending []binding) []binding {
	var still []binding
	for _, b := range pending {
		sub, err := b.bind()
		if err != nil {
			w.logger.Warn("billing: sin stream para el subject; se reintentara",
				zap.String("subject", b.subject), zap.Error(err))
			still = append(still, b)
			continue
		}
		w.mu.Lock()
		if w.stopped {
			w.mu.Unlock()
			_ = sub.Drain()
			return nil
		}
		w.subs = append(w.subs, sub)
		w.mu.Unlock()
		w.logger.Info("billing: suscrito", zap.String("subject", b.subject))
	}
	return still
}

func (w *IngestWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	for _, s := range w.subs {
		_ = s.Drain()
	}
	w.subs = nil
}

func (w *IngestWorker) onTenantCreated(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	tenantID, ok := w.tenantOf(evt, SubjectTenantCreated, str(data["tenant_id"]))
	if !ok {
		ack()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	w.finish(SubjectTenantCreated, w.uc.OnTenantCreated(ctx, evt.ID, SubjectTenantCreated, tenantID), ack)
}

func (w *IngestWorker) onTenantStatusChanged(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	tenantID, ok := w.tenantOf(evt, SubjectTenantStatusChanged, str(data["tenant_id"]))
	if !ok {
		ack()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	err := w.uc.OnTenantStatusChanged(ctx, evt.ID, SubjectTenantStatusChanged, tenantID, str(data["status"]))
	w.finish(SubjectTenantStatusChanged, err, ack)
}

func (w *IngestWorker) onMailboxCreated(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectMailboxCreated, str(data["tenant_id"]), domain.ResourceMailboxes, 1, "", "")
}

func (w *IngestWorker) onMailboxDeleted(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectMailboxDeleted, str(data["tenant_id"]), domain.ResourceMailboxes, -1, "", "")
}

func (w *IngestWorker) onMailDomainCreated(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectMailDomainCreated, str(data["tenant_id"]), domain.ResourceDomains, 1,
		domainKey(str(data["domain"])), sourceMailDirectory)
}

func (w *IngestWorker) onMailDomainDeleted(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectMailDomainDeleted, str(data["tenant_id"]), domain.ResourceDomains, -1,
		domainKey(str(data["domain"])), sourceMailDirectory)
}

func (w *IngestWorker) onDomainCreated(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectDomainCreated, str(data["tenant_id"]), domain.ResourceDomains, 1,
		domainKey(str(data["domain"])), sourceDomainService)
}

func (w *IngestWorker) onDomainDeleted(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectDomainDeleted, str(data["tenant_id"]), domain.ResourceDomains, -1,
		domainKey(str(data["domain"])), sourceDomainService)
}

// onEmailSent cuenta un mensaje por evento: transactional publica uno por mensaje enviado.
func (w *IngestWorker) onEmailSent(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectEmailSent, str(data["tenant_id"]), domain.ResourceTransactionalMessages, 1, "", "")
}

func (w *IngestWorker) onContactCreated(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectContactCreated, str(data["tenant_id"]), domain.ResourceContacts, 1, "", "")
}

func (w *IngestWorker) onContactDeleted(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]interface{})
	w.count(evt, ack, SubjectContactDeleted, str(data["tenant_id"]), domain.ResourceContacts, -1, "", "")
}

func (w *IngestWorker) count(evt events.Event, ack func(), subject, rawTenant string, resource domain.Resource, delta int64, itemKey, source string) {
	tenantID, ok := w.tenantOf(evt, subject, rawTenant)
	if !ok {
		ack()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	_, err := w.uc.RecordUsage(ctx, domain.UsageChange{
		EventID: evt.ID, Subject: subject, TenantID: tenantID, Resource: resource,
		Delta: delta, ItemKey: itemKey, Source: source,
	})
	w.finish(subject, err, ack)
}

// tenantOf lee la empresa del payload y, si falta, la del envelope. Un evento sin empresa
// no se puede atribuir: se descarta.
func (w *IngestWorker) tenantOf(evt events.Event, subject, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		id, err = uuid.Parse(evt.TenantID)
	}
	if err != nil || id == uuid.Nil {
		w.logger.Error("billing: evento sin tenant_id; se descarta",
			zap.String("subject", subject), zap.String("event_id", evt.ID))
		return uuid.Nil, false
	}
	return id, true
}

// finish acka lo procesado y lo que nunca va a poder procesarse; lo transitorio queda sin
// ack para que JetStream lo reentregue (y, agotado, lo deje en la DLQ).
func (w *IngestWorker) finish(subject string, err error, ack func()) {
	if err == nil {
		ack()
		return
	}
	if app.IsPermanent(err) {
		w.logger.Error("billing: evento invalido; se descarta", zap.String("subject", subject), zap.Error(err))
		ack()
		return
	}
	w.logger.Warn("billing: no se pudo procesar el evento; se reintentara", zap.String("subject", subject), zap.Error(err))
}

// domainKey normaliza el nombre de dominio con el que se identifica entre fuentes.
func domainKey(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}
