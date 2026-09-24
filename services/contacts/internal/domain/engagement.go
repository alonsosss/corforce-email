package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Interaccion de un contacto con los envios de marketing (campanas y flujos de
// automations, que transactional atribuye con campaign_id). Solo se guardan ids y horas:
// ni la direccion, ni el enlace, ni la ip del evento.

// EngagementKind es el hito de transactional.email.<accion> que se proyecta.
type EngagementKind string

const (
	EngagementDelivered EngagementKind = "delivered"
	EngagementOpened    EngagementKind = "opened"
	EngagementClicked   EngagementKind = "clicked"
)

const (
	// DefaultEngagementRetentionDays es la retencion de la proyeccion
	// (CONTACTS_ENGAGEMENT_RETENTION_DAYS). No puede ser menor que el tope de las reglas de
	// los ultimos N dias del DSL.
	DefaultEngagementRetentionDays = 400
	MaxEngagementRetentionDays     = 3650
	// engagementClockSkew es cuanto puede adelantarse la hora de un evento a la nuestra
	// antes de recortarla: una hora futura haria que un contacto "abrio en los ultimos N
	// dias" durante mas de N dias.
	engagementClockSkew = 5 * time.Minute
)

var (
	ErrInvalidEngagement = errors.New("evento de interaccion sin empresa, contacto, campana o tipo valido")
)

// EngagementEvent es un hito ya interpretado por el adaptador.
type EngagementEvent struct {
	TenantID   uuid.UUID
	ContactID  uuid.UUID
	CampaignID uuid.UUID
	Kind       EngagementKind
	OccurredAt time.Time
}

// EngagementTouch es lo que el hito aporta a la fila (contacto, campana): la proyeccion se
// queda con la recepcion mas antigua y con la apertura y el clic mas recientes, asi que
// aplicar el mismo toque dos veces, o en cualquier orden, deja la misma fila.
type EngagementTouch struct {
	TenantID   uuid.UUID
	ContactID  uuid.UUID
	CampaignID uuid.UUID
	ReceivedAt time.Time
	OpenedAt   *time.Time
	ClickedAt  *time.Time
}

// Touch valida el hito y lo traduce. Un clic cuenta tambien como apertura: quien hace clic
// abrio el correo aunque su cliente bloqueara el pixel. ok=false si el hito es anterior a
// la retencion (no se guarda lo que la poda borraria enseguida).
func (e EngagementEvent) Touch(now time.Time, retention time.Duration) (EngagementTouch, bool, error) {
	if e.TenantID == uuid.Nil || e.ContactID == uuid.Nil || e.CampaignID == uuid.Nil {
		return EngagementTouch{}, false, ErrInvalidEngagement
	}
	at := e.OccurredAt.UTC()
	if at.IsZero() || at.After(now.Add(engagementClockSkew)) {
		at = now
	}
	if retention > 0 && at.Before(now.Add(-retention)) {
		return EngagementTouch{}, false, nil
	}
	t := EngagementTouch{TenantID: e.TenantID, ContactID: e.ContactID, CampaignID: e.CampaignID, ReceivedAt: at}
	switch e.Kind {
	case EngagementDelivered:
	case EngagementOpened:
		t.OpenedAt = &at
	case EngagementClicked:
		t.OpenedAt = &at
		t.ClickedAt = &at
	default:
		return EngagementTouch{}, false, ErrInvalidEngagement
	}
	return t, true, nil
}
