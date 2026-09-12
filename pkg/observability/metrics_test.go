package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestSanitizePath(t *testing.T) {
	cases := map[string]string{
		"/api/v1/loans/9b3d1f0e-1c2a-4f5b-8d7e-0a1b2c3d4e5f/payments": "/api/v1/loans/:id/payments",
		"/api/v1/products/42":        "/api/v1/products/:id",
		"/api/v1/products":           "/api/v1/products",
		"/a/b/c/d/e/f/g/h/i/j/k/l/m": "/other",
	}
	for in, want := range cases {
		if got := sanitizePath(in); got != want {
			t.Errorf("sanitizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// El valor de la instrumentacion depende de que la etiqueta sea el PATRON de la ruta: si
// cayera la URL concreta, cada identificador crearia su propia serie y el almacen de
// metricas se degradaria por cardinalidad.
func TestHTTPMetricsUsesRoutePattern(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/loans/{id}/payments", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	h := HTTPMetrics()(r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/loans/7f0b1e2c-3d4a-4b5c-8d9e-0f1a2b3c4d5e/payments", nil))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if got := counterValue("GET", "/loans/{id}/payments", "201"); got != 1 {
		t.Errorf("http_requests_total{route=\"/loans/{id}/payments\"} = %v, want 1", got)
	}
}

// Un handler que no es un router chi (o una ruta que no casa) no puede quedarse sin
// medicion: cae al respaldo que colapsa los identificadores.
func TestHTTPMetricsFallsBackToSanitizedPath(t *testing.T) {
	h := HTTPMetrics()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhook/123", nil))

	// Sin WriteHeader explicito el estado es 200 implicito.
	if got := counterValue("POST", "/webhook/:id", "200"); got != 1 {
		t.Errorf("http_requests_total{route=\"/webhook/:id\"} = %v, want 1", got)
	}
}

// Las rutas operativas no deben atravesar el router del servicio ni su cadena de
// middlewares: el recolector y el healthcheck del contenedor no tienen credenciales.
func TestWithOpsServesOperationalRoutesOutsideTheRouter(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // simula el gateway/token exigido por el servicio
	})
	h := WithOps(inner)

	for _, path := range []string{HealthPath, MetricsPath} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("ruta de negocio: status = %d, want 403 (debe pasar por el servicio)", rec.Code)
	}
}

func TestMetricsEndpointExposesRequestSeries(t *testing.T) {
	h := WithOps(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MetricsPath, nil))
	if !strings.Contains(rec.Body.String(), "http_requests_total") {
		t.Error("/metrics no expone http_requests_total")
	}
}

func counterValue(method, route, status string) float64 {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return -1
	}
	for _, f := range families {
		if f.GetName() != "http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			if labelsMatch(m, map[string]string{
				"method": method, "route": route, "status": status,
			}) {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
