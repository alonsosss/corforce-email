package mailreconcile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/google/uuid"
)

const testToken = "token-de-gateway-de-prueba"

func directoryFor(t *testing.T, handler http.HandlerFunc) *DirectoryClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cells, err := tenantcell.LoadInstances(func(string) string { return "" }, tenantcell.BaseCellEnv, "MAIL_DIRECTORY_CELL_HOSTS")
	if err != nil {
		t.Fatal(err)
	}
	targets, err := cells.Targets("MAIL_DIRECTORY_CELL_HOSTS", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewDirectory(tenantcell.NewCaller("mail-directory", targets, testToken, nil, tenantcell.CallerOptions{Timeout: 2 * time.Second, MaxAttempts: 1}))
}

func TestPreguntaPorLaEmpresaConElTokenInternoYDevuelveSoloLoPreguntado(t *testing.T) {
	tenant := uuid.New()
	asked, extra := []uuid.UUID{uuid.New(), uuid.New()}, uuid.New()
	var gotBody map[string][]uuid.UUID
	c := directoryFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/mail-directory/mailboxes/existence" ||
			r.Header.Get("X-Gateway-Token") != testToken || r.Header.Get("X-Tenant-ID") != tenant.String() {
			t.Errorf("peticion inesperada: %s %s %v", r.Method, r.URL.Path, r.Header)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"existing": []uuid.UUID{asked[0], extra}}})
	})
	got, err := c.Existing(context.Background(), tenant, asked)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[asked[0]]; !ok || len(got) != 1 {
		t.Fatalf("existentes: %v (un id no preguntado no cuenta)", got)
	}
	if len(gotBody["ids"]) != 2 {
		t.Fatalf("cuerpo: %v", gotBody)
	}
}

func TestUnaListaVaciaEsUnaRespuestaValida(t *testing.T) {
	c := directoryFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"existing":[]}}`))
	})
	got, err := c.Existing(context.Background(), uuid.New(), []uuid.UUID{uuid.New()})
	if err != nil || len(got) != 0 {
		t.Fatalf("lista vacia: %v %v", got, err)
	}
}

// Lo que no es una respuesta correcta y completa nunca puede leerse como "ninguno existe": el barrido borraria
// los datos de buzones vivos.
func TestNadaQueNoSeaUnaRespuestaCompletaSeLeeComoAusencia(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"500":                   {http.StatusInternalServerError, ``},
		"503":                   {http.StatusServiceUnavailable, `{"error":{}}`},
		"401":                   {http.StatusUnauthorized, ``},
		"404":                   {http.StatusNotFound, ``},
		"422":                   {http.StatusUnprocessableEntity, ``},
		"200 sin cuerpo":        {http.StatusOK, ``},
		"200 ilegible":          {http.StatusOK, `<html>`},
		"200 sin datos":         {http.StatusOK, `{}`},
		"200 sin lista":         {http.StatusOK, `{"data":{}}`},
		"200 con lista nula":    {http.StatusOK, `{"data":{"existing":null}}`},
		"200 con ids invalidos": {http.StatusOK, `{"data":{"existing":["no-es-un-id"]}}`},
	} {
		c := directoryFor(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		if got, err := c.Existing(context.Background(), uuid.New(), []uuid.UUID{uuid.New()}); err == nil {
			t.Errorf("%s: se tomo por una respuesta (existentes: %v)", name, got)
		}
	}
}

func TestUnDirectorioCaidoEsUnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	cells, _ := tenantcell.LoadInstances(func(string) string { return "" }, tenantcell.BaseCellEnv, "MAIL_DIRECTORY_CELL_HOSTS")
	targets, err := cells.Targets("MAIL_DIRECTORY_CELL_HOSTS", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := NewDirectory(tenantcell.NewCaller("mail-directory", targets, testToken, nil, tenantcell.CallerOptions{Timeout: time.Second, MaxAttempts: 1}))
	if _, err := c.Existing(context.Background(), uuid.New(), []uuid.UUID{uuid.New()}); err == nil {
		t.Fatal("un directorio caido no es una respuesta")
	}
}

func TestLaRespuestaDesmesuradaSeAcota(t *testing.T) {
	c := directoryFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"existing":["` + strings.Repeat("a", 1<<20) + `"]}}`))
	})
	if _, err := c.Existing(context.Background(), uuid.New(), []uuid.UUID{uuid.New()}); err == nil {
		t.Fatal("una respuesta de un megabyte no es valida")
	}
}

func TestConfiguracionPorDefectoYLimites(t *testing.T) {
	cfg, err := ConfigFromEnv("PRUEBA")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled() || cfg.Interval != 6*time.Hour || cfg.Grace != 24*time.Hour || cfg.MaxPurgesPerTenant != 100 || cfg.BatchSize != DefaultBatchSize {
		t.Fatalf("defectos: %+v", cfg)
	}

	t.Setenv("PRUEBA_RECONCILE_INTERVAL", "0")
	if cfg, err := ConfigFromEnv("PRUEBA"); err != nil || cfg.Enabled() {
		t.Fatalf("con 0 debe quedar desactivado: %+v %v", cfg, err)
	}
	for name, env := range map[string][2]string{
		"intervalo menor de un minuto": {"PRUEBA_RECONCILE_INTERVAL", "30s"},
		"intervalo ilegible":           {"PRUEBA_RECONCILE_INTERVAL", "mucho"},
		"gracia menor que el retraso":  {"PRUEBA_RECONCILE_GRACE", "1m"},
		"gracia enorme":                {"PRUEBA_RECONCILE_GRACE", "9999h"},
		"tope cero":                    {"PRUEBA_RECONCILE_MAX_PURGES_PER_TENANT", "0"},
		"tope negativo":                {"PRUEBA_RECONCILE_MAX_PURGES_PER_TENANT", "-5"},
	} {
		t.Setenv("PRUEBA_RECONCILE_INTERVAL", "1h")
		t.Setenv(env[0], env[1])
		if _, err := ConfigFromEnv("PRUEBA"); err == nil {
			t.Errorf("%s: se acepto %s=%s", name, env[0], env[1])
		}
	}
}
