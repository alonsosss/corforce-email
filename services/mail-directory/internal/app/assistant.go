package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// errAssistantUnwired: main no cableo el repositorio del ajuste. Es un fallo de despliegue, no de la
// peticion; sin el, el asistente queda apagado para todas las empresas.
var errAssistantUnwired = errors.New("mail-directory: ajuste del asistente sin cablear")

// AssistantSettings devuelve el interruptor del asistente del webmail de la empresa; apagado si nunca
// se cambio.
func (uc *UseCase) AssistantSettings(ctx context.Context, tenantID uuid.UUID) (*domain.AssistantSettings, error) {
	if uc.assistant == nil {
		return nil, errAssistantUnwired
	}
	var out *domain.AssistantSettings
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		s, err := uc.assistant.Get(ctx, tenantID)
		if errors.Is(err, domain.ErrNotFound) {
			out = domain.NewAssistantSettings(tenantID)
			return nil
		}
		out = s
		return err
	})
	return out, err
}

// SetAssistantSettings activa o apaga el asistente para la empresa. by es el usuario de la plataforma
// que lo cambia (el tenant_admin que acepto el aviso); una empresa dada de baja no lo cambia.
func (uc *UseCase) SetAssistantSettings(ctx context.Context, tenantID, by uuid.UUID, enabled bool) (*domain.AssistantSettings, error) {
	if uc.assistant == nil {
		return nil, errAssistantUnwired
	}
	var out *domain.AssistantSettings
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		s, err := uc.assistant.Get(ctx, tenantID)
		if errors.Is(err, domain.ErrNotFound) {
			s, err = domain.NewAssistantSettings(tenantID), nil
		}
		if err != nil {
			return err
		}
		s.Apply(enabled, by, uc.now())
		if err := uc.assistant.Upsert(ctx, s); err != nil {
			return err
		}
		out = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	uc.logger.Info("mail-directory: asistente del webmail cambiado",
		zap.String("tenant_id", tenantID.String()), zap.String("by", by.String()), zap.Bool("enabled", enabled))
	return out, nil
}

// AssistantSettingsByUsername es la consulta del webmail: el interruptor de la empresa del buzon con el
// que se inicio sesion. La empresa la resuelve el directorio a partir del buzon; el webmail no la elige.
func (uc *UseCase) AssistantSettingsByUsername(ctx context.Context, username string) (*domain.AssistantSettings, error) {
	tenantID, _, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	return uc.AssistantSettings(ctx, tenantID)
}
