package domain

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type BatchStatus string

const (
	BatchPending   BatchStatus = "pending"
	BatchDelivered BatchStatus = "delivered"
	BatchFailed    BatchStatus = "failed"
)

func BatchStatuses() []BatchStatus { return []BatchStatus{BatchPending, BatchDelivered, BatchFailed} }

const (
	// MaxBatchSize es el tope de destinatarios del lote de transactional: un lote de
	// campana es exactamente una peticion a ese lote.
	MaxBatchSize = 500
	// MaxBatchAttempts: fallos transitorios seguidos de un lote antes de pausar la
	// campana para que una persona mire que pasa.
	MaxBatchAttempts = 10

	// CallTimeout acota la llamada a contacts mas la de transactional de un lote.
	CallTimeout = 90 * time.Second
	// BatchLease reserva el lote para quien lo reclamo. Debe superar CallTimeout con
	// margen: cuando vence, el primer trabajador ya abandono su llamada y otro puede
	// reintentar el lote con la misma clave sin solaparse con el.
	BatchLease = 3 * time.Minute

	DefaultRetryAfter = time.Minute
	MaxRetryAfter     = time.Hour

	retryBackoffBase = 30 * time.Second
	maxRetryBackoff  = 15 * time.Minute
)

// Contact es un contacto tal como lo entrega contacts para una audiencia.
type Contact struct {
	ID         uuid.UUID
	Email      string
	FirstName  string
	LastName   string
	Locale     string
	Timezone   string
	Attributes map[string]json.RawMessage
}

// Recipient es un destinatario del lote tal como se envia a transactional y se guarda
// en la pagina del lote. contact_id va siempre (transactional lo exige); en los envios de
// prueba es sintetico (TestContactID).
type Recipient struct {
	Email     string                     `json:"email"`
	Name      string                     `json:"name,omitempty"`
	ContactID *uuid.UUID                 `json:"contact_id,omitempty"`
	Variables map[string]json.RawMessage `json:"variables"`
}

// RecipientFromContact arma el destinatario con sus variables: los atributos del
// contacto y, por encima de ellos, first_name, last_name y email, que no se dejan pisar
// por un atributo con el mismo nombre. ok=false si el contacto no tiene direccion o id:
// transactional rechazaria el lote entero por un solo destinatario sin contact_id.
func RecipientFromContact(c Contact) (Recipient, bool) {
	email := strings.TrimSpace(c.Email)
	if email == "" || c.ID == uuid.Nil {
		return Recipient{}, false
	}
	vars := make(map[string]json.RawMessage, len(c.Attributes)+3)
	for k, v := range c.Attributes {
		vars[k] = v
	}
	vars["first_name"] = jsonString(c.FirstName)
	vars["last_name"] = jsonString(c.LastName)
	vars["email"] = jsonString(email)
	id := c.ID
	return Recipient{
		Email:     email,
		Name:      strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName)),
		ContactID: &id,
		Variables: vars,
	}, true
}

