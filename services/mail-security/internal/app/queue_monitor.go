package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"go.uber.org/zap"
)

// QueueMonitor consulta la cola de Postfix cada intervalo y anota su tamano y el mensaje mas antiguo, para
// que Prometheus avise de una cola atascada sin que nadie tenga que abrir la pantalla. Pide un solo mensaje:
// el conteo y el mas antiguo son de la cola entera.
type QueueMonitor struct {
	engine   ports.EngineQueue
	metrics  ports.QueueMetrics
	interval time.Duration
	logger   *zap.Logger
}

func NewQueueMonitor(engine ports.EngineQueue, metrics ports.QueueMetrics, interval time.Duration, logger *zap.Logger) *QueueMonitor {
	return &QueueMonitor{engine: engine, metrics: metrics, interval: interval, logger: logger}
}

// maxBackoffShift acota la espera tras fallos seguidos a 2^2 = 4 intervalos: el agente recorre todos los
// ficheros de la cola para contarla, y con una cola enorme (o el agente sobrecargado) repetir la consulta
// cada intervalo solo suma carga al contenedor de Postfix. La alerta de fallos sostenidos sigue viendo un
// fallo cada pocos minutos.
const maxBackoffShift = 2

// Run bloquea hasta que el contexto se cancele.
func (m *QueueMonitor) Run(ctx context.Context) {
	failures := 0
	for {
		if m.poll(ctx) {
			failures = 0
		} else {
			failures++
		}
		timer := time.NewTimer(m.delay(failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// delay es lo que se espera hasta la siguiente consulta: un intervalo, y el doble, el cuadruple... tras
// cada fallo seguido.
func (m *QueueMonitor) delay(failures int) time.Duration {
	return m.interval << min(failures, maxBackoffShift)
}

// poll consulta la cola una vez y dice si tuvo respuesta.
func (m *QueueMonitor) poll(ctx context.Context) bool {
	pctx, cancel := context.WithTimeout(ctx, m.interval)
	defer cancel()
	listing, err := m.engine.List(pctx, 1)
	if err != nil {
		m.metrics.QueuePollFailed()
		m.logger.Warn("no se pudo consultar la cola de Postfix", zap.Error(err))
		return false
	}
	var oldest time.Time
	if listing.OldestArrival > 0 {
		oldest = time.Unix(listing.OldestArrival, 0)
	}
	m.metrics.QueueObserved(listing.Counts, oldest)
	return true
}
