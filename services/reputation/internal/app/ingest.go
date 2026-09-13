package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

// Subjects que este servicio consume. Los publica transactional en su stream
// TRANSACTIONAL; aqui solo se leen.
const (
	SubjectEmailSent       = "transactional.email.sent"
	SubjectEmailBounced    = "transactional.email.bounced"
	SubjectEmailComplained = "transactional.email.complained"

	// BounceTypePermanent es el unico rebote que cuenta para la reputacion: el
	// transitorio (buzon lleno, servidor caido) no dice nada de la practica de quien envia.
	BounceTypePermanent = "permanent"

	maxEventIDLength = 200
)

// DeliveryKind es el hecho de entrega que describe un evento.
type DeliveryKind string

const (
	KindSent       DeliveryKind = "sent"
	KindBounced    DeliveryKind = "bounced"
	KindComplained DeliveryKind = "complained"
)

// KindForSubject traduce un subject consumido a su hecho de entrega.
func KindForSubject(subject string) (DeliveryKind, bool) {
	switch subject {
	case SubjectEmailSent:
		return KindSent, true
	case SubjectEmailBounced:
		return KindBounced, true
	case SubjectEmailComplained:
		return KindComplained, true
	}
	return "", false
}

// DeliveryEvent es un hecho de entrega tal como lo cuenta este servicio.
type DeliveryEvent struct {
	// EventID es el id del envelope: la clave de deduplicacion.
	EventID  string
	TenantID uuid.UUID
	Kind     DeliveryKind
	Class    domain.Class
	// Recipients son los destinatarios del envio. Los rebotes y las quejas llegan uno por
	// destinatario, asi que los envios se cuentan igual para que la tasa compare lo mismo.
	// Menos de 1 cuenta como 1.
	Recipients int64
	BounceType string
	OccurredAt time.Time
}

// IngestResult dice que se hizo con el evento, para el log del consumidor.
type IngestResult struct {
	// Ignored: el hecho no cuenta (rebote transitorio).
	Ignored bool
	// Duplicate: el evento ya se habia contado y no se sumo nada.
	Duplicate bool
}

// IsInputError distingue el error que viene del contenido del evento (no cambia al
// reintentar) del transitorio (la base no respondio). El consumidor descarta el primero y
// reintenta el segundo.
func IsInputError(err error) bool {
	return errors.Is(err, domain.ErrInvalidEvent) || errors.Is(err, domain.ErrInvalidClass)
}

// RecordDelivery cuenta un hecho de entrega de forma idempotente: anotar el id del evento y
// sumar el contador van en la misma transaccion, asi que una reentrega no suma dos veces.
// Un rebote o una queja reevaluan la clase despues, tambien cuando el evento es una
// reentrega: si la primera entrega sumo y fallo al reevaluar, esta termina el trabajo.
func (uc *UseCase) RecordDelivery(ctx context.Context, ev DeliveryEvent) (IngestResult, error) {
	delta, err := ev.delta()
	if err != nil {
		return IngestResult{}, err
	}
	if delta == (domain.Counts{}) {
		return IngestResult{Ignored: true}, nil
	}
	day := dayOf(uc.eventTime(ev.OccurredAt))
	counted := false
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		fresh, err := uc.stats.MarkProcessed(ctx, ev.TenantID, ev.EventID)
		if err != nil || !fresh {
			return err
		}
		counted = true
		return uc.stats.AddDaily(ctx, ev.TenantID, ev.Class, day, delta)
	})
	if err != nil {
		return IngestResult{}, err
	}
	res := IngestResult{Duplicate: !counted}
	if ev.Kind != KindSent {
		if _, _, err := uc.Reevaluate(ctx, ev.TenantID, ev.Class); err != nil {
			return res, err
		}
	}
	return res, nil
}

// delta valida el evento y devuelve lo que suma; cero si el hecho no cuenta.
func (ev DeliveryEvent) delta() (domain.Counts, error) {
	if ev.EventID == "" || len(ev.EventID) > maxEventIDLength {
		return domain.Counts{}, fmt.Errorf("%w: id de evento vacio o demasiado largo", domain.ErrInvalidEvent)
	}
	if ev.TenantID == uuid.Nil {
		return domain.Counts{}, fmt.Errorf("%w: sin empresa", domain.ErrInvalidEvent)
	}
	if _, err := domain.ParseClass(string(ev.Class)); err != nil {
		return domain.Counts{}, err
	}
	switch ev.Kind {
	case KindSent:
		n := ev.Recipients
		if n < 1 {
			n = 1
		}
		return domain.Counts{Sent: n}, nil
	case KindBounced:
		if ev.BounceType != BounceTypePermanent {
			return domain.Counts{}, nil
		}
		return domain.Counts{Bounced: 1}, nil
	case KindComplained:
		return domain.Counts{Complained: 1}, nil
	}
	return domain.Counts{}, fmt.Errorf("%w: hecho de entrega desconocido %q", domain.ErrInvalidEvent, ev.Kind)
}

// eventTime es el instante con el que se fecha el hecho: el del evento si es creible, el
// del reloj si falta o viene del futuro (un reloj adelantado no debe abrir un dia nuevo).
func (uc *UseCase) eventTime(t time.Time) time.Time {
	now := uc.now()
	if t.IsZero() || t.After(now) {
		return now
	}
	return t
}

// dayOf es el dia UTC de un instante.
func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
