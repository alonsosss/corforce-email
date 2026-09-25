package prometheus

import (
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
)

// ImageProxyMetrics cuenta las peticiones al proxy de imagenes remotas por resultado, un conjunto
// cerrado que fija el dominio (domain.RemoteImageOutcomes).
type ImageProxyMetrics struct {
	requests *prometheus.CounterVec
}

func NewImageProxyMetrics(reg prometheus.Registerer) *ImageProxyMetrics {
	m := &ImageProxyMetrics{requests: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "webmail_image_proxy_requests_total",
		Help: "Peticiones al proxy de imagenes remotas del webmail por resultado (ok, invalid, expired, rate_limited, busy, refused, upstream_error, too_large, not_image).",
	}, []string{"outcome"})}
	reg.MustRegister(m.requests)
	for _, o := range domain.RemoteImageOutcomes {
		m.requests.WithLabelValues(o)
	}
	return m
}

func (m *ImageProxyMetrics) ImageProxyRequest(outcome string) {
	m.requests.WithLabelValues(outcome).Inc()
}
