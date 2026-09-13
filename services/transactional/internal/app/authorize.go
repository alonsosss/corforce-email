package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var errReputationNotConfigured = errors.New("reputation client not configured")

// authorize pide a reputation permiso para enviar a count destinatarios de la clase. Una
// denegacion explicita se respeta siempre y no deja nada creado. Si reputation no
// responde, failOpen decide: el transaccional se envia igual y queda en el log (un codigo
// de acceso no se bloquea por una caida interna de la plataforma); el marketing no se
// encola y el llamador reintenta.
func (uc *UseCase) authorize(ctx context.Context, tenantID uuid.UUID, class string, count int, failOpen bool) error {
	if count <= 0 {
		return nil
	}
	var (
		auth *ports.Authorization
		err  error
	)
	if uc.reputation == nil {
		err = errReputationNotConfigured
	} else {
		auth, err = uc.reputation.Authorize(ctx, tenantID, class, count)
	}
	if err != nil {
		fields := []zap.Field{zap.String("tenant_id", tenantID.String()), zap.String("class", class), zap.Int("count", count), zap.Error(err)}
		if failOpen {
			uc.logger.Warn("transactional: reputation no respondio; el envio transaccional sigue sin autorizacion previa", fields...)
			return nil
		}
		uc.logger.Warn("transactional: reputation no respondio; no se encola", fields...)
		return domain.ErrReputationUnavailable
	}
	if auth.Allowed {
		return nil
	}
	denied := &domain.SendingDeniedError{Class: class, Reason: auth.Reason}
	if auth.RetryAfterSeconds != nil {
		retry := time.Duration(max(*auth.RetryAfterSeconds, 0)) * time.Second
		denied.RetryAfter = &retry
	}
	uc.logger.Info("transactional: reputation denego el envio",
		zap.String("tenant_id", tenantID.String()), zap.String("class", class), zap.Int("count", count),
		zap.String("reason", auth.Reason), zap.String("state", auth.State))
	return denied
}
