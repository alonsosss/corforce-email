package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	handler "github.com/alonsosss/corforce-email/services/mail-directory/internal/adapters/http"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const tokenInterno = "token-interno"

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

// celdaPe01 monta el router de main como la instancia de la celda pe-01, con organization de
// prueba.
func celdaPe01(t *testing.T, cells map[string]string, routes http.Handler) (*tenantcelltest.Organization, http.Handler) {
	t.Helper()
	t.Setenv("INTERNAL_GATEWAY_TOKEN", tokenInterno)
	org, url := tenantcelltest.New(t, tokenInterno, cells)
	m, err := tenantcell.NewMembership("pe-01", tenantcell.NewResolver(url, tokenInterno, zap.NewNop()), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return org, apiRouter(nil, m, routes, zap.NewNop())
}

// rutasReales son las rutas del servicio con un caso de uso sin repositorios ni base: una
// peticion que llegara a un handler de escritura entraria en panico y romperia la prueba.
func rutasReales() chi.Router {
	return handler.NewHandler(app.New(app.Deps{}), authz.NewChecker("http://127.0.0.1:9", "")).Routes()
}

type llamada struct {
	method, path, tenant, user string
	n                          int
}

func pedir(h http.Handler, c llamada) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
	// Una IP por peticion: el limitador del servicio no debe decidir el resultado.
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", c.n%250+1)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", tokenInterno)
	if c.tenant != "" {
		req.Header.Set("X-Tenant-ID", c.tenant)
	}
	if c.user != "" {
		req.Header.Set("X-User-ID", c.user)
		req.Header.Set("X-User-Roles", "tenant_admin")
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

// Cada ruta del servicio, la de una persona por el gateway y la de otro servicio con
// X-Tenant-ID (la activacion de domain-service), rechaza a una empresa de otra celda y a una
// que organization no conoce sin que ningun handler llegue a correr.
func TestCadaRutaRechazaUnaEmpresaQueNoEsDeLaCelda(t *testing.T) {
	ajena, nadie := uuid.NewString(), uuid.NewString()
	org, router := celdaPe01(t, map[string]string{ajena: "pe-02"}, rutasReales())

	var rutas []llamada
	err := chi.Walk(rutasReales(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		rutas = append(rutas, llamada{method: method, path: strings.TrimSuffix(paramRe.ReplaceAllString(route, "x"), "/*")})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	vistas := map[string]bool{}
	n := 0
	for _, ruta := range rutas {
		vistas[strings.TrimSuffix(ruta.path, "/")] = true
		for _, quien := range []llamada{{tenant: ajena, user: uuid.NewString()}, {tenant: ajena}, {tenant: nadie, user: uuid.NewString()}} {
			n++
			c := llamada{method: ruta.method, path: ruta.path, tenant: quien.tenant, user: quien.user, n: n}
			if rec := pedir(router, c); rec.Code != http.StatusForbidden || codigoDeError(rec) != tenantcell.CodeNotInCell {
				t.Fatalf("%s %s como %+v: %d %s", c.method, c.path, quien, rec.Code, rec.Body)
			}
		}
	}
	for _, imprescindible := range []string{"/api/v1/mailboxes", "/api/v1/mail-domains", "/api/v1/mail-routing/relayhosts",
		"/internal/mail-directory/domains/x/activation", "/internal/mail-directory/tenant-retirement"} {
		if !vistas[imprescindible] {
			t.Fatalf("la prueba no recorrio %s (%d rutas)", imprescindible, len(rutas))
		}
	}
	// Dos empresas, cada una una sola vez: la cache sirve el resto.
	if org.Calls() != 2 {
		t.Fatalf("consultas a organization: %d", org.Calls())
	}
}

func TestLaEmpresaDeLaCeldaLlegaASusRutas(t *testing.T) {
	socia, nueva := uuid.NewString(), uuid.NewString()
	org, router := celdaPe01(t, map[string]string{socia: "pe-01", nueva: "pe-01"}, rutasReales())
	meta := llamada{method: http.MethodGet, path: "/api/v1/mail-directory/meta", tenant: socia, user: uuid.NewString()}
	if rec := pedir(router, meta); rec.Code != http.StatusOK {
		t.Fatalf("empresa de la celda: %d %s", rec.Code, rec.Body)
	}

	// organization caido: la empresa ya comprobada sigue; la que no, 503 sin llegar a la ruta.
	org.SetDown(true)
	if rec := pedir(router, meta); rec.Code != http.StatusOK {
		t.Fatalf("empresa comprobada con organization caido: %d %s", rec.Code, rec.Body)
	}
	meta.tenant = nueva
	if rec := pedir(router, meta); rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != tenantcell.CodeCellUnavailable {
		t.Fatalf("empresa sin comprobar con organization caido: %d %s", rec.Code, rec.Body)
	}
}

// mail-directory no declara rutas de plataforma: una peticion con celda destino (la de un
// operador) se rechaza en todas sus rutas sin llegar a ningun handler ni preguntar a organization,
// aunque quien llama sea superadmin y la celda sea esta. Sus rutas son de datos de empresa.
func TestNingunaRutaAtiendeAUnOperadorConCeldaDestino(t *testing.T) {
	plataforma := uuid.NewString()
	org, router := celdaPe01(t, map[string]string{plataforma: "pe-02"}, rutasReales())
	n := 0
	err := chi.Walk(rutasReales(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		n++
		req := httptest.NewRequest(method, strings.TrimSuffix(paramRe.ReplaceAllString(route, "x"), "/*"), strings.NewReader(`{}`))
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", n%250+1)
		req.Header.Set("X-Gateway-Token", tokenInterno)
		req.Header.Set("X-Tenant-ID", plataforma)
		req.Header.Set("X-User-ID", uuid.NewString())
		req.Header.Set("X-User-Roles", "superadmin")
		req.Header.Set("X-Operator-Cell", "pe-01")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || codigoDeError(rec) != tenantcell.CodePlatformScopeOnly {
			t.Fatalf("%s %s con celda destino: %d %s", method, route, rec.Code, rec.Body)
		}
		return nil
	})
	if err != nil || n < 10 {
		t.Fatalf("rutas recorridas %d: %v", n, err)
	}
	if org.Calls() != 0 {
		t.Fatalf("consultas a organization: %d", org.Calls())
	}
}

// Lo que no actua por ninguna empresa sigue igual: la consulta de remitentes del webmail no
// lleva X-Tenant-ID y no pregunta a organization; sin el token interno nada entra.
func TestLasLlamadasSinEmpresaNoSeComprueban(t *testing.T) {
	atendidas := 0
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atendidas++
		w.WriteHeader(http.StatusOK)
	})
	org, router := celdaPe01(t, nil, stub)
	c := llamada{method: http.MethodGet, path: "/internal/mail-directory/sender-identities?username=ana@acme.test"}
	if rec := pedir(router, c); rec.Code != http.StatusOK || atendidas != 1 || org.Calls() != 0 {
		t.Fatalf("remitentes del webmail: %d, atendidas %d, consultas %d", rec.Code, atendidas, org.Calls())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mailboxes", nil)
	req.Header.Set("X-Tenant-ID", uuid.NewString())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || atendidas != 1 || org.Calls() != 0 {
		t.Fatalf("sin token interno: %d, atendidas %d, consultas %d", rec.Code, atendidas, org.Calls())
	}
}

func TestDAVServerURLFromEnv(t *testing.T) {
	cases := []struct {
		name, env, raw string
		want           string
		wantErr        bool
	}{
		{"sin configurar no ofrece nada", "production", "", "", false},
		{"https con barra final", "production", "https://mail.acme.test/api/v1/dav/", "https://mail.acme.test/api/v1/dav/", false},
		{"anade la barra final", "production", "https://mail.acme.test/api/v1/dav", "https://mail.acme.test/api/v1/dav/", false},
		{"http en produccion", "production", "http://mail.acme.test/api/v1/dav/", "", true},
		{"http en desarrollo", "development", "http://localhost:8080/api/v1/dav/", "http://localhost:8080/api/v1/dav/", false},
		{"con credenciales", "production", "https://ana:clave@mail.acme.test/api/v1/dav/", "", true},
		{"con consulta", "production", "https://mail.acme.test/api/v1/dav/?x=1", "", true},
		{"con fragmento", "production", "https://mail.acme.test/api/v1/dav/#x", "", true},
		{"sin host", "production", "https:///api/v1/dav/", "", true},
		{"otro esquema", "production", "ftp://mail.acme.test/dav/", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ENVIRONMENT", c.env)
			t.Setenv("MAIL_DAV_PUBLIC_URL", c.raw)
			got, err := davServerURLFromEnv()
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("got %q, %v; quiero %q, error=%v", got, err, c.want, c.wantErr)
			}
		})
	}
}
