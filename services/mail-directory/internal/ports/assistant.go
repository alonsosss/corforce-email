package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// AssistantSettingsRepository guarda el interruptor del asistente del webmail por empresa
// (mail.assistant_settings). Una fila por empresa; sin fila, domain.ErrNotFound.
type AssistantSettingsRepository interface {
	Get(ctx context.Context, tenantID uuid.UUID) (*domain.AssistantSettings, error)
	Upsert(ctx context.Context, s *domain.AssistantSettings) error
}
