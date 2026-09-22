package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"go.uber.org/zap"
)

// AntispamUseCase es la lectura del antispam de la celda: los contadores del controller de Rspamd y su
// historial reciente. Rspamd analiza el correo de todas las empresas de la celda y cada fila del
// historial lleva remitente, destinatarios y asunto, asi que solo lo lee el superadmin: el permiso
// rspamd es de plataforma y aqui se exige de nuevo, porque la comprobacion de permisos deja pasar
// tambien al administrador de una empresa. Nunca devuelve el contenido de un mensaje ni escribe nada
// en el controller.
type AntispamUseCase struct {
	inspector ports.AntispamInspector
	logger    *zap.Logger
}

func NewAntispamUseCase(inspector ports.AntispamInspector, logger *zap.Logger) *AntispamUseCase {
	return &AntispamUseCase{inspector: inspector, logger: logger}
}

// Stats devuelve los contadores del controller. actor es quien lo pide y queda en el registro.
func (uc *AntispamUseCase) Stats(ctx context.Context, platform bool, actor string) (domain.RspamdStats, error) {
	if !platform {
		return domain.RspamdStats{}, domain.ErrPlatformOnly
	}
	if uc.inspector == nil {
		return domain.RspamdStats{}, domain.ErrNotConfigured
	}
	out, err := uc.inspector.Stats(ctx)
	if err != nil {
		return domain.RspamdStats{}, err
	}
	uc.logger.Info("consulta del antispam", zap.String("kind", "stats"), zap.String("actor", actor))
	return out, nil
}

// History devuelve las filas mas recientes (por defecto DefaultRspamdHistoryRows, como mucho
// MaxRspamdHistoryRows).
func (uc *AntispamUseCase) History(ctx context.Context, platform bool, actor string, limit int) (domain.RspamdHistory, error) {
	if !platform {
		return domain.RspamdHistory{}, domain.ErrPlatformOnly
	}
	if uc.inspector == nil {
		return domain.RspamdHistory{}, domain.ErrNotConfigured
	}
	if limit <= 0 {
		limit = domain.DefaultRspamdHistoryRows
	}
	if limit > domain.MaxRspamdHistoryRows {
		limit = domain.MaxRspamdHistoryRows
	}
	rows, err := uc.inspector.History(ctx)
	if err != nil {
		return domain.RspamdHistory{}, err
	}
	out := domain.NewestRspamdHistory(rows, limit)
	uc.logger.Info("consulta del antispam", zap.String("kind", "history"), zap.Int("limit", limit), zap.Int("rows", len(out.Rows)), zap.String("actor", actor))
	return out, nil
}
