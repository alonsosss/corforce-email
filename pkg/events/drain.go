package events

import (
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// SubscriptionDrainTimeout es lo que DrainSubscriptions espera a los mensajes en vuelo. Cabe en el
// apagado de un servicio: pkg/server espera hasta 30 s a las peticiones HTTP y compose da 40 s
// (stop_grace_period) antes de matar el proceso.
const SubscriptionDrainTimeout = 5 * time.Second

// DrainSubscriptions deja de recibir en cada suscripcion y espera a que terminen los mensajes que ya
// tenia. Drain de nats.go vuelve en el acto y la baja sigue por detras: sin esperar, lo que el servidor
// empujaba durante la baja a un consumidor durable quedaba sin confirmar y no se reentregaba hasta
// AckWait (90 s). En cada despliegue, eventos con minuto y medio de retraso (2026-10-01, la prueba
// del rastro de audit fallaba una de cada quince veces). Lo que no termina a tiempo se avisa y queda
// para la reentrega, como antes.
func DrainSubscriptions(logger *zap.Logger, subs ...*nats.Subscription) {
	drainSubscriptions(logger, SubscriptionDrainTimeout, subs...)
}

func drainSubscriptions(logger *zap.Logger, timeout time.Duration, subs ...*nats.Subscription) int {
	for _, s := range subs {
		if s != nil {
			_ = s.Drain()
		}
	}
	deadline := time.Now().Add(timeout)
	pending := 0
	for _, s := range subs {
		if s == nil {
			continue
		}
		for s.IsValid() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if s.IsValid() {
			pending++
		}
	}
	if pending > 0 {
		logger.Warn("events: suscripciones sin terminar sus mensajes en vuelo; quedan para la reentrega",
			zap.Int("pending", pending), zap.Duration("timeout", timeout))
	}
	return pending
}
