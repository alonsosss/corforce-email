// Package prometheus expone las metricas propias de mail-security en el registro que sirve
// pkg/server en /metrics.
package prometheus

import (
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics implementa ports.DKIMReconcileMetrics. Las alertas que las leen estan en el grupo
// claves-dkim de ops/observability/prometheus/rules/plataforma.yml.
type Metrics struct {
	dkimRemovals    *prometheus.CounterVec
	dkimUnresolved  prometheus.Counter
	dkimLastSuccess prometheus.Gauge
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
	}
	// Las series nacen a cero: un contador que aparece ya en 1 no da increase().
	for _, reason := range domain.DKIMRemovalReasons() {
		m.dkimRemovals.WithLabelValues(string(reason))
	}
	prometheus.MustRegister(m.dkimRemovals, m.dkimUnresolved, m.dkimLastSuccess)
	return m
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
