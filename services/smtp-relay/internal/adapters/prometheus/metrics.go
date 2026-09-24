// Package prometheus expone las metricas del relay SMTP. Sin empresa ni direccion en las
// etiquetas: son datos personales y dispararian la cardinalidad.
package prometheus

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	connections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "smtp_relay_connections_total",
		Help: "Conexiones SMTP recibidas por puerto (starttls o tls).",
	}, []string{"listener"})
	auths = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "smtp_relay_auth_total",
		Help: "Autenticaciones SMTP por resultado (ok, failed, blocked, unavailable).",
	}, []string{"result"})
	rejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "smtp_relay_rejections_total",
		Help: "Rechazos del relay por etapa de la sesion y motivo.",
	}, []string{"stage", "reason"})
	delivered = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "smtp_relay_messages_delivered_total",
		Help: "Mensajes aceptados por transactional desde el relay.",
	})
	deliveredBytes = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "smtp_relay_message_bytes",
		Help:    "Tamano de los mensajes aceptados.",
		Buckets: prometheus.ExponentialBuckets(4<<10, 4, 8),
	})
	deliverySeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "smtp_relay_delivery_seconds",
		Help:    "Tiempo desde el fin del DATA hasta la respuesta de transactional, analisis incluido.",
		Buckets: prometheus.DefBuckets,
	})
)

func init() {
	prometheus.MustRegister(connections, auths, rejections, delivered, deliveredBytes, deliverySeconds)
}

// Metrics implementa ports.Metrics.
type Metrics struct{}

func New() Metrics { return Metrics{} }

func (Metrics) Connection(listener string)    { connections.WithLabelValues(listener).Inc() }
func (Metrics) Auth(result string)            { auths.WithLabelValues(result).Inc() }
func (Metrics) Rejected(stage, reason string) { rejections.WithLabelValues(stage, reason).Inc() }

// RegisterCertificateExpiry publica la caducidad del certificado servido (epoch unix): la alerta
// avisa antes de que las integraciones empiecen a fallar el TLS.
func RegisterCertificateExpiry(notAfter func() time.Time) {
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "smtp_relay_certificate_expiry_timestamp_seconds",
		Help: "Caducidad del certificado TLS que sirve el relay.",
	}, func() float64 { return float64(notAfter().Unix()) }))
}

func (Metrics) Delivered(size int, took time.Duration) {
	delivered.Inc()
	deliveredBytes.Observe(float64(size))
	deliverySeconds.Observe(took.Seconds())
}
