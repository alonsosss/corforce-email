package domain

import (
	"time"

	"github.com/google/uuid"
)

// CampaignAction es la accion de un evento campaigns.campaign.<accion>.
type CampaignAction string

const (
	CampaignStarted   CampaignAction = "started"
	CampaignPaused    CampaignAction = "paused"
	CampaignResumed   CampaignAction = "resumed"
	CampaignCompleted CampaignAction = "completed"
	CampaignCancelled CampaignAction = "cancelled"
	CampaignFailed    CampaignAction = "failed"
)

// CampaignActionFrom traduce la accion del subject. scheduled no se registra: una campana
// programada aun no tiene envios que mostrar.
func CampaignActionFrom(action string) (CampaignAction, bool) {
	switch a := CampaignAction(action); a {
	case CampaignStarted, CampaignPaused, CampaignResumed, CampaignCompleted, CampaignCancelled, CampaignFailed:
		return a, true
	}
	return "", false
}

// Terminal indica si la accion cierra la campana.
func (a CampaignAction) Terminal() bool {
	return a == CampaignCompleted || a == CampaignCancelled || a == CampaignFailed
}

// maxStatusLen es el ancho de campaigns_seen.status.
const maxStatusLen = 32

// CampaignStatus devuelve el estado publicado por campaigns si es un identificador
// valido (minusculas y guion bajo); si no, la accion del evento.
func CampaignStatus(status string, action CampaignAction) string {
	if status == "" || len(status) > maxStatusLen {
		return string(action)
	}
	for _, r := range status {
		if !(r >= 'a' && r <= 'z' || r == '_') {
			return string(action)
		}
	}
	return status
}

// CampaignEvent es un cambio de estado de una campana.
type CampaignEvent struct {
	EventID    uuid.UUID
	TenantID   uuid.UUID
	CampaignID uuid.UUID
	Action     CampaignAction
	Status     string
	// OccurredAt y PublishedAt como en MessageEvent.
	OccurredAt  time.Time
	PublishedAt time.Time
}

func (e CampaignEvent) Validate() error {
	switch {
	case e.EventID == uuid.Nil:
		return invalidEvent("evento sin id")
	case e.TenantID == uuid.Nil:
		return invalidEvent("evento sin empresa")
	case e.CampaignID == uuid.Nil:
		return invalidEvent("evento sin campaign_id")
	case e.Status == "":
		return invalidEvent("evento sin estado")
	case e.OccurredAt.IsZero():
		return invalidEvent("evento sin instante")
	}
	if _, ok := CampaignActionFrom(string(e.Action)); !ok {
		return invalidEvent("accion de campana desconocida %q", e.Action)
	}
	return nil
}

// CampaignSeen es lo que analytics sabe de una campana por sus eventos.
type CampaignSeen struct {
	CampaignID uuid.UUID
	TenantID   uuid.UUID
	Status     string
	// StatusAt es el instante del evento que fijo Status.
	StatusAt    time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
}

// NewCampaignSeen es la campana tal como la describe su primer evento.
func NewCampaignSeen(ev CampaignEvent) CampaignSeen {
	c := CampaignSeen{CampaignID: ev.CampaignID, TenantID: ev.TenantID}
	c.Merge(ev)
	return c
}

// Merge incorpora un evento sin depender del orden de llegada: el estado es el del
// evento mas reciente (a igual instante gana el que cierra la campana), el inicio el mas
// temprano y el fin el mas tardio; pausar o reanudar solo cambia el estado. Devuelve si
// algo cambio.
func (c *CampaignSeen) Merge(ev CampaignEvent) bool {
	at := ev.OccurredAt.UTC()
	changed := false
	if c.Status == "" || at.After(c.StatusAt) ||
		(at.Equal(c.StatusAt) && ev.Action.Terminal() && c.Status != ev.Status) {
		c.Status, c.StatusAt = ev.Status, at
		changed = true
	}
	switch {
	case ev.Action == CampaignStarted:
		if c.StartedAt == nil || at.Before(*c.StartedAt) {
			c.StartedAt = &at
			changed = true
		}
	case ev.Action.Terminal():
		if c.CompletedAt == nil || at.After(*c.CompletedAt) {
			c.CompletedAt = &at
			changed = true
		}
	}
	return changed
}

// CampaignSummary es una campana con datos: lo visto en sus eventos (nil si solo se
// conoce por los envios) y sus totales.
type CampaignSummary struct {
	CampaignID  uuid.UUID
	Status      *string
	StartedAt   *time.Time
	CompletedAt *time.Time
	// FirstDay y LastDay son el primer y el ultimo dia con envios; nil si aun no hay.
	FirstDay *time.Time
	LastDay  *time.Time
	Totals   Counters
}

// CampaignDetail es una campana con su serie diaria.
type CampaignDetail struct {
	Summary CampaignSummary
	Range   Range
	Series  []DayCounters
}

// DomainStats son los totales de un dominio destino.
type DomainStats struct {
	RecipientDomain string
	Totals          Counters
}
