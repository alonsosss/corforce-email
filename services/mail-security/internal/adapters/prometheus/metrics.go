// Package prometheus expone las metricas propias de mail-security en el registro que sirve
// pkg/server en /metrics.
package prometheus

import (
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics implementa ports.DKIMReconcileMetrics y ports.SessionRevocationMetrics. Las alertas que
// las leen estan en los grupos claves-dkim y revocacion-en-dovecot de
// ops/observability/prometheus/rules/plataforma.yml.
type Metrics struct {
	dkimRemovals       *prometheus.CounterVec
	dkimUnresolved     prometheus.Counter
	dkimLastSuccess    prometheus.Gauge
	dovecotRevocations *prometheus.CounterVec
	dovecotFailures    *prometheus.CounterVec
}

func New() *Metrics {
	m := &Metrics{
		dkimRemovals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_security_dkim_reconcile_removals_total",
			Help: "Dominios a los que el repaso retiro las claves DKIM de los motores, por motivo (not_served: no esta activo en el directorio de la celda; tenant_gone: organization ya no conoce su empresa).",
		}, []string{"reason"}),
		dkimUnresolved: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mail_security_dkim_reconcile_unresolved_total",
			Help: "Dominios cuyas claves DKIM el repaso conservo porque organization no dio su empresa.",
		}),
		dkimLastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mail_security_dkim_reconcile_last_success_timestamp_seconds",
			Help: "Instante Unix de la ultima pasada completa del repaso de claves DKIM en esta replica; 0 si no ha completado ninguna desde que arranco.",
		}),
		dovecotRevocations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_security_dovecot_revocations_total",
			Help: "Buzones cuya credencial se retiro de Dovecot con un evento del directorio, por accion (flush: cache de autenticacion vaciada; kick: vaciada y sesiones abiertas cerradas).",
		}, []string{"action"}),
		dovecotFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_security_dovecot_revocation_failures_total",
			Help: "Revocaciones en Dovecot que fallaron y esperan la reentrega del evento, por motivo (unreachable, rejected, command, directory).",
		}, []string{"reason"}),
	}
	// Las series nacen a cero: un contador que aparece ya en 1 no da increase().
	for _, reason := range domain.DKIMRemovalReasons() {
		m.dkimRemovals.WithLabelValues(string(reason))
	}
	for _, action := range domain.SessionActions() {
		m.dovecotRevocations.WithLabelValues(string(action))
	}
	for _, reason := range domain.SessionRevocationFailures() {
		m.dovecotFailures.WithLabelValues(string(reason))
	}
	prometheus.MustRegister(m.dkimRemovals, m.dkimUnresolved, m.dkimLastSuccess, m.dovecotRevocations, m.dovecotFailures)
	return m
}

func (m *Metrics) SessionsRevoked(action domain.SessionAction) {
	m.dovecotRevocations.WithLabelValues(string(action)).Inc()
}

func (m *Metrics) SessionRevocationFailed(reason domain.SessionRevocationFailure) {
	m.dovecotFailures.WithLabelValues(string(reason)).Inc()
}

func (m *Metrics) DKIMKeysRemoved(reason domain.DKIMRemovalReason, domains int) {
	if domains > 0 {
		m.dkimRemovals.WithLabelValues(string(reason)).Add(float64(domains))
	}
}

func (m *Metrics) DKIMUnresolved(domains int) {
	if domains > 0 {
		m.dkimUnresolved.Add(float64(domains))
	}
}

func (m *Metrics) DKIMReconciled(at time.Time) {
	m.dkimLastSuccess.Set(float64(at.UnixNano()) / 1e9)
}
