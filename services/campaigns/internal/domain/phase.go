package domain

import (
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PhaseKind es el tipo de fase de envio. Cada fase recorre la audiencia desde el principio
// con sus propios lotes y elige a quien envia:
//   - main: a todos (campana sin A/B ni zona horaria);
//   - sample: a los contactos de la muestra asignados a su variante;
//   - winner: con la variante ganadora, a quien aun no la recibio;
//   - zone: a quien aun no la recibio y ya alcanzo la hora local objetivo en su zona;
//   - resend: con otro asunto, a quien la recibio, se entrego y no la abrio.
type PhaseKind string

const (
	PhaseMain   PhaseKind = "main"
	PhaseSample PhaseKind = "sample"
	PhaseWinner PhaseKind = "winner"
	PhaseZone   PhaseKind = "zone"
	PhaseResend PhaseKind = "resend"
)

func PhaseKinds() []PhaseKind {
	return []PhaseKind{PhaseMain, PhaseSample, PhaseWinner, PhaseZone, PhaseResend}
}

type PhaseStatus string

const (
	PhasePending PhaseStatus = "pending"
	PhaseDone    PhaseStatus = "done"
)

func PhaseStatuses() []PhaseStatus { return []PhaseStatus{PhasePending, PhaseDone} }

// Round agrupa las fases por ronda de envio: un contacto recibe la campana una vez en la
// ronda inicial y, como mucho, otra en el reenvio.
type Round string

const (
	RoundInitial Round = "initial"
	RoundResend  Round = "resend"
)

// Orden de proceso: las muestras por variante, la ganadora, los tramos por instante y el
// reenvio al final.
const (
	ordinalMain   = 0
	ordinalWinner = 10
	ordinalZone   = 20
	ordinalResend = 30
)

// MaxAudiencePagesPerBatch acota cuantas paginas de la audiencia se leen para llenar un
// lote de una fase que filtra (muestra, ganadora, tramo, reenvio): con una muestra del 10 %
// repartida en cuatro variantes, una pagina da un 2,5 % de destinatarios y un lote por
// pagina haria la muestra cuarenta veces mas lenta.
const MaxAudiencePagesPerBatch = 20

type Phase struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	CampaignID  uuid.UUID
	Kind        PhaseKind
	Key         string
	Ordinal     int
	Variant     *int
	SlotAt      *time.Time
	NotBefore   *time.Time
	Status      PhaseStatus
	Targeted    int
	Accepted    int
	Suppressed  int
	StartedAt   *time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func newPhase(c *Campaign, kind PhaseKind, key string, ordinal int) *Phase {
	return &Phase{
		ID: uuid.New(), TenantID: c.TenantID, CampaignID: c.ID,
		Kind: kind, Key: key, Ordinal: ordinal, Status: PhasePending,
	}
}

// Round es la ronda a la que pertenecen los envios de la fase.
func (p *Phase) Round() Round {
	if p.Kind == PhaseResend {
		return RoundResend
	}
	return RoundInitial
}

// Filtered dice si la fase elige destinatarios dentro de cada pagina (todas salvo main).
func (p *Phase) Filtered() bool { return p.Kind != PhaseMain }

// Due dice si la fase puede empezar ya.
func (p *Phase) Due(now time.Time) bool { return p.NotBefore == nil || !p.NotBefore.After(now) }

func (p *Phase) Start(now time.Time) {
	if p.StartedAt == nil {
		p.StartedAt = &now
	}
}

func (p *Phase) Complete(now time.Time) {
	p.Status = PhaseDone
	p.CompletedAt = &now
	p.Start(now)
}

// InitialPhases son las fases con que arranca la campana: la principal; las muestras y
// la ganadora (A/B); o el primer tramo de zona en el momento del arranque, que envia a
// quien ya alcanzo su hora y da de alta los tramos siguientes.
func (c *Campaign) InitialPhases(now time.Time) []*Phase {
	switch {
	case c.ABTest != nil:
		out := make([]*Phase, 0, len(c.ABTest.Variants)+1)
		for i := range c.ABTest.Variants {
			v := i
			p := newPhase(c, PhaseSample, "sample:"+strconv.Itoa(i), i)
			p.Variant = &v
			out = append(out, p)
		}
		return append(out, newPhase(c, PhaseWinner, "winner", ordinalWinner))
	case c.TimezoneDelivery != nil:
		// El tramo de cierre, en la ultima zona del mundo, recoge a quien no llego a ningun
		// tramo: un contacto que entra en la audiencia despues del ultimo tramo descubierto,
		// con una zona que nadie tenia, no se queda sin la campana, la recibe como tarde ahi.
		first, closing := c.ZonePhase(now), c.ZonePhase(c.TimezoneDelivery.LocalSendAt.Latest())
		if !closing.SlotAt.After(*first.SlotAt) {
			return []*Phase{first}
		}
		return []*Phase{first, closing}
	}
	return []*Phase{newPhase(c, PhaseMain, "main", ordinalMain)}
}

// ZonePhase es el tramo de los contactos cuya hora local objetivo cae en slot.
func (c *Campaign) ZonePhase(slot time.Time) *Phase {
	slot = slot.UTC()
	p := newPhase(c, PhaseZone, "zone:"+slot.Format(time.RFC3339), ordinalZone)
	p.SlotAt = &slot
	p.NotBefore = &slot
	return p
}

// ResendPhase es el reenvio, que espera su retraso desde now (fin de la ronda inicial).
// nil si la campana no lo tiene configurado.
func (c *Campaign) ResendPhase(now time.Time) *Phase {
	if c.Resend == nil {
		return nil
	}
	at := now.Add(c.Resend.Delay())
	p := newPhase(c, PhaseResend, "resend", ordinalResend)
	p.NotBefore = &at
	return p
}

// Content es lo que recibe el destinatario de un lote: plantilla, version y un asunto que
// sustituye al de la plantilla (vacio = el de la plantilla). UTMContent distingue en la
// analitica de enlaces la variante A/B ("ab-a") y el reenvio ("resend").
type Content struct {
	TemplateID      uuid.UUID
	TemplateVersion int
	Subject         string
	UTMContent      string
}

// UTMContentResend es el utm_content de los enlaces del reenvio.
const UTMContentResend = "resend"

// VariantUTMContent es el utm_content de los enlaces de la variante i.
func VariantUTMContent(i int) string { return "ab-" + strings.ToLower(VariantLabel(i)) }

// ContentFor es el contenido de los lotes de la fase. La fase nil es la de un lote
// anterior a las fases, que es la principal.
func (c *Campaign) ContentFor(p *Phase) (Content, error) {
	if c.TemplateVersion == nil {
		return Content{}, NewValidationError("template_version: la campana no tiene version fijada")
	}
	base := Content{TemplateID: c.TemplateID, TemplateVersion: *c.TemplateVersion}
	if p == nil {
		return base, nil
	}
	switch p.Kind {
	case PhaseSample:
		return c.variantContent(*p.Variant)
	case PhaseWinner:
		if c.ABWinner == nil {
			return Content{}, NewValidationError("ab_winner: la ganadora aun no se decidio")
		}
		return c.variantContent(*c.ABWinner)
	case PhaseResend:
		content := base
		if c.ABTest != nil && c.ABWinner != nil {
			var err error
			if content, err = c.variantContent(*c.ABWinner); err != nil {
				return Content{}, err
			}
		}
		if c.Resend != nil {
			content.Subject = c.Resend.Subject
		}
		content.UTMContent = UTMContentResend
		return content, nil
	}
	return base, nil
}

func (c *Campaign) variantContent(i int) (Content, error) {
	if c.ABTest == nil || i < 0 || i >= len(c.ABTest.Variants) {
		return Content{}, NewValidationError("ab_test: la variante %d no existe", i)
	}
	v := c.ABTest.Variants[i]
	if v.PinnedVersion == nil {
		return Content{}, NewValidationError("ab_test: la variante %s no tiene version fijada", VariantLabel(i))
	}
	return Content{
		TemplateID:      c.ABTest.VariantTemplate(i, c.TemplateID),
		TemplateVersion: *v.PinnedVersion,
		Subject:         v.Subject,
		UTMContent:      VariantUTMContent(i),
	}, nil
}

// Selection es lo que una fase toma de una pagina de la audiencia.
type Selection struct {
	Recipients []Recipient
	// FutureSlots son los instantes de tramo posteriores al de la fase que aparecen en la
	// pagina: se dan de alta como fases nuevas.
	FutureSlots []time.Time
}

// SelectionLookup es lo que la fase necesita saber de la base sobre los contactos de la
// pagina: si ya recibieron la ronda inicial y si cumplen el reenvio.
type SelectionLookup struct {
	Sent           map[uuid.UUID]bool
	ResendEligible map[uuid.UUID]bool
}

// NeedsSent y NeedsResendEligibility dicen que consultas pide la fase.
func (p *Phase) NeedsSent() bool { return p.Kind == PhaseWinner || p.Kind == PhaseZone }

func (p *Phase) NeedsResendEligibility() bool { return p.Kind == PhaseResend }

// Select elige los destinatarios de la fase dentro de una pagina de contactos.
func (c *Campaign) Select(p *Phase, contacts []Contact, look SelectionLookup) Selection {
	var sel Selection
	if p == nil || p.Kind == PhaseMain {
		sel.Recipients = RecipientsFromContacts(contacts)
		return sel
	}
	kept := make([]Contact, 0, len(contacts))
	slots := map[time.Time]bool{}
	for _, ct := range contacts {
		switch p.Kind {
		case PhaseSample:
			if c.ABTest == nil || p.Variant == nil {
				continue
			}
			if v, in := c.ABTest.Assign(c.ID, ct.ID); in && v == *p.Variant {
				kept = append(kept, ct)
			}
		case PhaseWinner:
			if !look.Sent[ct.ID] {
				kept = append(kept, ct)
			}
		case PhaseZone:
			if look.Sent[ct.ID] || c.TimezoneDelivery == nil || p.SlotAt == nil {
				continue
			}
			target := c.TimezoneDelivery.Target(ct.Timezone).UTC()
			if !target.After(*p.SlotAt) {
				kept = append(kept, ct)
			} else if strings.TrimSpace(ct.Email) != "" {
				slots[target] = true
			}
		case PhaseResend:
			if look.ResendEligible[ct.ID] {
				kept = append(kept, ct)
			}
		}
	}
	sel.Recipients = RecipientsFromContacts(kept)
	for s := range slots {
		sel.FutureSlots = append(sel.FutureSlots, s)
	}
	return sel
}
