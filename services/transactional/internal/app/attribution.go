package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// attributionFor lee la clase, la campana y el contacto del mensaje antes de avisar a
// suppression. Si no se puede leer, la supresion no espera: se sigue sin atribucion y la
// transaccion posterior la vuelve a leer (un mensaje desconocido se ignora; una base caida
// hace que el emisor reintente).
func (uc *UseCase) attributionFor(ctx context.Context, tenantID, messageID uuid.UUID) *domain.MessageAttribution {
	a, err := uc.repo.GetAttribution(ctx, tenantID, messageID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			uc.logger.Warn("transactional: no se pudo leer la atribucion del mensaje; se sigue sin ella",
				zap.String("tenant_id", tenantID.String()), zap.String("message_id", messageID.String()), zap.Error(err))
		}
		return nil
	}
	return a
}

// resolveAttribution devuelve la atribucion ya leida o la lee dentro de la transaccion. Un
// mensaje que no existe se atribuye como transaccional sin campana ni contacto, que es el
// contrato de un evento sin clase.
func (uc *UseCase) resolveAttribution(ctx context.Context, tenantID, messageID uuid.UUID, known *domain.MessageAttribution) (domain.MessageAttribution, error) {
	if known != nil {
		return *known, nil
	}
	a, err := uc.repo.GetAttribution(ctx, tenantID, messageID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.MessageAttribution{Class: domain.ClassTransactional}, nil
	}
	if err != nil {
		return domain.MessageAttribution{}, err
	}
	a.Class = domain.ClassOrDefault(a.Class)
	return *a, nil
}

// eventTime es el momento del hecho en los eventos transactional.email.*: la hora de SES en
// lo que llega por la ingesta, no la del rele de la outbox, que puede ir con retraso.
func eventTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// nullableID pone null en el evento cuando no hay campana o contacto.
func nullableID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// campaignOf es la campana en texto para suppression; vacia si no la hay.
func campaignOf(a *domain.MessageAttribution) string {
	if a == nil || a.CampaignID == nil {
		return ""
	}
	return a.CampaignID.String()
}
