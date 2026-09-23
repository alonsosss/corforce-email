package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"go.uber.org/zap"
)

// SpamCheckUseCase puntua con el Rspamd de la celda un correo que todavia no se envia (el verificador de
// entregabilidad de templates). No sabe de empresas: el mensaje no se guarda ni se asocia a nadie, y el
// registro solo lleva su tamano y el veredicto, nunca cabeceras ni contenido.
type SpamCheckUseCase struct {
	scanner ports.SpamScanner
	metrics ports.SpamCheckMetrics
	logger  *zap.Logger
}

func NewSpamCheckUseCase(scanner ports.SpamScanner, metrics ports.SpamCheckMetrics, logger *zap.Logger) *SpamCheckUseCase {
	if metrics == nil {
		metrics = noSpamCheckMetrics{}
	}
	return &SpamCheckUseCase{scanner: scanner, metrics: metrics, logger: logger}
}

func (uc *SpamCheckUseCase) Check(ctx context.Context, msg []byte) (domain.SpamCheckResult, error) {
	if err := domain.ValidateSpamCheckMessage(msg); err != nil {
		uc.metrics.SpamChecked(domain.SpamCheckInvalid)
		return domain.SpamCheckResult{}, err
	}
	if uc.scanner == nil {
		uc.metrics.SpamChecked(domain.SpamCheckNotConfigured)
		return domain.SpamCheckResult{}, domain.ErrNotConfigured
	}
	started := time.Now()
	out, err := uc.scanner.Check(ctx, msg)
	if err != nil {
		outcome := domain.SpamCheckUnavailable
		if errors.Is(err, domain.ErrNotConfigured) {
			outcome = domain.SpamCheckNotConfigured
		}
		uc.metrics.SpamChecked(outcome)
		uc.logger.Warn("puntuacion antispam no disponible", zap.Int("bytes", len(msg)), zap.String("outcome", string(outcome)), zap.Error(err))
		return domain.SpamCheckResult{}, err
	}
	if out.Symbols == nil {
		out.Symbols = []domain.SpamCheckSymbol{}
	}
	domain.SortSpamCheckSymbols(out.Symbols)
	uc.metrics.SpamChecked(domain.SpamCheckScanned)
	uc.logger.Debug("puntuacion antispam", zap.Int("bytes", len(msg)), zap.String("action", out.Action),
		zap.String("score", out.Score.String()), zap.Int("symbols", len(out.Symbols)), zap.Duration("took", time.Since(started)))
	return out, nil
}

type noSpamCheckMetrics struct{}

func (noSpamCheckMetrics) SpamChecked(domain.SpamCheckOutcome) {}
