// Package prometheus expone las metricas de las verificaciones de cadena y del informe de anclas
// de audit en el registro que sirve pkg/server en /metrics.
package prometheus

import (
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics implementa ports.IntegrityMetrics y ports.AnchorReportMetrics. Las etiquetas toman
// valores de conjuntos cerrados y ninguna lleva la empresa: la empresa afectada esta en el log.
type Metrics struct {
	runs        *prometheus.CounterVec
	sweepBroken prometheus.Gauge

	reports           *prometheus.CounterVec
	reportLastSuccess prometheus.Gauge
	reportInterval    prometheus.Gauge
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
		reports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "audit_anchor_reports_total",
			Help: "Envios del informe de anclas de auditoria (una copia de la cabeza de cada cadena fuera del servidor), uno por direccion de AUDIT_ANCHOR_RUA, por resultado (sent, suppressed: la direccion esta suprimida, rejected: transactional lo rechazo, failed: no llego a transactional).",
		}, []string{"result"}),
		reportLastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "audit_anchor_report_last_success_timestamp_seconds",
			Help: "Instante en que el informe de anclas salio por ultima vez hacia al menos una direccion; 0 si ninguno desde el arranque.",
		}),
		reportInterval: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "audit_anchor_report_interval_seconds",
			Help: "Intervalo configurado del informe de anclas (AUDIT_ANCHOR_REPORT_INTERVAL); 0 si el informe esta desactivado (AUDIT_ANCHOR_RUA vacia).",
		}),
	}
	// Las series nacen a cero: un contador que aparece ya en 1 no da increase().
	for _, origin := range []domain.RunTrigger{domain.RunTriggerManual, domain.RunTriggerSweep, domain.RunTriggerRequest} {
		for _, outcome := range []string{domain.RunOutcomeOK, domain.RunOutcomeBroken, domain.RunOutcomeCancelled, domain.RunOutcomeFailed} {
			m.runs.WithLabelValues(string(origin), outcome)
		}
	}
	for _, result := range []string{domain.ReportResultSent, domain.ReportResultSuppressed, domain.ReportResultRejected, domain.ReportResultFailed} {
		m.reports.WithLabelValues(result)
	}
	prometheus.MustRegister(m.runs, m.sweepBroken, m.reports, m.reportLastSuccess, m.reportInterval)
	return m
}

func (m *Metrics) RunFinished(origin domain.RunTrigger, outcome string) {
	m.runs.WithLabelValues(string(origin), outcome).Inc()
}

func (m *Metrics) SweepBroken(tenants int) { m.sweepBroken.Set(float64(tenants)) }

func (m *Metrics) ReportResult(result string) { m.reports.WithLabelValues(result).Inc() }

func (m *Metrics) ReportSucceeded(at time.Time) { m.reportLastSuccess.Set(float64(at.Unix())) }

func (m *Metrics) ReportSchedule(interval time.Duration) { m.reportInterval.Set(interval.Seconds()) }
