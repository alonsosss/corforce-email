package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"go.uber.org/zap"
)

// QueueUseCase es el gestor de la cola de Postfix de la celda: ver los mensajes con su motivo de
// diferimiento, reintentarlos, retenerlos, liberarlos y borrarlos, y vaciar la cola diferida. La cola
// mezcla el correo de todas las empresas de la celda, asi que solo lo opera el superadmin: el permiso
// queue es de plataforma y aqui se exige de nuevo, porque la comprobacion de permisos deja pasar tambien
// al administrador de una empresa. Nunca devuelve el contenido de un mensaje.
type QueueUseCase struct {
	engine ports.EngineQueue
	logger *zap.Logger
}

func NewQueueUseCase(engine ports.EngineQueue, logger *zap.Logger) *QueueUseCase {
	return &QueueUseCase{engine: engine, logger: logger}
}

// List devuelve hasta limit mensajes (por defecto DefaultQueueListLimit, como mucho MaxQueueListLimit).
func (uc *QueueUseCase) List(ctx context.Context, platform bool, limit int) (domain.QueueListing, error) {
	if !platform {
		return domain.QueueListing{}, domain.ErrPlatformOnly
	}
	if uc.engine == nil {
		return domain.QueueListing{}, domain.ErrNotConfigured
	}
	if limit <= 0 {
		limit = domain.DefaultQueueListLimit
	}
	if limit > domain.MaxQueueListLimit {
		limit = domain.MaxQueueListLimit
	}
	return uc.engine.List(ctx, limit)
}

// Apply hace action sobre el mensaje id. actor es quien lo pide y queda en el registro: borrar un
// mensaje de la cola es perder correo de un cliente, y hay que poder decir quien lo hizo.
func (uc *QueueUseCase) Apply(ctx context.Context, platform bool, actor string, action domain.QueueAction, id string) error {
	if !platform {
		return domain.ErrPlatformOnly
	}
	if err := domain.ValidateQueueAction(action); err != nil {
		return err
	}
	if err := domain.ValidateQueueID(id); err != nil {
		return err
	}
	if uc.engine == nil {
		return domain.ErrNotConfigured
	}
	if err := uc.engine.Apply(ctx, action, id); err != nil {
		return err
	}
	uc.logger.Info("accion sobre la cola de correo", zap.String("action", string(action)), zap.String("queue_id", id), zap.String("actor", actor))
	return nil
}

// Flush pide a Postfix reintentar toda la cola diferida.
func (uc *QueueUseCase) Flush(ctx context.Context, platform bool, actor string) error {
	if !platform {
		return domain.ErrPlatformOnly
	}
	if uc.engine == nil {
		return domain.ErrNotConfigured
	}
	if err := uc.engine.Flush(ctx); err != nil {
		return err
	}
	uc.logger.Info("cola de correo vaciada", zap.String("actor", actor))
	return nil
}
