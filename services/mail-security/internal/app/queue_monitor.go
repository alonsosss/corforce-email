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

// Run bloquea hasta que el contexto se cancele.
func (m *QueueMonitor) Run(ctx context.Context) {
	m.poll(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

func (m *QueueMonitor) poll(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, m.interval)
	defer cancel()
	listing, err := m.engine.List(pctx, 1)
	if err != nil {
		m.metrics.QueuePollFailed()
		m.logger.Warn("no se pudo consultar la cola de Postfix", zap.Error(err))
		return
	}
	var oldest time.Time
	if listing.OldestArrival > 0 {
		oldest = time.Unix(listing.OldestArrival, 0)
	}
	m.metrics.QueueObserved(listing.Counts, oldest)
}
