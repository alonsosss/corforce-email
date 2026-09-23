package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Class es la clase de envio. Transaccional y marketing salen por configuration sets
// separados y se miden por separado.
type Class string

const (
	ClassTransactional Class = "transactional"
	ClassMarketing     Class = "marketing"
)

// Classes es la lista cerrada de clases, en el mismo orden que los CHECK de las tablas.
func Classes() []Class { return []Class{ClassTransactional, ClassMarketing} }

func (c Class) Valid() bool { return c == ClassTransactional || c == ClassMarketing }

// ParseClassFilter lee el filtro opcional de clase de una consulta: vacio = todas.
func ParseClassFilter(s string) (Class, error) {
	if s == "" {
		return "", nil
	}
	c := Class(s)
	if !c.Valid() {
		return "", &ValidationError{Field: "class", Message: "debe ser transactional o marketing"}
	}
	return c, nil
}

// EventClass lee la clase de un evento de envio. Un evento sin clase es transaccional:
// la via de marketing de transactional la declara siempre.
func EventClass(s string) (Class, error) {
	if s == "" {
		return ClassTransactional, nil
	}
	c := Class(s)
	if !c.Valid() {
		return "", invalidEvent("clase de envio desconocida %q", s)
	}
	return c, nil
}

// Milestone es un hito del ciclo de vida de un mensaje que se contabiliza.
type Milestone string

const (
	MilestoneSent         Milestone = "sent"
	MilestoneDelivered    Milestone = "delivered"
	MilestoneBounced      Milestone = "bounced"
	MilestoneComplained   Milestone = "complained"
	MilestoneOpened       Milestone = "opened"
	MilestoneClicked      Milestone = "clicked"
	MilestoneUnsubscribed Milestone = "unsubscribed"
	MilestoneFailed       Milestone = "failed"
)

// MilestoneFromAction traduce la accion del subject (transactional.email.<accion>) a su
// hito. Lo que no es un hito contable (queued, u otro que se anada) no se cuenta.
func MilestoneFromAction(action string) (Milestone, bool) {
	switch m := Milestone(action); m {
	case MilestoneSent, MilestoneDelivered, MilestoneBounced, MilestoneComplained,
		MilestoneOpened, MilestoneClicked, MilestoneUnsubscribed, MilestoneFailed:
		return m, true
	}
	return "", false
}

// BounceKind clasifica un rebote.
type BounceKind string

const (
	BounceHard BounceKind = "hard"
	BounceSoft BounceKind = "soft"
)

// BounceKindFromProvider clasifica el tipo de rebote del proveedor. Solo el permanente es
// duro; transitorio, indeterminado o ausente cuenta como blando, el mismo criterio con el
// que suppression decide suprimir una direccion.
func BounceKindFromProvider(t string) BounceKind {
	if strings.EqualFold(strings.TrimSpace(t), "permanent") {
		return BounceHard
	}
	return BounceSoft
}

func (k BounceKind) counter() Counter {
	if k == BounceHard {
		return CounterBouncedHard
	}
	return CounterBouncedSoft
}

// maxDomainLen es la longitud maxima de un nombre de dominio (RFC 1035).
const maxDomainLen = 253

// RecipientDomain extrae el dominio de una direccion, en minusculas. Analytics no guarda
// la direccion: el dominio basta para saber que proveedor rebota o se queja. Devuelve ""
// si no hay un dominio valido.
func RecipientDomain(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return ""
	}
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(email[at+1:])), ".")
	if d == "" || len(d) > maxDomainLen || !strings.Contains(d, ".") ||
		strings.HasPrefix(d, ".") || strings.HasPrefix(d, "-") || strings.Contains(d, "..") {
		return ""
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return ""
		}
	}
	return d
}

// MaxClockSkew es el adelanto maximo que se tolera en el instante de un hecho.
const MaxClockSkew = time.Hour

// OccurredAt elige el instante de un hecho: el que declara el emisor (el del proveedor
// cuando lo trae) si es creible; si no, el del sobre del evento; y si tampoco, el de
// recepcion. Un instante futuro por encima del desfase tolerado no es creible: abriria
// dias que aun no existen en los agregados.
func OccurredAt(declared, published, now time.Time) time.Time {
	limit := now.Add(MaxClockSkew)
	for _, t := range []time.Time{declared, published} {
		if !t.IsZero() && !t.After(limit) {
			return t.UTC()
		}
	}
	return now.UTC()
}

// MessageEvent es un hito de un mensaje tal como lo lee analytics.
type MessageEvent struct {
	EventID         uuid.UUID
	TenantID        uuid.UUID
	MessageID       uuid.UUID
	Milestone       Milestone
	Class           Class
	CampaignID      *uuid.UUID
	RecipientDomain string
	BounceKind      BounceKind
	// Link es la URL de un clic ya normalizada (NormalizeLink): sin utm_ ni identificadores
	// del destinatario. Vacia en los demas hitos o si no es agregable.
	Link string
	// Test: el mensaje es un envio de prueba de una campana (lo marca transactional en su
	// lote interno). No es actividad real y no se cuenta.
	Test bool
	// OccurredAt es el instante declarado por el emisor (cero si no lo trae) y
	// PublishedAt el del sobre del evento; la ingesta elige con OccurredAt().
	OccurredAt  time.Time
	PublishedAt time.Time
}

// Validate comprueba lo que el evento debe traer para poder contarse.
func (e MessageEvent) Validate() error {
	switch {
	case e.EventID == uuid.Nil:
		return invalidEvent("evento sin id")
	case e.TenantID == uuid.Nil:
		return invalidEvent("evento sin empresa")
	case e.MessageID == uuid.Nil:
		return invalidEvent("evento sin message_id")
	case !e.Class.Valid():
		return invalidEvent("clase de envio desconocida %q", e.Class)
	case e.CampaignID != nil && *e.CampaignID == uuid.Nil:
		return invalidEvent("campaign_id vacio")
	case e.OccurredAt.IsZero():
		return invalidEvent("evento sin instante")
	}
	if _, ok := MilestoneFromAction(string(e.Milestone)); !ok {
		return invalidEvent("hito desconocido %q", e.Milestone)
	}
	return nil
}
