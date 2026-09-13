// Package prometheus expone las metricas propias de mail-auth en el registro que sirve
// pkg/server en /metrics.
package prometheus

import (
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics cuenta los intentos de autenticacion por servicio y desenlace.
type Metrics struct {
	attempts *prometheus.CounterVec
}

func New() *Metrics {
	m := &Metrics{
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_auth_attempts_total",
			Help: "Intentos de autenticacion de buzon atendidos para Dovecot, por servicio y resultado.",
		}, []string{"service", "result"}),
	}
	prometheus.MustRegister(m.attempts)
	return m
}

// Attempt cuenta un intento. El servicio se etiqueta solo si Dovecot lo conoce: una
// cadena arbitraria en la etiqueta abriria la cardinalidad a quien controle el campo.
func (m *Metrics) Attempt(service string, result domain.Result) {
	label := "unknown"
	if _, known := domain.ProtocolFromService(service); known {
		label = strings.ToLower(strings.TrimSpace(service))
	}
	m.attempts.WithLabelValues(label, string(result)).Inc()
}
