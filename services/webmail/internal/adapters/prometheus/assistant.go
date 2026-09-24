// Package prometheus expone las metricas propias del webmail en el registro que sirve pkg/server en
// /metrics.
package prometheus

import (
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// AssistantMetrics implementa ports.AssistantMetrics. Las etiquetas son de conjuntos cerrados: la accion
// y el resultado los fija el dominio y el modelo, la configuracion. El coste sale de los tokens por
// modelo con la tarifa vigente del proveedor, que no se fija en codigo.
type AssistantMetrics struct {
	requests     *prometheus.CounterVec
	tokens       *prometheus.CounterVec
	latency      *prometheus.HistogramVec
	auditFailure prometheus.Counter
}

func NewAssistantMetrics(reg prometheus.Registerer) *AssistantMetrics {
	m := &AssistantMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "webmail_assistant_requests_total",
			Help: "Peticiones al asistente del webmail por accion y resultado (ok, quota, disabled, busy, refused, failed, empty).",
		}, []string{"action", "outcome"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "webmail_assistant_tokens_total",
			Help: "Tokens facturables del proveedor del asistente por modelo y tipo (input, output).",
		}, []string{"model", "kind"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "webmail_assistant_provider_seconds",
			Help:    "Duracion de la llamada al proveedor del asistente, reintentos incluidos.",
			Buckets: []float64{0.5, 1, 2, 4, 8, 15, 30, 60},
		}, []string{"action"}),
		auditFailure: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "webmail_assistant_audit_failures_total",
			Help: "Usos del asistente que no dejaron apunte de auditoria (NATS o el stream WEBMAIL no disponibles).",
		}),
	}
	reg.MustRegister(m.requests, m.tokens, m.latency, m.auditFailure)
	for _, a := range domain.AssistantActions {
		m.latency.WithLabelValues(string(a))
		for _, o := range []string{domain.AssistantOutcomeOK, domain.AssistantOutcomeFailed, domain.AssistantOutcomeBusy} {
			m.requests.WithLabelValues(string(a), o)
		}
	}
	return m
}

func (m *AssistantMetrics) AssistantRequest(action domain.AssistantAction, outcome string) {
	m.requests.WithLabelValues(string(action), outcome).Inc()
}

func (m *AssistantMetrics) AssistantTokens(model string, input, output int) {
	if input > 0 {
		m.tokens.WithLabelValues(model, "input").Add(float64(input))
	}
	if output > 0 {
		m.tokens.WithLabelValues(model, "output").Add(float64(output))
	}
}

func (m *AssistantMetrics) AssistantLatency(action domain.AssistantAction, d time.Duration) {
	m.latency.WithLabelValues(string(action)).Observe(d.Seconds())
}

func (m *AssistantMetrics) AssistantAuditFailed() { m.auditFailure.Inc() }
