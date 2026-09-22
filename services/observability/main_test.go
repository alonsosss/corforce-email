package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	handler "github.com/alonsosss/corforce-email/services/observability/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/observability/internal/app"
	"github.com/alonsosss/corforce-email/services/observability/internal/domain"
	"go.uber.org/zap"
)

const tokenInterno = "token-interno"

type almacen struct {
	Query   string
	Limit   int
	Entries []domain.LogEntry
}

func (a *almacen) QueryRange(_ context.Context, query string, _, _ time.Time, limit int, _ domain.LogDirection) ([]domain.LogEntry, error) {
	a.Query, a.Limit = query, limit
	return a.Entries, nil
}

func router(t *testing.T, store *almacen) http.Handler {
	t.Helper()
	t.Setenv("INTERNAL_GATEWAY_TOKEN", tokenInterno)
	deps := app.LogsDeps{Logger: zap.NewNop()}
	if store != nil {
		deps.Store = store
	}
	uc := app.NewLogsUseCase(deps)
	return apiRouter(handler.NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", "")).Routes(), zap.NewNop())
}

type llamada struct {
	path, roles, user string
	sinToken          bool
	n                 int
}

func pedir(h http.Handler, c llamada) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, c.path, nil)
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", c.n%250+1)
	if !c.sinToken {
		req.Header.Set("X-Gateway-Token", tokenInterno)
	}
	user := c.user
	if user == "" {
		user = "user-1"
	}
	req.Header.Set("X-User-ID", user)
	req.Header.Set("X-Tenant-ID", "11111111-1111-1111-1111-111111111111")
	if c.roles != "" {
		req.Header.Set("X-User-Roles", c.roles)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func codigoDeError(rec *httptest.ResponseRecorder) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Error.Code
}

func TestSoloElSuperadminLlegaAlVisor(t *testing.T) {
	h := router(t, &almacen{})
	for i, ruta := range []string{"/api/v1/observability/logs/services", "/api/v1/observability/logs?service=gateway"} {
		if rec := pedir(h, llamada{path: ruta, roles: "tenant_admin", n: i}); rec.Code != http.StatusForbidden {
			t.Fatalf("tenant_admin en %s: %d %s", ruta, rec.Code, rec.Body)
		}
		if rec := pedir(h, llamada{path: ruta, n: i + 10}); rec.Code != http.StatusForbidden {
			t.Fatalf("sin rol en %s: %d %s", ruta, rec.Code, rec.Body)
		}
		if rec := pedir(h, llamada{path: ruta, roles: "superadmin", sinToken: true, n: i + 20}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("sin token de gateway en %s: %d %s", ruta, rec.Code, rec.Body)
		}
	}
	if rec := pedir(h, llamada{path: "/api/v1/observability/health", n: 30}); rec.Code != http.StatusOK {
		t.Fatalf("salud: %d", rec.Code)
	}
}

