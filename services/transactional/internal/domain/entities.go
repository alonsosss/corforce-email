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

// Clases de envio. Cada una sale por su carril (cola, configuration set y tasa propios) y
// reputation la juzga por separado: la practica de una nunca frena a la otra.
const (
	ClassTransactional = "transactional"
	ClassMarketing     = "marketing"
)

// Tipos de plantilla que informa templates en su render interno. Cada via solo acepta el
// suyo: una campana no sale por el carril transaccional ni un transaccional por el de
// marketing.
const (
	TemplateKindTransactional = "transactional"
	TemplateKindMarketing     = "marketing"
)

// ClassOrDefault aplica el contrato heredado: un mensaje o una peticion sin clase es
// transaccional.
func ClassOrDefault(class string) string {
	if class == "" {
		return ClassTransactional
	}
	return class
}

// ValidClass dice si la clase es una de las conocidas.
func ValidClass(class string) bool {
	return class == ClassTransactional || class == ClassMarketing
}

// Limites de una peticion de envio.
const (
	// MaxBatchRecipients acota un lote de campana: un destinatario es un mensaje, un
	// render y una fila en la misma transaccion.
	MaxBatchRecipients = 500
	MaxRecipients      = 50
	// MaxBodyBytes es el tope del cuerpo total (html + texto): el limite de SES v2 sin
	// adjuntos. Los adjuntos no se admiten en esta fase.
	MaxBodyBytes = 10 << 20
	// MaxRawBytes es el tope de un mensaje MIME de SMTP: el de SES v2 con adjuntos.
	MaxRawBytes = 40 << 20
	MaxHeaders  = 10
	MaxTags     = 10
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
	Class           string            `json:"class"`
	CampaignID      *uuid.UUID        `json:"campaign_id,omitempty"`
	ContactID       *uuid.UUID        `json:"contact_id,omitempty"`
	Status          string            `json:"status"`
	SESMessageID    *string           `json:"ses_message_id,omitempty"`
	Error           *string           `json:"error,omitempty"`
	Attempts        int               `json:"attempts"`
	ScheduledAt     *time.Time        `json:"scheduled_at,omitempty"`
	SentAt          *time.Time        `json:"sent_at,omitempty"`
	CreatedBy       *uuid.UUID        `json:"created_by,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	// Test: envio de prueba de una campana (ver IsTestSend). Viaja en todos los eventos
	// transactional.email.* para que la analitica no lo cuente.
	Test bool `json:"test"`
	// Origin es por donde entro el mensaje (OriginAPI u OriginSMTP); uno de SMTP sale con su
	// MIME guardado aparte (RawContent) y no con los cuerpos de la fila.
	Origin string `json:"origin"`
	// APIKeyID es la clave de API con la que se creo, si la hubo.
	APIKeyID *uuid.UUID `json:"api_key_id,omitempty"`
}

// Origenes de un mensaje.
const (
	OriginAPI  = "api"
	OriginSMTP = "smtp"
)

// OriginOrDefault: una fila anterior a la columna es del API.
func OriginOrDefault(origin string) string {
	if origin == "" {
		return OriginAPI
	}
	return origin
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

// Attribution devuelve la clase, la campana, el contacto y la marca de prueba del mensaje.
func (m *Message) Attribution() MessageAttribution {
	return MessageAttribution{Class: ClassOrDefault(m.Class), CampaignID: m.CampaignID, ContactID: m.ContactID, Test: m.Test}
}

// MessageAttribution es lo que viaja en todos los eventos transactional.email.*:
// reputation lee la clase; campaigns y analytics, la campana y el contacto; analytics
// descarta lo marcado como prueba.
type MessageAttribution struct {
	Class      string
	CampaignID *uuid.UUID
	ContactID  *uuid.UUID
	Test       bool
}

// Marca de envio de prueba. campaigns manda sus pruebas por el lote interno
// (POST /internal/transactional/batch) con esta etiqueta, y solo ese carril la interpreta.
// En el API publico es una etiqueta mas de SES (la empresa puede usarla para lo suyo) y no
// marca nada: una empresa no puede sacar envios reales de la analitica etiquetandolos.
const (
	TestSendTag   = "test"
	TestSendValue = "true"
)

// IsTestSend dice si las etiquetas de un lote interno lo marcan como envio de prueba.
func IsTestSend(tags map[string]string) bool {
	return tags[TestSendTag] == TestSendValue
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

// Submission es una peticion de envio con clave de idempotencia, su clase, los mensajes
// que creo y los destinatarios que la supresion retiro (una repeticion devuelve la misma
// respuesta).
type Submission struct {
	ID             uuid.UUID             `json:"id"`
	TenantID       uuid.UUID             `json:"tenant_id"`
	IdempotencyKey string                `json:"idempotency_key"`
	Class          string                `json:"class"`
	MessageIDs     []uuid.UUID           `json:"message_ids"`
	Suppressed     []SuppressedRecipient `json:"suppressed"`
	CreatedAt      time.Time             `json:"created_at"`
}

// SuppressedRecipient es una direccion que la lista de supresion rechazo. Reason es su
// causa principal (la vigente mas grave); Reasons, todas sus causas vigentes tal como las
// devuelve suppression. Una respuesta guardada antes de existir Reasons no lo lleva.
type SuppressedRecipient struct {
	Email   string   `json:"email"`
	Reason  string   `json:"reason"`
	Reasons []string `json:"reasons,omitempty"`
}

// SendingDomain es la proyeccion local de un dominio publicado por domain-service.
type SendingDomain struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Domain   string    `json:"domain"`
	Status   string    `json:"status"`
	Purpose  string    `json:"purpose"`
	// SendingReady es si Amazon SES acepta ya envios del dominio, cuando domain-service gestiona su
	// identidad en SES; nil si no lo ha dicho.
	SendingReady *bool     `json:"sending_ready"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CanSend dice si el dominio autoriza a enviar por SES: verificado, de envio y, si domain-service
// gestiona su identidad en SES, verificado tambien alli.
func (d *SendingDomain) CanSend() bool {
	if d == nil || d.Status != DomainStatusVerified {
		return false
	}
	if d.SendingReady != nil && !*d.SendingReady {
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
	Class    string
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
