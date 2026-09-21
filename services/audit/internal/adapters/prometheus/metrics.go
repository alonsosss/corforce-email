// Package prometheus expone las metricas de las verificaciones de cadena de audit en el registro
// que sirve pkg/server en /metrics.
package prometheus

import (
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics implementa ports.IntegrityMetrics. Las etiquetas toman valores de conjuntos cerrados
// (origen y desenlace) y ninguna lleva la empresa: la empresa afectada esta en el log.
type Metrics struct {
	runs        *prometheus.CounterVec
	sweepBroken prometheus.Gauge
}

func New() *Metrics {
	m := &Metrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "audit_integrity_runs_total",
			Help: "Verificaciones de la cadena de hash terminadas, por origen (manual, sweep, request) y desenlace (ok, broken: la cadena esta rota, cancelled, failed: fallo tecnico que no dice nada de la cadena).",
		}, []string{"origin", "outcome"}),
		sweepBroken: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "audit_integrity_sweep_broken_tenants",
			Help: "Empresas cuya cadena dio rota en la ultima pasada completa del barrido periodico de verificacion.",
		}),
	}
	// Las series nacen a cero: un contador que aparece ya en 1 no da increase().
	for _, origin := range []domain.RunTrigger{domain.RunTriggerManual, domain.RunTriggerSweep, domain.RunTriggerRequest} {
		for _, outcome := range []string{domain.RunOutcomeOK, domain.RunOutcomeBroken, domain.RunOutcomeCancelled, domain.RunOutcomeFailed} {
			m.runs.WithLabelValues(string(origin), outcome)
		}
	}
	prometheus.MustRegister(m.runs, m.sweepBroken)
	return m
}

func (m *Metrics) RunFinished(origin domain.RunTrigger, outcome string) {
	m.runs.WithLabelValues(string(origin), outcome).Inc()
}

func (m *Metrics) SweepBroken(tenants int) { m.sweepBroken.Set(float64(tenants)) }