func TestLaListaBlancaSeSirveOrdenadaAlSuperadmin(t *testing.T) {
	rec := pedir(router(t, nil), llamada{path: "/api/v1/observability/logs/services", roles: "superadmin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			Services []string `json:"services"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(body.Data.Services, ",")
	for _, s := range []string{"postfix-mail", "gateway", "observability", "rspamd-mail"} {
		if !strings.Contains(","+got+",", ","+s+",") {
			t.Fatalf("falta %s en %s", s, got)
		}
	}
	for i := 1; i < len(body.Data.Services); i++ {
		if body.Data.Services[i-1] >= body.Data.Services[i] {
			t.Fatalf("sin ordenar: %s", got)
		}
	}
}

func TestSinLokiElVisorResponde503YElServicioSigue(t *testing.T) {
	h := router(t, nil)
	rec := pedir(h, llamada{path: "/api/v1/observability/logs?service=gateway", roles: "superadmin"})
	if rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != "NOT_CONFIGURED" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := pedir(h, llamada{path: "/api/v1/observability/logs/services", roles: "superadmin", n: 2}); rec.Code != http.StatusOK {
		t.Fatalf("la lista no depende de Loki: %d", rec.Code)
	}
}

func TestLosParametrosSeValidanAntesDeConsultar(t *testing.T) {
	store := &almacen{}
	h := router(t, store)
	casos := map[string]int{
		"service=postgres-primary":         http.StatusUnprocessableEntity,
		"service=gateway&since=ayer":       http.StatusBadRequest,
		"service=gateway&until=2026-13-01": http.StatusBadRequest,
		"service=gateway&limit=0":          http.StatusBadRequest,
		"service=gateway&limit=x":          http.StatusBadRequest,
		"service=gateway&direction=up":     http.StatusUnprocessableEntity,
		"service=gateway&q=" + "a%0Ab":     http.StatusUnprocessableEntity,
		"service=gateway&since=2030-01-01T00:00:00Z&until=2030-01-02T00:00:00Z": http.StatusUnprocessableEntity,
	}
	n := 0
	for query, want := range casos {
		n++
		if rec := pedir(h, llamada{path: "/api/v1/observability/logs?" + query, roles: "superadmin", n: n}); rec.Code != want {
			t.Errorf("%s: %d %s, se esperaba %d", query, rec.Code, rec.Body, want)
		}
	}
	if store.Query != "" {
		t.Fatalf("una peticion invalida llego al almacen: %q", store.Query)
	}
}

func TestUnaConsultaValidaDevuelveLasLineasConSusEtiquetas(t *testing.T) {
	when := time.Date(2026, 9, 21, 11, 30, 0, 0, time.UTC)
	store := &almacen{Entries: []domain.LogEntry{{Timestamp: when, Line: `to=<a@b.c>, status=sent`, Labels: map[string]string{"contenedor": "app-postfix-mail-1", "flujo": "stdout"}}}}
	h := router(t, store)
	rec := pedir(h, llamada{path: `/api/v1/observability/logs?service=postfix-mail&q=status%3Dsent%22%7D&limit=7&direction=forward`, roles: "superadmin"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if store.Query != `{servicio="postfix-mail"} |= "status=sent\"}"` || store.Limit != 7 {
		t.Fatalf("al almacen llego %q con limite %d", store.Query, store.Limit)
	}
	var body struct {
		Data domain.LogPage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	p := body.Data
	if p.Service != "postfix-mail" || p.Direction != domain.DirectionForward || p.Limit != 7 || p.Truncated || len(p.Entries) != 1 {
		t.Fatalf("pagina: %+v", p)
	}
	if e := p.Entries[0]; !e.Timestamp.Equal(when) || e.Line != `to=<a@b.c>, status=sent` || e.Labels["contenedor"] != "app-postfix-mail-1" {
		t.Fatalf("linea: %+v", e)
	}
	if p.Until.Sub(p.Since) != domain.DefaultLogWindow {
		t.Fatalf("ventana por defecto: %v", p.Until.Sub(p.Since))
	}
}

func TestCadaUsuarioTieneSuCupoDeConsultas(t *testing.T) {
	h := router(t, &almacen{})
	for i := 0; i < 30; i++ {
		if rec := pedir(h, llamada{path: "/api/v1/observability/logs?service=gateway", roles: "superadmin", user: "u-a", n: i}); rec.Code != http.StatusOK {
			t.Fatalf("consulta %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := pedir(h, llamada{path: "/api/v1/observability/logs?service=gateway", roles: "superadmin", user: "u-a", n: 31}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("la 31 debe ser 429: %d", rec.Code)
	}
	if rec := pedir(h, llamada{path: "/api/v1/observability/logs?service=gateway", roles: "superadmin", user: "u-b", n: 32}); rec.Code != http.StatusOK {
		t.Fatalf("otro usuario tiene su cupo: %d", rec.Code)
	}
	if rec := pedir(h, llamada{path: "/api/v1/observability/logs/services", roles: "superadmin", user: "u-a", n: 33}); rec.Code != http.StatusOK {
		t.Fatalf("la lista no consume el cupo de consultas: %d", rec.Code)
	}
}

func TestLaConfiguracionSeValidaAlArrancar(t *testing.T) {
	t.Setenv("ENVIRONMENT", "test")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "")
	t.Setenv("LOKI_URL", "")
	st, err := loadSettings()
	if err != nil || st.port != defaultPort || st.lokiURL != "" {
		t.Fatalf("defectos: %+v %v", st, err)
	}
	t.Setenv("LOKI_URL", "http://loki:3100/")
	if st, err = loadSettings(); err != nil || st.lokiURL != "http://loki:3100" {
		t.Fatalf("LOKI_URL: %+v %v", st, err)
	}
	for _, mala := range []string{"loki:3100", "http://loki:3100/loki", "http://user@loki:3100", "ftp://loki"} {
		t.Setenv("LOKI_URL", mala)
		if _, err := loadSettings(); err == nil {
			t.Fatalf("LOKI_URL=%q debe rechazarse", mala)
		}
	}
	t.Setenv("LOKI_URL", "")
	t.Setenv("OBSERVABILITY_PORT", "70000")
	if _, err := loadSettings(); err == nil {
		t.Fatal("un puerto fuera de rango debe rechazarse")
	}
	t.Setenv("OBSERVABILITY_PORT", "")
	t.Setenv("ENVIRONMENT", "production")
	if _, err := loadSettings(); err == nil {
		t.Fatal("sin token interno en produccion no arranca")
	}
}

func TestSinLokiElCasoDeUsoNoLlevaUnAlmacenNilTipado(t *testing.T) {
	uc := logsUseCase(settings{}, nil, zap.NewNop())
	_, err := uc.Query(context.Background(), true, "u", domain.LogQueryInput{Service: "gateway"})
	if err == nil || !strings.Contains(err.Error(), "no configurada") {
		t.Fatalf("%v", err)
	}
}
