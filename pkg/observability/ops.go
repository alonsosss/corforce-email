package observability

import (
	"net/http"
	"os"
)

// WithOps antepone al router de un servicio las dos rutas operativas de la plataforma:
// /healthz (liveness del proceso) y /metrics (exposicion Prometheus), y mide todo lo
// demas. Se resuelve por comparacion exacta de ruta, sin un multiplexor intermedio, para
// no alterar en nada la semantica de ruteo del servicio (limpieza de rutas, redirecciones
// o escapes) que ya esta en produccion.
//
// Ambas rutas quedan por FUERA de la cadena de middlewares del servicio: ni el recolector
// ni el healthcheck del contenedor tienen token de gateway o sesion. Solo son alcanzables
// desde la red interna de Docker y desde el loopback del host (los puertos de servicio se
// publican en 127.0.0.1); el gateway nunca las expone, porque solo reenvia /api/v1.
//
// METRICS_DISABLED=true apaga la medicion del trafico (no las rutas operativas, de las que
// depende el healthcheck del contenedor): valvula de escape si el coste de medir molestara
// en un servicio concreto, sin desplegar codigo distinto.
func WithOps(handler http.Handler) http.Handler {
	service := ServiceName()

	metered := handler
	if os.Getenv("METRICS_DISABLED") != "true" {
		metered = HTTPMetrics()(handler)
	}
	metrics := Handler()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case HealthPath:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","service":"` + service + `"}`))
			return
		case MetricsPath:
			metrics.ServeHTTP(w, r)
			return
		}
		metered.ServeHTTP(w, r)
	})
}
