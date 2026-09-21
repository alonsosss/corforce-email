package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/observability"
)

// El borde reenvia al gateway toda ruta del dominio publico, y las operativas de WithOps se
// resuelven antes que el router: sin esta guardia, https://<dominio publico>/metrics entrega las
// metricas internas del gateway (rutas, modulos, celdas, denegaciones) a cualquiera. El borde escribe
// siempre X-Real-IP; el recolector de metricas llega desde la red interna sin esa cabecera.
func TestLasMetricasDelGatewayNoSalenPorElBorde(t *testing.T) {
	const ops, app = "ops", "app"
	h := internalOps(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(ops)) }),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(app)) }),
	)
	get := func(path string, headers ...string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	if got := get(observability.MetricsPath); got != ops {
		t.Fatalf("el recolector interno debe poder leer las metricas: %q", got)
	}
	for _, header := range []string{"X-Real-IP", "X-Forwarded-For"} {
		if got := get(observability.MetricsPath, header, "203.0.113.9"); got != app {
			t.Fatalf("una peticion que llega por el borde (%s) no recibe las metricas: %q", header, got)
		}
	}
	if got := get(observability.HealthPath, "X-Real-IP", "203.0.113.9"); got != ops {
		t.Fatalf("la salud del proceso no revela nada y sigue disponible: %q", got)
	}
	if got := get("/api/v1/users", "X-Real-IP", "203.0.113.9"); got != ops {
		t.Fatalf("el resto de rutas sigue midiendose: %q", got)
	}
}
