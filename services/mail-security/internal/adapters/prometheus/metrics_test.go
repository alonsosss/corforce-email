package prometheus

import (
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// seriesDelRepaso devuelve el valor de cada serie del repaso DKIM que expone el registro, por
// nombre y etiquetas.
func seriesDelRepaso(t *testing.T) map[string]float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "mail_security_dkim_reconcile_") {
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

// Las series nacen a cero, cada motivo de retirada con la suya: una alerta sobre increase() ve
// asi la primera retirada. Una pasada completa deja su instante.
func TestLasSeriesDelRepasoNacenACeroYCuentan(t *testing.T) {
	m := New()
	want := map[string]float64{
		"mail_security_dkim_reconcile_removals_total{reason=not_served}":  0,
		"mail_security_dkim_reconcile_removals_total{reason=tenant_gone}": 0,
		"mail_security_dkim_reconcile_unresolved_total":                   0,
		"mail_security_dkim_reconcile_last_success_timestamp_seconds":     0,
	}
	if got := seriesDelRepaso(t); !maps.Equal(got, want) {
		t.Fatalf("al nacer: %v, se esperaba %v", got, want)
	}

	m.DKIMKeysRemoved(domain.DKIMNotServed, 2)
	m.DKIMKeysRemoved(domain.DKIMTenantGone, 0)
	m.DKIMUnresolved(3)
	m.DKIMReconciled(time.Unix(1757955200, 0))
	want["mail_security_dkim_reconcile_removals_total{reason=not_served}"] = 2
	want["mail_security_dkim_reconcile_unresolved_total"] = 3
	want["mail_security_dkim_reconcile_last_success_timestamp_seconds"] = 1757955200
	if got := seriesDelRepaso(t); !maps.Equal(got, want) {
		t.Fatalf("tras una pasada: %v, se esperaba %v", got, want)
	}
}
