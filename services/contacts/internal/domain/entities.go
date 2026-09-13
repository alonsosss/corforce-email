package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Status es el estado de entrega del contacto. Lo cambian las causas de exclusion que
// registra suppression (bajas, rebotes, quejas, direcciones no validas y exclusiones
// manuales) y el nuevo consentimiento explicito; nunca un PATCH.
type Status string

const (
	StatusActive       Status = "active"
	StatusUnsubscribed Status = "unsubscribed"
	StatusBounced      Status = "bounced"
	StatusComplained   Status = "complained"
	StatusInvalid      Status = "invalid"
	StatusExcluded     Status = "excluded"
)

func Statuses() []Status {
	return []Status{StatusActive, StatusUnsubscribed, StatusBounced, StatusComplained, StatusInvalid, StatusExcluded}
}

func ParseStatus(s string) (Status, error) {
	for _, st := range Statuses() {
		if Status(s) == st {
			return st, nil
		}
	}
	return "", ErrInvalidStatus
}

// Severity ordena los estados como suppression ordena sus causas (una sola escala, que un
// test contrasta con la suya): queja, rebote duro, baja, direccion no valida y exclusion
// manual. Con varias causas vigentes el estado es el de la mas grave. La baja pesa mas que
// invalid y excluded porque es lo unico que no levanta un operador: mostrarla dice que hace
// falta para que la persona vuelva. invalid pesa mas que excluded porque es un hecho de la
// direccion, y excluded una decision de la empresa que puede caducar.
func (s Status) Severity() int {
	switch s {
	case StatusComplained:
		return 5
	case StatusBounced:
		return 4
	case StatusUnsubscribed:
		return 3
	case StatusInvalid:
		return 2
	case StatusExcluded:
		return 1
	}
	return 0
}

// StatusLift dice que devuelve a un contacto excluido a su estado anterior.
type StatusLift string

const (
	// LiftReconsent: solo un consentimiento nuevo con prueba de que lo pidio la persona.
	LiftReconsent StatusLift = "reconsent"
	// LiftOperator: que un operador retire la causa en suppression.
	LiftOperator StatusLift = "operator"
	// LiftOperatorOrExpiry: que un operador la retire o que caduque.
	LiftOperatorOrExpiry StatusLift = "operator_or_expiry"
)

// LiftedBy es lo que levanta el estado; vacio en active, que no hay que levantar.
func (s Status) LiftedBy() StatusLift {
	switch s {
	case StatusUnsubscribed:
		return LiftReconsent
	case StatusComplained, StatusBounced, StatusInvalid:
		return LiftOperator
	case StatusExcluded:
		return LiftOperatorOrExpiry
	}
	return ""
}

// BlocksAllMail indica si la causa del estado excluye a la direccion de todo envio,
// tambien del doble opt-in (que solo pasa por encima de una baja), y si el estado lo
// levanta la retirada de esa causa y no un consentimiento.
func (s Status) BlocksAllMail() bool {
	lift := s.LiftedBy()
	return lift == LiftOperator || lift == LiftOperatorOrExpiry
}

// Source dice por donde entro el contacto.
type Source string

const (
	SourceAPI         Source = "api"
	SourceImport      Source = "import"
	SourceForm        Source = "form"
	SourceIntegration Source = "integration"
)

func Sources() []Source {
	return []Source{SourceAPI, SourceImport, SourceForm, SourceIntegration}
}

// AttrType es el tipo declarado de un atributo.
type AttrType string

const (
	AttrString  AttrType = "string"
	AttrNumber  AttrType = "number"
	AttrBoolean AttrType = "boolean"
	AttrDate    AttrType = "date"
)

func AttrTypes() []AttrType {
	return []AttrType{AttrString, AttrNumber, AttrBoolean, AttrDate}
}

// AttributeDefinition es un atributo que la empresa declara para sus contactos.
type AttributeDefinition struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Key       string    `json:"key"`
	Type      AttrType  `json:"type"`
	Label     string    `json:"label"`
	Required  bool      `json:"required"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Contact es una persona de la audiencia de la empresa. Attributes guarda valores ya
// validados contra las definiciones: string, json.Number o bool (las fechas, como
// string AAAA-MM-DD).
type Contact struct {
	ID         uuid.UUID      `json:"id"`
	TenantID   uuid.UUID      `json:"tenant_id"`
	Email      string         `json:"email"`
	FirstName  string         `json:"first_name"`
	LastName   string         `json:"last_name"`
	Locale     *string        `json:"locale"`
	Timezone   *string        `json:"timezone"`
	Attributes map[string]any `json:"attributes"`
	Tags       []string       `json:"tags"`
	Status     Status         `json:"status"`
	// ConsentStatus es el consentimiento vigente de marketing (none si nunca hubo uno).
	ConsentStatus ConsentStatus `json:"consent_status"`
	Source        Source        `json:"source"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// Sendable indica si el contacto puede recibir marketing: activo y con consentimiento
