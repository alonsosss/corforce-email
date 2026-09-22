package prometheus

import (
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// metricas registra las metricas una sola vez para todas las pruebas: New usa el registro global
// y un segundo registro de las mismas series entra en panico.
var metricas = sync.OnceValue(New)

// seriesCon devuelve el valor de cada serie del registro cuyo nombre empieza por prefijo.
func seriesCon(t *testing.T, prefijo string) map[string]float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), prefijo) {
			continue
		}
		for _, m := range mf.GetMetric() {
			key := mf.GetName()
			for _, l := range m.GetLabel() {
				key += "{" + l.GetName() + "=" + l.GetValue() + "}"
			}
			if mf.GetType() == dto.MetricType_GAUGE {
				out[key] = m.GetGauge().GetValue()
			} else {
				out[key] = m.GetCounter().GetValue()
			}
		}
	}
	return out
}

// Las series del informe de anclas nacen a cero, cada resultado con la suya, y el intervalo y el
// ultimo exito son gauges: la alerta AnclaDeAuditoriaSinEnviar los lee tal cual.
func TestLasSeriesDelInformeDeAnclasNacenACeroYCuentan(t *testing.T) {
	m := metricas()
	want := map[string]float64{
		"audit_anchor_reports_total{result=sent}":            0,
		"audit_anchor_reports_total{result=suppressed}":      0,
		"audit_anchor_reports_total{result=rejected}":        0,
		"audit_anchor_reports_total{result=failed}":          0,
		"audit_anchor_report_last_success_timestamp_seconds": 0,
		"audit_anchor_report_interval_seconds":               0,
	}
	if got := seriesCon(t, "audit_anchor_"); !maps.Equal(got, want) {
		t.Fatalf("al nacer: %v, se esperaba %v", got, want)
	}

	m.ReportSchedule(24 * time.Hour)
	m.ReportResult(domain.ReportResultSent)
	m.ReportResult(domain.ReportResultSent)
	m.ReportResult(domain.ReportResultSuppressed)
	m.ReportSucceeded(time.Unix(1758499200, 0))
	want["audit_anchor_report_interval_seconds"] = 86400
	want["audit_anchor_reports_total{result=sent}"] = 2
	want["audit_anchor_reports_total{result=suppressed}"] = 1
	want["audit_anchor_report_last_success_timestamp_seconds"] = 1758499200
	if got := seriesCon(t, "audit_anchor_"); !maps.Equal(got, want) {
		t.Fatalf("tras un informe: %v, se esperaba %v", got, want)
	}

	// Desactivar el informe deja el intervalo a cero, que es lo que apaga la alerta.
	m.ReportSchedule(0)
	if got := seriesCon(t, "audit_anchor_")["audit_anchor_report_interval_seconds"]; got != 0 {
		t.Fatalf("intervalo: %v", got)
	}
}

// Las verificaciones nacen con una serie por origen y desenlace, sin la empresa en ninguna etiqueta.
func TestLasSeriesDeVerificacionNacenACero(t *testing.T) {
	m := metricas()
	got := seriesCon(t, "audit_integrity_")
	for _, origin := range []string{"manual", "sweep", "request"} {
		for _, outcome := range []string{"ok", "broken", "cancelled", "failed"} {
			key := "audit_integrity_runs_total{origin=" + origin + "}{outcome=" + outcome + "}"
			if _, ok := got[key]; !ok {
				t.Fatalf("falta %s en %v", key, got)
			}
		}
	}
	for key := range got {
		if strings.Contains(key, "{tenant") {
			t.Fatalf("una serie lleva la empresa como etiqueta: %s", key)
		}
	}
	m.RunFinished(domain.RunTriggerSweep, domain.RunOutcomeBroken)
	m.SweepBroken(3)
	got = seriesCon(t, "audit_integrity_")
	if got["audit_integrity_runs_total{origin=sweep}{outcome=broken}"] != 1 || got["audit_integrity_sweep_broken_tenants"] != 3 {
		t.Fatalf("%v", got)
	}
}
