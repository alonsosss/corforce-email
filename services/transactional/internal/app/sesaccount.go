package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"go.uber.org/zap"
)

// RunSESAccountMonitor lee el estado de la cuenta de SES cada interval y lo publica en las
// metricas hasta que ctx se cancela. Es de solo lectura y barato (dos llamadas), asi que cada
// replica lo hace por su cuenta y las alertas toman el maximo. Un fallo no detiene el bucle:
// cuenta el fallo y la alerta de datos viejos avisa si persiste.
func (uc *UseCase) RunSESAccountMonitor(ctx context.Context, reader ports.SESAccountReader, interval time.Duration) {
	check := func() {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		status, err := reader.AccountStatus(cctx)
		if err == nil {
			err = status.Validate()
		}
		if err != nil {
			if ctx.Err() == nil {
				uc.metrics.SESAccountCheckFailed()
				uc.logger.Warn("transactional: no se pudo leer el estado de la cuenta de SES", zap.Error(err))
			}
			return
		}
		uc.metrics.SESAccount(status)
	}
	check()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

// NoteSESEvent cuenta un evento de SES autentico recibido, por su tipo.
func (uc *UseCase) NoteSESEvent(eventType string) { uc.metrics.SESEvent(eventType) }

// NoteSESEventRejected cuenta una notificacion rechazada en la ruta de eventos, por motivo.
func (uc *UseCase) NoteSESEventRejected(reason string) { uc.metrics.SESEventRejected(reason) }
