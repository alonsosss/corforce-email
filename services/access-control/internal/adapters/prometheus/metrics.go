// Package prometheus expone las metricas de las claves de API de access-control.
package prometheus

import "github.com/prometheus/client_golang/prometheus"

// resolutions cuenta las resoluciones de claves por resultado (ok, error o el motivo del
// rechazo). Un pico de "secret" o "unknown" es alguien probando claves; uno de "owner" tras un
// cambio de roles, integraciones que se quedaron sin dueno.
var resolutions = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "access_control_api_key_resolutions_total",
	Help: "Resoluciones de claves de API por resultado.",
}, []string{"result"})

func init() { prometheus.MustRegister(resolutions) }

// Metrics implementa ports.APIKeyMetrics.
type Metrics struct{}

func New() Metrics { return Metrics{} }

func (Metrics) Resolved(result string) { resolutions.WithLabelValues(result).Inc() }
