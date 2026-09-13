package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// VerifyUnsubscribeLink comprueba la firma del enlace de baja.
func (uc *UseCase) VerifyUnsubscribeLink(claims domain.UnsubscribeClaims, signature string) error {
	claims.Email = domain.NormalizeEmail(claims.Email)
	if !domain.ValidEmail(claims.Email) || !uc.links.Verify(claims, signature) {
		return domain.ErrInvalidSignature
	}
	return nil
}

// Unsubscribe registra la baja (RFC 8058): alta en suppression con reason unsubscribe (y
// la campana si el mensaje era de marketing), rastro local y evento. Idempotente: una
// segunda baja del mismo enlace no publica nada. Leer la atribucion nunca bloquea la baja:
// si falla, suppression la recibe igual y el evento la lee dentro de la transaccion.
func (uc *UseCase) Unsubscribe(ctx context.Context, claims domain.UnsubscribeClaims, signature string) error {
	if err := uc.VerifyUnsubscribeLink(claims, signature); err != nil {
		return err
	}
	claims.Email = domain.NormalizeEmail(claims.Email)
	known := uc.attributionFor(ctx, claims.TenantID, claims.MessageID)
	if err := uc.suppression.Add(ctx, claims.TenantID, ports.SuppressionEntry{
		Email: claims.Email, Reason: "unsubscribe", Source: uc.cfg.Source,
		MessageID: claims.MessageID.String(), CampaignID: campaignOf(known),
	}); err != nil {
		uc.logger.Warn("transactional: suppression no acepto la baja", zap.Error(err))
		return domain.ErrSuppressionUnavailable
	}
	now := uc.now()
	return uc.repo.Transact(ctx, func(ctx context.Context) error {
		inserted, err := uc.repo.InsertUnsubscribe(ctx, &domain.Unsubscribe{
			ID: uuid.New(), TenantID: claims.TenantID, Email: claims.Email, MessageID: claims.MessageID, CreatedAt: now,
		})
		if err != nil || !inserted {
			return err
		}
		attr, err := uc.resolveAttribution(ctx, claims.TenantID, claims.MessageID, known)
		if err != nil {
			return err
		}
		return uc.events.Publish(ctx, "transactional.email.unsubscribed", claims.TenantID, map[string]any{
			"tenant_id":   claims.TenantID.String(),
			"message_id":  claims.MessageID.String(),
			"email":       claims.Email,
			"class":       attr.Class,
			"campaign_id": nullableID(attr.CampaignID),
			"contact_id":  nullableID(attr.ContactID),
			"test":        attr.Test,
			"occurred_at": eventTime(now),
		})
	})
}
