package domain

import (
	"time"

	"github.com/google/uuid"
)

// Reason es la causa por la que una direccion queda fuera de todo envio.
type Reason string

const (
	ReasonHardBounce  Reason = "hard_bounce"
	ReasonComplaint   Reason = "complaint"
	ReasonUnsubscribe Reason = "unsubscribe"
	ReasonInvalid     Reason = "invalid"
	ReasonManual      Reason = "manual"
)

// Reasons enumera las causas en orden de gravedad descendente.
func Reasons() []Reason {
	return []Reason{ReasonComplaint, ReasonHardBounce, ReasonUnsubscribe, ReasonInvalid, ReasonManual}
}

// ParseReason valida el texto recibido por API o evento.
func ParseReason(s string) (Reason, error) {
	r := Reason(s)
	for _, known := range Reasons() {
		if r == known {
			return r, nil
		}
	}
	return "", ErrInvalidReason
}

// Severity ordena las causas: una direccion registrada por una causa grave no se
// degrada cuando llega la misma direccion por una causa menor. Una queja pesa mas que
// un rebote duro porque afecta a la reputacion de envio; la baja pesa mas que el resto
// porque la pidio la persona; lo manual es lo unico que un operador puede revertir a
// voluntad.
func (r Reason) Severity() int {
	switch r {
	case ReasonComplaint:
		return 5
	case ReasonHardBounce:
		return 4
	case ReasonUnsubscribe:
		return 3
	case ReasonInvalid:
		return 2
	case ReasonManual:
		return 1
	}
	return 0
}

// Removable indica si un operador puede retirar la exclusion por API. La baja pedida por
// la persona solo la levanta un nuevo consentimiento registrado por contacts.
func (r Reason) Removable() bool {
	return r != ReasonUnsubscribe
}

// Entry es una direccion excluida de envio en una empresa.
type Entry struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	Email      string     `json:"email"`
	Reason     Reason     `json:"reason"`
	Source     string     `json:"source"`
	Detail     string     `json:"detail"`
	MessageID  *uuid.UUID `json:"message_id,omitempty"`
	CampaignID *uuid.UUID `json:"campaign_id,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Active indica si la exclusion sigue vigente en el instante dado. Solo las manuales
// pueden caducar; el resto no lleva fecha.
func (e Entry) Active(now time.Time) bool {
	return e.ExpiresAt == nil || e.ExpiresAt.After(now)
}

// Import es el rastro de una carga masiva de exclusiones.
type Import struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Total     int       `json:"total"`
	Added     int       `json:"added"`
	Skipped   int       `json:"skipped"`
	CreatedBy uuid.UUID `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Suppressed es la respuesta de la consulta previa al envio para una direccion.
type Suppressed struct {
	Email  string `json:"email"`
	Reason Reason `json:"reason"`
}

// ReasonCount es el numero de exclusiones vigentes por causa.
type ReasonCount struct {
	Reason Reason
	Count  int64
}