// RecipientsFromContacts arma la pagina de un lote: omite los contactos sin direccion y
// las direcciones repetidas, que harian rechazar el lote entero.
func RecipientsFromContacts(contacts []Contact) []Recipient {
	out := make([]Recipient, 0, len(contacts))
	seen := make(map[string]bool, len(contacts))
	for _, c := range contacts {
		r, ok := RecipientFromContact(c)
		if !ok {
			continue
		}
		key := strings.ToLower(r.Email)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// MaxTestRecipients acota un envio de prueba.
const MaxTestRecipients = 5

// testContactNamespace es el espacio de nombres de los contactos sinteticos de los envios
// de prueba. Una direccion de prueba no es un contacto, pero transactional exige
// contact_id en todo destinatario de marketing: se le da un id derivado (UUIDv5 de la
// campana y la direccion) que el consumidor de estadisticas recalcula para reconocer esos
// mensajes y no contarlos, sin guardar nada ni depender del orden de llegada.
var testContactNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("urn:core-force-mail:campaigns:test-recipient"))

// TestContactID es el contact_id sintetico de una direccion de prueba de la campana.
func TestContactID(campaignID uuid.UUID, email string) uuid.UUID {
	return uuid.NewSHA1(testContactNamespace, []byte(campaignID.String()+":"+strings.ToLower(strings.TrimSpace(email))))
}

// IsTestContact dice si contactID es el sintetico de esa direccion en esa campana.
func IsTestContact(campaignID, contactID uuid.UUID, email string) bool {
	return strings.TrimSpace(email) != "" && contactID == TestContactID(campaignID, email)
}

// NormalizeTestRecipients valida las direcciones de un envio de prueba y quita las
// repetidas.
func NormalizeTestRecipients(emails []string) ([]string, error) {
	if len(emails) == 0 || len(emails) > MaxTestRecipients {
		return nil, NewValidationError("emails: indique entre 1 y %d direcciones", MaxTestRecipients)
	}
	out := make([]string, 0, len(emails))
	seen := make(map[string]bool, len(emails))
	for _, e := range emails {
		e = strings.TrimSpace(e)
		if err := validateAddress("emails", e, true); err != nil {
			return nil, err
		}
		key := strings.ToLower(e)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out, nil
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// Batch es una pagina de la audiencia entregada (o por entregar) a transactional.
type Batch struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	CampaignID uuid.UUID
	// PhaseID es la fase a la que pertenece; nil en un lote anterior a las fases (main).
	PhaseID *uuid.UUID
	Seq     int
	// CursorIn es el cursor con el que se pide la pagina; CursorOut el que contacts
	// devolvio para la siguiente (nil = fin de la audiencia).
	CursorIn  *string
	CursorOut *string
	// Page es la pagina fijada antes del primer envio. Mientras el lote esta pendiente,
	// cada reintento envia exactamente estos destinatarios; al entregarse se vacia.
	Page        []Recipient
	PageFetched bool
	Recipients  int
	Status      BatchStatus
	Accepted    int
	Suppressed  int
	Attempts    int
	LastError   string
	LeasedUntil *time.Time
	LeaseToken  *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NextBatch es el lote que sigue a last (nil para el primero) en una campana sin fases,
// ya reservado para quien lo crea.
func NextBatch(c *Campaign, last *Batch, now time.Time) *Batch {
	return NextPhaseBatch(c, last, nil, now)
}

// NextPhaseBatch es el siguiente lote de la fase p. El numero sigue al del ultimo lote de
// la campana (la clave de idempotencia es unica en toda ella) y el cursor al del ultimo
// lote solo si era de la misma fase: una fase nueva recorre la audiencia desde el
// principio.
func NextPhaseBatch(c *Campaign, last *Batch, p *Phase, now time.Time) *Batch {
	b := &Batch{
		ID:         uuid.New(),
		TenantID:   c.TenantID,
		CampaignID: c.ID,
		Seq:        1,
		Status:     BatchPending,
	}
	if p != nil {
		id := p.ID
		b.PhaseID = &id
	}
	if last != nil {
		b.Seq = last.Seq + 1
		if last.CursorOut != nil && last.InPhase(p) {
			cursor := *last.CursorOut
			b.CursorIn = &cursor
		}
	}
	b.Lease(now)
	return b
}

// InPhase dice si el lote es de la fase p. Un lote sin fase (anterior a ellas) es de la
// principal.
func (b *Batch) InPhase(p *Phase) bool {
	if p == nil {
		return b.PhaseID == nil
	}
	if b.PhaseID == nil {
		return p.Kind == PhaseMain
	}
	return *b.PhaseID == p.ID
}

// Lease reserva el lote para un trabajador hasta now+BatchLease con un testigo nuevo.
func (b *Batch) Lease(now time.Time) {
	until := now.Add(BatchLease)
	token := uuid.New()
	b.LeasedUntil = &until
	b.LeaseToken = &token
}

// Leased dice si otro trabajador lo tiene reservado todavia.
func (b *Batch) Leased(now time.Time) bool {
	return b.LeasedUntil != nil && b.LeasedUntil.After(now)
}

// Exhausted dice si este lote entrego la ultima pagina de la audiencia.
func (b *Batch) Exhausted() bool {
	return b.Status == BatchDelivered && b.CursorOut == nil
}

// IdempotencyKey es la clave con la que el lote se entrega a transactional. Depende
// solo de la campana y del numero de lote: cualquier reintento, de cualquier replica,
// usa la misma.
func (b *Batch) IdempotencyKey() string {
	return BatchIdempotencyKey(b.CampaignID, b.Seq)
}

func BatchIdempotencyKey(campaignID uuid.UUID, seq int) string {
	return "campaign:" + campaignID.String() + ":batch:" + strconv.Itoa(seq)
}

// TestIdempotencyKey es la clave de un envio de prueba: unica por peticion.
func TestIdempotencyKey(campaignID, nonce uuid.UUID) string {
	return "campaign:" + campaignID.String() + ":test:" + nonce.String()
}

// RetryBackoff es la espera antes de reintentar un lote tras su fallo transitorio
// numero attempts: 30 s, 1 min, 2 min... hasta 15 min. Diez intentos cubren algo mas de
// una hora, lo bastante para absorber un despliegue o una caida breve sin pausar.
func RetryBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := retryBackoffBase
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= maxRetryBackoff {
			return maxRetryBackoff
		}
	}
	return d
}

// ClampRetryAfter acota la espera que pide transactional con un 429.
func ClampRetryAfter(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultRetryAfter
	case d > MaxRetryAfter:
		return MaxRetryAfter
	}
	return d
}
