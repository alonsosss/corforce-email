package prometheus

import (
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestImageProxyMetricsCuentaPorResultado(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewImageProxyMetrics(reg)
	if n := testutil.CollectAndCount(m.requests); n != len(domain.RemoteImageOutcomes) {
		t.Fatalf("una serie a cero por resultado: %d", n)
	}
	m.ImageProxyRequest(domain.RemoteImageOutcomeOK)
	m.ImageProxyRequest(domain.RemoteImageOutcomeOK)
	m.ImageProxyRequest(domain.RemoteImageOutcomeRefused)
	if got := testutil.ToFloat64(m.requests.WithLabelValues(domain.RemoteImageOutcomeOK)); got != 2 {
		t.Fatalf("ok: %v", got)
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues(domain.RemoteImageOutcomeRefused)); got != 1 {
		t.Fatalf("refused: %v", got)
	}
}
