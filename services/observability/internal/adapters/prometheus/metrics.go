// Package prometheus expone las metricas propias de observability en el registro que sirve pkg/server
// en /metrics.
package prometheus

import "github.com/prometheus/client_golang/prometheus"

// Metrics implementa ports.LogMetrics.
type Metrics struct {
	logQueries *prometheus.CounterVec
}

func New() *Metrics {
	m := &Metrics{
		logQueries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "observability_log_queries_total",
			Help: "Consultas del visor de registros por servicio consultado y desenlace (ok, invalid, not_configured, unavailable, error). Nunca lleva el texto buscado.",
		}, []string{"service", "outcome"}),
	}
	prometheus.MustRegister(m.logQueries)
	return m
}

func (m *Metrics) LogQueryObserved(service, outcome string) {
	m.logQueries.WithLabelValues(service, outcome).Inc()
}
