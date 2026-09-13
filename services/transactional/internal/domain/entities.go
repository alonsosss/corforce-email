package domain

import (
	"time"

	"github.com/google/uuid"
)

// Estados de un mensaje. accepted espera su hora programada; queued esta en la cola
// durable; sent lo acepto SES; delivered, bounced, complained y rejected los fija la
// ingesta de eventos de SES; failed es un rechazo definitivo del proveedor o el
// agotamiento de reintentos; suppressed nunca se encolo porque todos sus destinatarios
// estaban en la lista de supresion.
const (
	StatusAccepted   = "accepted"
	StatusQueued     = "queued"
	StatusSent       = "sent"
	StatusDelivered  = "delivered"
	StatusBounced    = "bounced"
	StatusComplained = "complained"
	StatusRejected   = "rejected"
	StatusFailed     = "failed"
	StatusSuppressed = "suppressed"
)

// Statuses es la lista cerrada de estados, en el mismo orden que el CHECK de la tabla.
var Statuses = []string{
	StatusAccepted, StatusQueued, StatusSent, StatusDelivered, StatusBounced,
	StatusComplained, StatusRejected, StatusFailed, StatusSuppressed,
}

// Tipos de evento: send lo registra el propio servicio al aceptar SES la peticion; el
// resto llegan por SNS con el eventType de SES ya normalizado.
const (
	EventSend             = "send"
	EventDelivery         = "delivery"
	EventBounce           = "bounce"
	EventComplaint        = "complaint"
	EventReject           = "reject"
	EventDeliveryDelay    = "delivery_delay"
	EventOpen             = "open"
	EventClick            = "click"
	EventRenderingFailure = "rendering_failure"
	EventSubscription     = "subscription"
)

// Tipos de rebote segun SES.
const (
	BounceTypePermanent = "permanent"
	BounceTypeTransient = "transient"
)

// Estados y propositos de un dominio de envio, tal como los publica domain-service.
const (
	DomainStatusVerified = "verified"
	DomainStatusFailed   = "failed"

	DomainPurposeSending = "sending"
	DomainPurposeBoth    = "both"
)

// Limites de una peticion de envio.
const (
	MaxRecipients = 50
	// MaxBodyBytes es el tope del cuerpo total (html + texto): el limite de SES v2 sin
	// adjuntos. Los adjuntos no se admiten en esta fase.
	MaxBodyBytes = 10 << 20
	MaxHeaders   = 10
	MaxTags      = 10
)

// Recipient es una direccion con nombre opcional.
type Recipient struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type Message struct {
	ID              uuid.UUID         `json:"id"`
	TenantID        uuid.UUID         `json:"tenant_id"`
	SubmissionID    *uuid.UUID        `json:"submission_id,omitempty"`
	IdempotencyKey  *string           `json:"idempotency_key,omitempty"`
	FromEmail       string            `json:"from_email"`
	FromName        string            `json:"from_name"`
	ReplyTo         *string           `json:"reply_to,omitempty"`
	To              []Recipient       `json:"to"`
	Cc              []Recipient       `json:"cc"`
	Bcc             []Recipient       `json:"bcc"`
	Subject         string            `json:"subject"`
	TemplateID      *uuid.UUID        `json:"template_id,omitempty"`
	TemplateVersion *int              `json:"template_version,omitempty"`
	Variables       map[string]any    `json:"variables"`
	HTML            *string           `json:"html,omitempty"`
	Text            *string           `json:"text,omitempty"`
	Headers         map[string]string `json:"headers"`
	Tags            map[string]string `json:"tags"`
	Unsubscribable  bool              `json:"unsubscribable"`
	Status          string            `json:"status"`
	SESMessageID    *string           `json:"ses_message_id,omitempty"`
	Error           *string           `json:"error,omitempty"`
	Attempts        int               `json:"attempts"`
	ScheduledAt     *time.Time        `json:"scheduled_at,omitempty"`
	SentAt          *time.Time        `json:"sent_at,omitempty"`
	CreatedBy       *uuid.UUID        `json:"created_by,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// AllRecipients devuelve las direcciones de to, cc y bcc en ese orden.
func (m *Message) AllRecipients() []string {
	out := make([]string, 0, len(m.To)+len(m.Cc)+len(m.Bcc))
	for _, list := range [][]Recipient{m.To, m.Cc, m.Bcc} {
		for _, r := range list {
			out = append(out, r.Email)
		}
	}
	return out
}

type Event struct {
	ID           uuid.UUID      `json:"id"`
	TenantID     uuid.UUID      `json:"tenant_id"`
	MessageID    uuid.UUID      `json:"message_id"`
	Type         string         `json:"type"`
	Recipient    string         `json:"recipient"`
	Detail       map[string]any `json:"detail"`
	SNSMessageID *string        `json:"sns_message_id,omitempty"`
	OccurredAt   time.Time      `json:"occurred_at"`
	CreatedAt    time.Time      `json:"created_at"`
}

// Submission es una peticion de envio con clave de idempotencia y los mensajes que creo.
type Submission struct {
	ID             uuid.UUID   `json:"id"`
	TenantID       uuid.UUID   `json:"tenant_id"`
	IdempotencyKey string      `json:"idempotency_key"`
	MessageIDs     []uuid.UUID `json:"message_ids"`
	CreatedAt      time.Time   `json:"created_at"`
}

// SendingDomain es la proyeccion local de un dominio publicado por domain-service.
type SendingDomain struct {
	TenantID  uuid.UUID `json:"tenant_id"`
	Domain    string    `json:"domain"`
	Status    string    `json:"status"`
	Purpose   string    `json:"purpose"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CanSend dice si el dominio autoriza a enviar por SES.
func (d *SendingDomain) CanSend() bool {
	if d == nil || d.Status != DomainStatusVerified {
		return false
	}
	return d.Purpose == DomainPurposeSending || d.Purpose == DomainPurposeBoth
}

type Unsubscribe struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Email     string    `json:"email"`
	MessageID uuid.UUID `json:"message_id"`
	CreatedAt time.Time `json:"created_at"`
}

// MessageFilter acota un listado.
type MessageFilter struct {
	Status   string
	To       string
	From     string
	DateFrom *time.Time
	DateTo   *time.Time
}

// StatusCount es una fila de las estadisticas por estado.
type StatusCount struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}
