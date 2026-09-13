// Package outbox publica los eventos de contacts con garantia transaccional: cada
// evento se encola en platform.event_outbox dentro de la transaccion que produce el
// cambio, y el rele de pkg/outbox (RunForTenants en main) lo entrega al stream CONTACTS.
//
// Ningun payload lleva los atributos del contacto; la direccion solo viaja donde el
// consumidor la necesita (resuscripcion para suppression, peticion de doble opt-in para
// automations, que recibe tambien el nombre de pila) y nunca en el evento de borrado.
package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// Subjects propios; el stream CONTACTS que los cubre se declara en main.
const (
	SubjectContactCreated      = "contacts.contact.created"
	SubjectContactUpdated      = "contacts.contact.updated"
	SubjectContactDeleted      = "contacts.contact.deleted"
	SubjectContactResubscribed = "contacts.contact.resubscribed"
	SubjectConsentGranted      = "contacts.consent.granted"
	SubjectConsentRevoked      = "contacts.consent.revoked"
	SubjectConsentRequested    = "contacts.consent.requested"
	SubjectImportCompleted     = "contacts.import.completed"
	source                     = "contacts"
)

type Publisher struct {
	q outbox.Execer
}

// NewPublisher recibe el db.ContextPool: Enqueue escribe por la transaccion del contexto.
func NewPublisher(q outbox.Execer) *Publisher { return &Publisher{q: q} }

// enqueue arma el envelope. UserID lleva a quien actuo cuando hay una persona detras.
func (p *Publisher) enqueue(ctx context.Context, subject string, tenantID uuid.UUID, data map[string]interface{}) error {
	data["tenant_id"] = tenantID.String()
	return outbox.Enqueue(ctx, p.q, subject, events.Event{
		Type:     subject,
		Source:   source,
		TenantID: tenantID.String(),
		UserID:   middleware.GetUserID(ctx),
		Data:     data,
	})
}

func (p *Publisher) ContactCreated(ctx context.Context, c *domain.Contact) error {
	return p.enqueue(ctx, SubjectContactCreated, c.TenantID, map[string]interface{}{
		"contact_id": c.ID.String(),
		"source":     string(c.Source),
	})
}

// ContactUpdated lleva los NOMBRES de los campos que cambiaron (attributes.<clave> por
// atributo), no sus valores.
func (p *Publisher) ContactUpdated(ctx context.Context, c *domain.Contact, changed []string) error {
	return p.enqueue(ctx, SubjectContactUpdated, c.TenantID, map[string]interface{}{
		"contact_id": c.ID.String(),
		"changed":    changed,
		"status":     string(c.Status),
	})
}

func (p *Publisher) ContactDeleted(ctx context.Context, tenantID, contactID uuid.UUID) error {
	return p.enqueue(ctx, SubjectContactDeleted, tenantID, map[string]interface{}{
		"contact_id": contactID.String(),
	})
}

// ContactResubscribed es el contrato que lee suppression (IngestWorker.resubscribe):
// {tenant_id, email, consented_at}. consented_at es el occurred_at del consentimiento que
// reactivo el contacto, en RFC 3339 con fraccion y en UTC: suppression solo levanta la
// baja registrada antes de esa hora, asi que una reentrega tardia no retira una baja
// posterior. Sin la hora el consumidor levantaria cualquier baja, por eso una hora cero
// es un error y no un evento. No se anade nada mas: el consumidor solo levanta la baja de
// esa direccion.
func (p *Publisher) ContactResubscribed(ctx context.Context, c *domain.Contact, consentedAt time.Time) error {
	if consentedAt.IsZero() {
		return errors.New("contacts: resuscripcion sin la hora del consentimiento")
	}
	return p.enqueue(ctx, SubjectContactResubscribed, c.TenantID, map[string]interface{}{
		"email":        c.Email,
		"consented_at": consentedAt.UTC().Format(time.RFC3339Nano),
	})
}

func (p *Publisher) ConsentGranted(ctx context.Context, c *domain.Consent) error {
	return p.enqueue(ctx, SubjectConsentGranted, c.TenantID, consentData(c))
}

func (p *Publisher) ConsentRevoked(ctx context.Context, c *domain.Consent) error {
	return p.enqueue(ctx, SubjectConsentRevoked, c.TenantID, consentData(c))
}

// ConsentRequested lleva el enlace de confirmacion para que automations envie el correo
// del doble opt-in, y el nombre de pila para saludar. Es el unico sitio por el que viaja
// el token en claro; en la base solo queda su sha256.
func (p *Publisher) ConsentRequested(ctx context.Context, c *domain.Contact, confirmURL string) error {
	return p.enqueue(ctx, SubjectConsentRequested, c.TenantID, map[string]interface{}{
		"contact_id":  c.ID.String(),
		"email":       c.Email,
		"first_name":  c.FirstName,
		"confirm_url": confirmURL,
	})
}

func (p *Publisher) ImportCompleted(ctx context.Context, imp *domain.Import) error {
	data := map[string]interface{}{
		"import_id": imp.ID.String(),
		"total":     imp.Total,
		"created":   imp.Created,
		"updated":   imp.Updated,
		"skipped":   imp.Skipped,
	}
	if imp.ListID != nil {
		data["list_id"] = imp.ListID.String()
	}
	return p.enqueue(ctx, SubjectImportCompleted, imp.TenantID, data)
}

func consentData(c *domain.Consent) map[string]interface{} {
	return map[string]interface{}{
		"contact_id": c.ContactID.String(),
		"consent_id": c.ID.String(),
		"purpose":    c.Purpose,
		"method":     string(c.Method),
	}
}
