package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// observacionesDeLatencia cuenta cuantas muestras lleva el histograma para una ruta.
func observacionesDeLatencia(t *testing.T, metodo, ruta string) uint64 {
	t.Helper()
	m := &dto.Metric{}
	obs, err := duration.GetMetricWithLabelValues(metodo, ruta)
	if err != nil {
		t.Fatalf("no se pudo leer el histograma: %v", err)
	}
	if err := obs.(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("no se pudo volcar el histograma: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

// Una conexion WebSocket dura lo que dura la conversacion. Si su duracion entra en el
// histograma, la p95 del servicio se queda pegada al ultimo bucket mientras haya un equipo
// conectado y la alerta de latencia no puede apagarse. Paso en produccion con el servicio
// de balanzas: el 100% de las muestras caia en +Inf con el servicio sano.
func TestElUpgradeDeWebSocketNoEntraEnLaLatencia(t *testing.T) {
	const ruta = "/ws/edge"

	antes := observacionesDeLatencia(t, http.MethodGet, ruta)
	antesPeticiones := 0.0
	if c, err := requests.GetMetricWithLabelValues(http.MethodGet, ruta, "200"); err == nil {
		m := &dto.Metric{}
		if c.Write(m) == nil {
			antesPeticiones = m.GetCounter().GetValue()
		}
	}

	// El handler se comporta como el de un socket: no vuelve hasta que la conversacion
	// termina. Se simula con una espera corta que, medida, caeria fuera del primer bucket.
	handler := HTTPMetrics()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, ruta, nil)
	req.Header.Set("Upgrade", "websocket")
	// Con varios valores y en otro orden, como lo manda algun proxy.
	req.Header.Set("Connection", "keep-alive, Upgrade")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got := observacionesDeLatencia(t, http.MethodGet, ruta); got != antes {
		t.Errorf("la conexion WebSocket entro en el histograma de latencia: %d -> %d", antes, got)
	}

	// Pero la conexion SI se cuenta: saber cuantas se abren es util.
	c, err := requests.GetMetricWithLabelValues(http.MethodGet, ruta, "200")
	if err != nil {
		t.Fatalf("no se pudo leer el contador: %v", err)
	}
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("no se pudo volcar el contador: %v", err)
	}
	if m.GetCounter().GetValue() != antesPeticiones+1 {
		t.Errorf("la conexion WebSocket no se conto: %v -> %v", antesPeticiones, m.GetCounter().GetValue())
	}
}

// Una peticion normal se sigue midiendo: el arreglo no puede dejar ciego al histograma.
func TestUnaPeticionNormalSiEntraEnLaLatencia(t *testing.T) {
	const ruta = "/api/v1/algo"

	antes := observacionesDeLatencia(t, http.MethodGet, ruta)

	handler := HTTPMetrics()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ruta, nil))

	if got := observacionesDeLatencia(t, http.MethodGet, ruta); got != antes+1 {
		t.Errorf("la peticion normal no se midio: %d -> %d", antes, got)
	}
}

// Una cabecera Upgrade que no es de WebSocket no exime de medir.
func TestOtroUpgradeSiEntraEnLaLatencia(t *testing.T) {
	const ruta = "/api/v1/otro"

	antes := observacionesDeLatencia(t, http.MethodGet, ruta)

	handler := HTTPMetrics()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, ruta, nil)
	req.Header.Set("Upgrade", "h2c")
	req.Header.Set("Connection", "Upgrade")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got := observacionesDeLatencia(t, http.MethodGet, ruta); got != antes+1 {
		t.Errorf("un upgrade que no es WebSocket dejo de medirse: %d -> %d", antes, got)
	}
}
