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

// Unsubscribe registra la baja (RFC 8058): alta en suppression con reason unsubscribe,
// rastro local y evento. Idempotente: una segunda baja del mismo enlace no publica nada.
func (uc *UseCase) Unsubscribe(ctx context.Context, claims domain.UnsubscribeClaims, signature string) error {
	if err := uc.VerifyUnsubscribeLink(claims, signature); err != nil {
		return err
	}
	claims.Email = domain.NormalizeEmail(claims.Email)
	if err := uc.suppression.Add(ctx, claims.TenantID, ports.SuppressionEntry{
		Email: claims.Email, Reason: "unsubscribe", Source: uc.cfg.Source, MessageID: claims.MessageID.String(),
	}); err != nil {
		uc.logger.Warn("transactional: suppression no acepto la baja", zap.Error(err))
		return domain.ErrSuppressionUnavailable
	}
	return uc.repo.Transact(ctx, func(ctx context.Context) error {
		inserted, err := uc.repo.InsertUnsubscribe(ctx, &domain.Unsubscribe{
			ID: uuid.New(), TenantID: claims.TenantID, Email: claims.Email, MessageID: claims.MessageID, CreatedAt: uc.now(),
		})
		if err != nil || !inserted {
			return err
		}
		return uc.events.Publish(ctx, "transactional.email.unsubscribed", claims.TenantID, map[string]any{
			"tenant_id":  claims.TenantID.String(),
			"message_id": claims.MessageID.String(),
			"email":      claims.Email,
		})
	})
}