// vigente concedido.
func (c *Contact) Sendable() bool {
	return c.Status == StatusActive && c.ConsentStatus == ConsentGranted
}

// ConsentStatus es el estado de una fila de consentimiento. ConsentNone solo existe como
// proyeccion: un contacto sin ninguna fila.
type ConsentStatus string

const (
	ConsentGranted ConsentStatus = "granted"
	ConsentRevoked ConsentStatus = "revoked"
	ConsentPending ConsentStatus = "pending"
	ConsentNone    ConsentStatus = "none"
)

// ConsentStatuses son los estados de consentimiento, proyeccion none incluida.
func ConsentStatuses() []ConsentStatus {
	return []ConsentStatus{ConsentGranted, ConsentRevoked, ConsentPending, ConsentNone}
}

// ConsentMethod es como se obtuvo o retiro el consentimiento.
type ConsentMethod string

const (
	MethodForm            ConsentMethod = "form"
	MethodImport          ConsentMethod = "import"
	MethodAPI             ConsentMethod = "api"
	MethodDoubleOptIn     ConsentMethod = "double_opt_in"
	MethodUnsubscribeLink ConsentMethod = "unsubscribe_link"
	MethodSuppression     ConsentMethod = "suppression"
)

// ConsentMethods son todos los metodos con que se registra un consentimiento.
func ConsentMethods() []ConsentMethod {
	return []ConsentMethod{MethodForm, MethodImport, MethodAPI, MethodDoubleOptIn, MethodUnsubscribeLink, MethodSuppression}
}

// PurposeMarketing es, por ahora, el unico proposito de consentimiento.
const PurposeMarketing = "marketing"

// Consent es una fila de evidencia. No se modifica: un cambio de voluntad es otra fila.
type Consent struct {
	ID         uuid.UUID      `json:"id"`
	TenantID   uuid.UUID      `json:"tenant_id"`
	ContactID  uuid.UUID      `json:"contact_id"`
	Purpose    string         `json:"purpose"`
	Status     ConsentStatus  `json:"status"`
	Method     ConsentMethod  `json:"method"`
	Source     string         `json:"source"`
	IP         *string        `json:"ip"`
	UserAgent  *string        `json:"user_agent"`
	Evidence   map[string]any `json:"evidence"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// ConfirmationToken es la huella de un enlace de doble opt-in.
type ConfirmationToken struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	ContactID uuid.UUID
	TokenHash string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// Usable indica si el enlace aun confirma: no usado y no caducado.
func (t *ConfirmationToken) Usable(now time.Time) bool {
	return t.UsedAt == nil && now.Before(t.ExpiresAt)
}

// List es una lista estatica de contactos.
type List struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	MemberCount int64     `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Segment es un filtro dinamico sobre los contactos. Definition es el DSL validado
// (internal/segment) en su forma canonica.
type Segment struct {
	ID          uuid.UUID       `json:"id"`
	TenantID    uuid.UUID       `json:"tenant_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Definition  json.RawMessage `json:"definition"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ImportStatus es el resultado de una importacion.
type ImportStatus string

const (
	ImportCompleted ImportStatus = "completed"
	ImportFailed    ImportStatus = "failed"
)

// ImportError es una fila rechazada: su numero (1 = primera fila) y el motivo.
type ImportError struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// Import es el rastro de una importacion.
type Import struct {
	ID       uuid.UUID     `json:"id"`
	TenantID uuid.UUID     `json:"tenant_id"`
	Status   ImportStatus  `json:"status"`
	Total    int           `json:"total"`
	Created  int           `json:"created"`
	Updated  int           `json:"updated"`
	Skipped  int           `json:"skipped"`
	Errors   []ImportError `json:"errors"`
	// Suppressed cuenta, por estado, los contactos creados que entraron ya excluidos por una
	// causa vigente en suppression (AdmitSuppression); sin los active. Esos nunca reciben el
	// consentimiento de la importacion (ImportMayGrant).
	Suppressed   map[Status]int `json:"suppressed"`
	ConsentBasis string         `json:"consent_basis"`
	ListID       *uuid.UUID     `json:"list_id"`
	CreatedBy    uuid.UUID      `json:"created_by"`
	CreatedAt    time.Time      `json:"created_at"`
}
