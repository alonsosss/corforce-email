package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MaxSendAttempts acota los reintentos por errores transitorios del proveedor. Es el
// presupuesto por mensaje, no por entrega de la cola: un throttling que persiste mas de
// esto no es transitorio y el mensaje pasa a failed con el motivo.
const MaxSendAttempts = 8

// OutgoingEmail es lo que se le pide al proveedor, ya resuelto: cuerpo final, cabeceras
// propias y las de baja cuando corresponde.
type OutgoingEmail struct {
	MessageID      uuid.UUID
	TenantID       uuid.UUID
	From           string // "Nombre <email>"
	ReplyTo        []string
	To             []string
	Cc             []string
	Bcc            []string
	Subject        string
	HTML           string
	Text           string
	Headers        map[string]string
	Tags           map[string]string
	UnsubscribeURL string // vacio cuando el mensaje no es dable de baja
	// Raw es el MIME completo de un mensaje de SMTP: sale tal cual (SES contenido Raw) a los
	// destinatarios de To, que entonces son el sobre y no la cabecera.
	Raw []byte
}

// ErrorKind clasifica un fallo del proveedor: transitorio se reintenta con backoff,
// permanente marca el mensaje como failed.
type ErrorKind int

const (
	ErrorTransient ErrorKind = iota
	ErrorPermanent
)

func (k ErrorKind) String() string {
	if k == ErrorPermanent {
		return "permanent"
	}
	return "transient"
}

// SendError es el fallo clasificado que devuelve el adaptador del proveedor.
type SendError struct {
	Kind    ErrorKind
	Code    string
	Message string
}

func (e *SendError) Error() string {
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Kind, e.Message)
}

// InboundEvent es un evento de SES ya extraido de la notificacion SNS.
type InboundEvent struct {
	Type         string // uno de los Event*
	TenantID     uuid.UUID
	MessageID    uuid.UUID
	Recipients   []string
	BounceType   string // permanent | transient; solo en bounce
	OccurredAt   time.Time
	Detail       map[string]any
	SNSMessageID string
}

// StatusForEvent devuelve el estado que fija el evento y desde que estados se aplica.
// Los eventos pueden llegar desordenados (un Delivery despues de un Bounce del mismo
// mensaje con varios destinatarios): un estado terminal negativo nunca retrocede.
func StatusForEvent(eventType string) (newStatus string, from []string) {
	switch eventType {
	case EventDelivery:
		return StatusDelivered, []string{StatusQueued, StatusSent}
	case EventBounce:
		return StatusBounced, []string{StatusQueued, StatusSent, StatusDelivered}
	case EventComplaint:
		return StatusComplained, []string{StatusQueued, StatusSent, StatusDelivered, StatusBounced}
	case EventReject:
		return StatusRejected, []string{StatusQueued, StatusSent}
	}
	return "", nil
}

// NormalizeSESEventType traduce el eventType de SES al tipo local; vacio si no se conoce.
func NormalizeSESEventType(sesType string) string {
	switch sesType {
	case "Send":
		return EventSend
	case "Delivery":
		return EventDelivery
	case "Bounce":
		return EventBounce
	case "Complaint":
		return EventComplaint
	case "Reject":
		return EventReject
	case "DeliveryDelay":
		return EventDeliveryDelay
	case "Open":
		return EventOpen
	case "Click":
		return EventClick
	case "RenderingFailure":
		return EventRenderingFailure
	case "Subscription":
		return EventSubscription
	}
	return ""
}
