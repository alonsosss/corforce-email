// Package prometheus expone las metricas propias de reputation en el registro que sirve
// pkg/server en /metrics.
package prometheus

import (
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics implementa ports.Metrics. Todas las etiquetas toman valores de conjuntos
// cerrados (clase, estado, resultado, dependencia): nada que llegue de fuera las abre.
type Metrics struct {
	authorize    *prometheus.CounterVec
	stateChanges *prometheus.CounterVec
	degraded     *prometheus.CounterVec
}

func New() *Metrics {
	m := &Metrics{
		authorize: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reputation_authorize_total",
			Help: "Autorizaciones previas al envio, por clase y resultado (allowed o motivo de denegacion).",
		}, []string{"class", "result"}),
		stateChanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reputation_state_changes_total",
			Help: "Cambios de estado de reputacion, por clase y estado de destino.",
		}, []string{"class", "to"}),
		degraded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reputation_authorize_degraded_total",
			Help: "Autorizaciones concedidas sin poder consultar una dependencia (redis: limite de tasa; billing: derecho mensual).",
		}, []string{"dependency"}),
	}
	prometheus.MustRegister(m.authorize, m.stateChanges, m.degraded)
	return m
}

func (m *Metrics) Authorize(class domain.Class, result string) {
	m.authorize.WithLabelValues(string(class), result).Inc()
}

func (m *Metrics) StateChanged(class domain.Class, to domain.State) {
	m.stateChanges.WithLabelValues(string(class), string(to)).Inc()
}

func (m *Metrics) Degraded(dependency string) {
	m.degraded.WithLabelValues(dependency).Inc()
}
