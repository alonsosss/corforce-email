package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/google/uuid"
)

// IsInputError distingue el error que viene del contenido de un evento (una direccion o
// una causa que no son validas y no lo seran al reintentar) del transitorio (la base no
// respondio). El consumidor descarta el primero y reintenta el segundo.
func IsInputError(err error) bool {
	return errors.Is(err, domain.ErrInvalidEmail) || errors.Is(err, domain.ErrInvalidReason)
}

// Subjects que este servicio consume. Los publica transactional (stream TRANSACTIONAL) y
// contacts (fase 4); aqui solo se leen.
const (
	SubjectEmailBounced       = "transactional.email.bounced"
	SubjectEmailComplained    = "transactional.email.complained"
	SubjectContactResubscribe = "contacts.contact.resubscribed"

	// SourceSES marca lo que llega por los eventos de entrega del proveedor.
	SourceSES = "ses"
	// BounceTypePermanent es el unico tipo de rebote que suprime; un transient (buzon
	// lleno, servidor caido) se reintenta y no dice nada definitivo de la direccion.
	BounceTypePermanent = "permanent"
)

// DeliveryEvent es el payload de transactional.email.bounced y .complained tal como lo
// lee este servicio: {tenant_id, message_id, email, bounce_type?, detail}.
type DeliveryEvent struct {
	Subject    string
	TenantID   uuid.UUID
	MessageID  *uuid.UUID
	Email      string
	BounceType string
	Detail     string
}

// IngestResult dice que hizo la ingesta con el evento, para el log del consumidor.
type IngestResult struct {
	// Ignored: el evento no describe un hecho definitivo (rebote transitorio o subject
	// que no suprime) y no se toca la lista.
	Ignored bool
	// Added: la causa entro (o se reactivo); false si la direccion ya la tenia vigente.
	Added bool
}

// Ingest convierte un evento de entrega en una exclusion. Idempotente: reentregar el
// mismo evento no cambia nada la segunda vez.
func (uc *UseCase) Ingest(ctx context.Context, ev DeliveryEvent) (IngestResult, error) {
	var reason domain.Reason
	switch ev.Subject {
	case SubjectEmailComplained:
		reason = domain.ReasonComplaint
	case SubjectEmailBounced:
		if ev.BounceType != BounceTypePermanent {
			return IngestResult{Ignored: true}, nil
		}
		reason = domain.ReasonHardBounce
	default:
		return IngestResult{Ignored: true}, nil
	}
	_, added, err := uc.Add(ctx, ev.TenantID, AddInput{
		Email: ev.Email, Reason: reason, Source: SourceSES, Detail: ev.Detail, MessageID: ev.MessageID,
	})
	if err != nil {
		return IngestResult{}, err
	}
	return IngestResult{Added: added}, nil
}
