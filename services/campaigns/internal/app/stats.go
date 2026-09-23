package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/google/uuid"
)

// RecordDeliveryEvent suma un evento de transactional a su campana, una sola vez: el id
// del evento se registra en la misma transaccion que el contador, asi que una
// reentrega de JetStream no vuelve a sumar. Las aperturas y los clics suman solo la
// primera vez por mensaje. counted=false si no sumo nada.
func (uc *UseCase) RecordDeliveryEvent(ctx context.Context, ev domain.DeliveryEvent) (bool, error) {
	if ev.EventID == "" || len(ev.EventID) > domain.MaxEventIDLength {
		return false, domain.NewValidationError("event_id: el evento no trae un id utilizable")
	}
	if ev.Kind.UniquePerMessage() && ev.MessageID == nil {
		return false, domain.NewValidationError("message_id: %s sin mensaje no se puede contar como unico", ev.Kind)
	}
	at := ev.OccurredAt
	if at.IsZero() {
		at = uc.now()
	}
	counted := false
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		fresh, err := uc.stats.MarkProcessed(ctx, ev.TenantID, ev.EventID, uc.now())
		if err != nil || !fresh {
			return err
		}
		if ev.Kind.UniquePerMessage() {
			first, err := uc.stats.FirstEngagement(ctx, ev.TenantID, ev.CampaignID, *ev.MessageID, ev.ContactID, ev.Kind, at)
			if err != nil || !first {
				return err
			}
		}
		// La entrega por mensaje es lo que el reenvio exige para considerar que alguien
		// recibio la campana y no la abrio.
		if ev.Kind == domain.KindDelivered && ev.MessageID != nil {
			if err := uc.stats.NoteDelivery(ctx, ev.TenantID, ev.CampaignID, *ev.MessageID, ev.ContactID, at); err != nil {
				return err
			}
		}
		ok, err := uc.stats.IncrementCounter(ctx, ev.TenantID, ev.CampaignID, ev.Kind)
		if err != nil {
			return err
		}
		if !ok {
			return domain.ErrCampaignNotFound
		}
		counted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return counted, nil
}

// PruneProcessedEvents olvida los eventos contados hace mas de la retencion.
func (uc *UseCase) PruneProcessedEvents(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	return uc.stats.PruneProcessed(ctx, tenantID, uc.now().Add(-domain.ProcessedEventRetention))
}
