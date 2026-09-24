package domain

import (
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// DOISettings es la configuracion del correo del doble opt-in de una empresa.
type DOISettings struct {
	TenantID   uuid.UUID  `json:"tenant_id"`
	Enabled    bool       `json:"enabled"`
	TemplateID *uuid.UUID `json:"template_id"`
	FromEmail  string     `json:"from_email"`
	FromName   string     `json:"from_name"`
	ReplyTo    string     `json:"reply_to"`
	UpdatedBy  *uuid.UUID `json:"updated_by,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// Normalize valida y normaliza. Activado exige plantilla y remitente; desactivado admite
// dejarlos a medias, pero lo que se indique debe ser valido.
func (s *DOISettings) Normalize() error {
	if s.TemplateID != nil && *s.TemplateID == uuid.Nil {
		return NewValidationError("template_id no es válido")
	}
	if s.Enabled && s.TemplateID == nil {
		return NewValidationError("template_id es obligatorio para activar el doble opt-in")
	}
	if s.Enabled || strings.TrimSpace(s.FromEmail) != "" {
		if err := normalizeSender("", &s.FromEmail, &s.FromName, &s.ReplyTo); err != nil {
			return err
		}
		return nil
	}
	s.FromEmail = ""
	s.FromName = strings.TrimSpace(s.FromName)
	if utf8.RuneCountInString(s.FromName) > MaxNameLen {
		return NewValidationError("from_name admite como máximo %d caracteres", MaxNameLen)
	}
	s.ReplyTo = NormalizeEmail(s.ReplyTo)
	if s.ReplyTo != "" && !ValidEmail(s.ReplyTo) {
		return NewValidationError("reply_to debe ser un correo válido")
	}
	return nil
}

// Ready dice si con estos ajustes se puede enviar.
func (s *DOISettings) Ready() bool {
	return s != nil && s.Enabled && s.TemplateID != nil && s.FromEmail != ""
}

// DOIStatus: pending (reclamado, en vuelo), sent, skipped (no se envio a proposito) y
// failed (transactional lo rechazo o no respondio tras agotar los intentos).
type DOIStatus string

const (
	DOIPending DOIStatus = "pending"
	DOISent    DOIStatus = "sent"
	DOISkipped DOIStatus = "skipped"
	DOIFailed  DOIStatus = "failed"
)

func DOIStatuses() []DOIStatus { return []DOIStatus{DOIPending, DOISent, DOISkipped, DOIFailed} }

func ParseDOIStatus(s string) (DOIStatus, bool) {
	for _, st := range DOIStatuses() {
		if string(st) == s {
			return st, true
		}
	}
	return "", false
}

// Motivos propios de skipped y failed; los rechazos de transactional se guardan con su
// codigo (SENDING_DOMAIN_NOT_VERIFIED, TEMPLATE_NOT_TRANSACTIONAL...).
const (
	ReasonNotConfigured = "not_configured"
	ReasonDisabled      = "disabled"
	ReasonRateLimited   = "rate_limited"
	ReasonSuppressed    = "SUPPRESSED"
)

// DOIMaxAttempts: entregas del evento sin respuesta util de transactional antes de dar el
// intento por fallido. Queda por debajo del tope de reentregas del bus (20) para que el
// intento termine registrado y no en la cola de mensajes muertos.
const DOIMaxAttempts = 10

type DOIDelivery struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	EventID    string     `json:"event_id"`
	ContactID  uuid.UUID  `json:"contact_id"`
	Status     DOIStatus  `json:"status"`
	Reason     string     `json:"reason"`
	MessageID  *uuid.UUID `json:"message_id"`
	Attempts   int        `json:"attempts"`
	TemplateID *uuid.UUID `json:"template_id"`
	FromEmail  string     `json:"from_email"`
	FromName   string     `json:"-"`
	ReplyTo    string     `json:"-"`
	SentAt     *time.Time `json:"sent_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// DOILimits es el tope anti abuso por contacto: una empresa podria usar el doble opt-in
// para escribir una y otra vez a quien se dio de baja.
type DOILimits struct {
	PerDay    int
	Per30Days int
}

func (l DOILimits) Validate() error {
	if l.PerDay < 1 || l.Per30Days < l.PerDay {
		return NewValidationError("los límites del doble opt-in deben cumplir 1 <= por día <= por 30 días")
	}
	return nil
}

// Allows dice si cabe otro correo con los ya enviados o en vuelo en cada ventana.
func (l DOILimits) Allows(lastDay, last30Days int) bool {
	return lastDay < l.PerDay && last30Days < l.Per30Days
}

// ConsentRequest es el evento contacts.consent.requested ya interpretado.
type ConsentRequest struct {
	EventID    string
	TenantID   uuid.UUID
	ContactID  uuid.UUID
	Email      string
	ConfirmURL string
	FirstName  string
}

const maxEventIDLen = 200

// Validate comprueba lo que hace falta para enviar: sin direccion o sin enlace valido no
// hay correo posible y reintentar no lo arregla.
func (r *ConsentRequest) Validate() error {
	if r.EventID == "" || len(r.EventID) > maxEventIDLen {
		return NewValidationError("evento sin id válido")
	}
	if r.TenantID == uuid.Nil || r.ContactID == uuid.Nil {
		return NewValidationError("evento sin empresa o contacto")
	}
	r.Email = NormalizeEmail(r.Email)
	if !ValidEmail(r.Email) {
		return NewValidationError("evento sin dirección válida")
	}
	u, err := url.Parse(strings.TrimSpace(r.ConfirmURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return NewValidationError("evento sin enlace de confirmación válido")
	}
	r.FirstName = strings.TrimSpace(r.FirstName)
	if utf8.RuneCountInString(r.FirstName) > MaxNameLen {
		r.FirstName = string([]rune(r.FirstName)[:MaxNameLen])
	}
	return nil
}

// NewDOIDelivery registra el intento. pending lleva la foto de los ajustes con la que se
// enviara y reenviara; skipped la guarda si la hay, para el historial.
func NewDOIDelivery(req ConsentRequest, s *DOISettings, status DOIStatus, reason string) *DOIDelivery {
	d := &DOIDelivery{
		ID: uuid.New(), TenantID: req.TenantID, EventID: req.EventID, ContactID: req.ContactID,
		Status: status, Reason: reason,
	}
	if s != nil {
		d.TemplateID, d.FromEmail, d.FromName, d.ReplyTo = s.TemplateID, s.FromEmail, s.FromName, s.ReplyTo
	}
	return d
}

// IdempotencyKey es la clave del envio en transactional: una por evento.
func (d *DOIDelivery) IdempotencyKey() string { return "doi:" + d.EventID }
