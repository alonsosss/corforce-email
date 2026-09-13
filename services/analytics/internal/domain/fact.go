package domain

import (
	"time"

	"github.com/google/uuid"
)

// Dimensions son los ejes de los agregados de un mensaje. Las fija el primer evento que
// lo nombra y no cambian despues: asi todos sus contadores caen en las mismas filas y un
// descuento siempre encuentra la suma que corrige.
type Dimensions struct {
	Class      Class
	CampaignID *uuid.UUID
	// RecipientDomain vacio = desconocido; el mensaje no entra en el agregado por dominio.
	RecipientDomain string
}

// MessageFact es la fila de un mensaje: sus dimensiones y la primera ocurrencia de cada
// hito.
type MessageFact struct {
	MessageID uuid.UUID
	TenantID  uuid.UUID
	Dimensions

	SentAt         *time.Time
	DeliveredAt    *time.Time
	FirstOpenedAt  *time.Time
	FirstClickedAt *time.Time
	BouncedAt      *time.Time
	BounceKind     BounceKind
	ComplainedAt   *time.Time
	UnsubscribedAt *time.Time
	FailedAt       *time.Time
}

// NewMessageFact es la fila de un mensaje que aun no tiene hitos.
func NewMessageFact(ev MessageEvent) MessageFact {
	return MessageFact{
		MessageID: ev.MessageID,
		TenantID:  ev.TenantID,
		Dimensions: Dimensions{
			Class:           ev.Class,
			CampaignID:      ev.CampaignID,
			RecipientDomain: ev.RecipientDomain,
		},
	}
}

// Change es el efecto de un evento sobre un mensaje: si la fila cambio y los contadores
// que hay que mover en los agregados.
type Change struct {
	Changed bool
	Deltas  []CounterDelta
}

// Apply incorpora un hito. Cada hito cuenta una vez por mensaje, en el dia de su primera
// ocurrencia conocida: una repeticion no suma y una ocurrencia anterior que llega tarde
// mueve la cuenta a su dia. Un rebote duro prevalece sobre uno blando.
func (f *MessageFact) Apply(ev MessageEvent) Change {
	if ev.Milestone == MilestoneBounced {
		return f.applyBounce(ev.OccurredAt, ev.BounceKind)
	}
	slot, counter, ok := f.slot(ev.Milestone)
	if !ok {
		return Change{}
	}
	return keepEarliest(slot, ev.OccurredAt, counter)
}

func (f *MessageFact) slot(m Milestone) (**time.Time, Counter, bool) {
	switch m {
	case MilestoneSent:
		return &f.SentAt, CounterSent, true
	case MilestoneDelivered:
		return &f.DeliveredAt, CounterDelivered, true
	case MilestoneOpened:
		return &f.FirstOpenedAt, CounterOpenedUnique, true
	case MilestoneClicked:
		return &f.FirstClickedAt, CounterClickedUnique, true
	case MilestoneComplained:
		return &f.ComplainedAt, CounterComplained, true
	case MilestoneUnsubscribed:
		return &f.UnsubscribedAt, CounterUnsubscribed, true
	case MilestoneFailed:
		return &f.FailedAt, CounterFailed, true
	}
	return nil, 0, false
}

func keepEarliest(slot **time.Time, at time.Time, counter Counter) Change {
	at = at.UTC()
	current := *slot
	if current == nil {
		*slot = &at
		return Change{Changed: true, Deltas: []CounterDelta{{Day: Day(at), Counter: counter, Delta: 1}}}
	}
	if !at.Before(*current) {
		return Change{}
	}
	previous := *current
	*slot = &at
	return Change{Changed: true, Deltas: move(previous, counter, at, counter)}
}

func (f *MessageFact) applyBounce(at time.Time, kind BounceKind) Change {
	at = at.UTC()
	if kind != BounceHard {
		kind = BounceSoft
	}
	if f.BouncedAt == nil {
		f.BouncedAt, f.BounceKind = &at, kind
		return Change{Changed: true, Deltas: []CounterDelta{{Day: Day(at), Counter: kind.counter(), Delta: 1}}}
	}
	previousAt, previousKind := *f.BouncedAt, f.BounceKind
	nextAt, nextKind := previousAt, previousKind
	if at.Before(nextAt) {
		nextAt = at
	}
	if kind == BounceHard {
		nextKind = BounceHard
	}
	if nextAt.Equal(previousAt) && nextKind == previousKind {
		return Change{}
	}
	f.BouncedAt, f.BounceKind = &nextAt, nextKind
	return Change{Changed: true, Deltas: move(previousAt, previousKind.counter(), nextAt, nextKind.counter())}
}

// move traslada una cuenta de (dia, contador) a otro; nada si caen en la misma celda.
func move(fromAt time.Time, fromCounter Counter, toAt time.Time, toCounter Counter) []CounterDelta {
	fromDay, toDay := Day(fromAt), Day(toAt)
	if fromDay.Equal(toDay) && fromCounter == toCounter {
		return nil
	}
	return []CounterDelta{
		{Day: fromDay, Counter: fromCounter, Delta: -1},
		{Day: toDay, Counter: toCounter, Delta: 1},
	}
}
