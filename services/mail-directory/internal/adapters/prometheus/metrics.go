// Package prometheus expone las metricas propias de mail-directory en el registro que
// sirve pkg/server en /metrics.
package prometheus

import "github.com/prometheus/client_golang/prometheus"

// Metrics implementa ports.Metrics. La etiqueta motivo toma valores de un conjunto
// cerrado (ports.PlanSkip*): nada que llegue de fuera la abre.
type Metrics struct {
	planConfigured prometheus.Gauge
	planSkipped    *prometheus.CounterVec
	mfaReencrypted prometheus.Counter
	mfaPending     prometheus.Gauge
}

func New() *Metrics {
	m := &Metrics{
		planConfigured: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mail_directory_plan_limits_configured",
			Help: "1 si mail-directory sabe a quien preguntar los limites del plan (BILLING_URL); 0 si no, y entonces el plan no limita nada.",
		}),
		planSkipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_directory_plan_limit_skipped_total",
			Help: "Altas o cambios de cuota resueltos SIN aplicar el limite del plan, por motivo (unreachable: billing no respondio; sin_plan: la empresa no tiene plan o el plan no fija el recurso).",
		}, []string{"motivo"}),
		mfaReencrypted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mail_directory_mfa_secrets_reencrypted_total",
			Help: "Secretos de la verificacion en dos pasos de los buzones re-cifrados bajo la llave activa de MAIL_ENCRYPTION_KEY.",
		}),
		mfaPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mail_directory_mfa_secrets_pending_reencryption",
			Help: "Secretos de la verificacion en dos pasos que ninguna llave del anillo abrio en la ultima pasada de rotacion. Con uno solo, retirar una llave deja buzones sin poder entrar al webmail.",
		}),
	}
	prometheus.MustRegister(m.planConfigured, m.planSkipped, m.mfaReencrypted, m.mfaPending)
	return m
}

// PlanLimitsConfigured se publica al arrancar: una alerta sobre este valor descubre un
// BILLING_URL mal puesto sin esperar a que alguien cree un buzon.
func (m *Metrics) PlanLimitsConfigured(ok bool) {
	v := 0.0
	if ok {
		v = 1
	}
	m.planConfigured.Set(v)
}

func (m *Metrics) PlanLimitSkipped(motivo string) { m.planSkipped.WithLabelValues(motivo).Inc() }

// MFASecretsReencrypted suma los secretos re-cifrados, tambien los de una pasada interrumpida.
func (m *Metrics) MFASecretsReencrypted(n int) { m.mfaReencrypted.Add(float64(n)) }

// MFASecretsPending publica cuantos no abrio ninguna llave en la ultima pasada completa.
func (m *Metrics) MFASecretsPending(n int) { m.mfaPending.Set(float64(n)) }
