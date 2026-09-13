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
	handler "github.com/alonsosss/corforce-email/services/mail-security/internal/adapters/http"
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

// rutasReales son las rutas del servicio sin casos de uso: una peticion que llegara a un
// handler que lee o escribe entraria en panico y romperia la prueba.
func rutasReales() http.Handler {
	return handler.NewHandler(nil, nil, nil, authz.NewChecker("http://127.0.0.1:9", "")).Routes()
}

type llamada struct {
	method, path, tenant, user string
	n                          int
}

func pedir(h http.Handler, c llamada) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
	// Una IP por peticion: los limitadores del servicio no deben decidir el resultado.
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

// Cada ruta del API de administracion y las internas de DKIM (domain-service, con
// X-Tenant-ID) rechazan a una empresa de otra celda y a una desconocida sin llegar a ningun
// handler.
func TestCadaRutaRechazaUnaEmpresaQueNoEsDeLaCelda(t *testing.T) {
	ajena, nadie := uuid.NewString(), uuid.NewString()
	org, router := celdaPe01(t, map[string]string{ajena: "pe-02"}, rutasReales())

	var rutas []llamada
	err := chi.Walk(rutasReales().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
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
	for _, imprescindible := range []string{"/api/v1/mail-security/quarantine-settings", "/api/v1/mail-security/quarantine/x/release", "/api/v1/mail-security/firewall/networks", "/internal/mail-security/dkim/x"} {
		if !vistas[imprescindible] {
			t.Fatalf("la prueba no recorrio %s (%d rutas)", imprescindible, len(rutas))
		}
	}
	if org.Calls() != 2 {
		t.Fatalf("consultas a organization: %d", org.Calls())
	}
}

func TestLaEmpresaDeLaCeldaLlegaASusRutas(t *testing.T) {
	socia, nueva := uuid.NewString(), uuid.NewString()
	org, router := celdaPe01(t, map[string]string{socia: "pe-01", nueva: "pe-01"}, rutasReales())
	salud := llamada{method: http.MethodGet, path: "/api/v1/mail-security/health", tenant: socia, user: uuid.NewString()}
	if rec := pedir(router, salud); rec.Code != http.StatusOK {
		t.Fatalf("empresa de la celda: %d %s", rec.Code, rec.Body)
	}
	org.SetDown(true)
	if rec := pedir(router, salud); rec.Code != http.StatusOK {
		t.Fatalf("empresa comprobada con organization caido: %d %s", rec.Code, rec.Body)
	}
	salud.tenant = nueva
	if rec := pedir(router, salud); rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != tenantcell.CodeCellUnavailable {
		t.Fatalf("empresa sin comprobar con organization caido: %d %s", rec.Code, rec.Body)
	}
}

// Los enlaces publicos de cuarentena no llevan empresa del gateway: su celda va en la firma y
// la comprueba el caso de uso. Pasan sin preguntar a organization y responden su propia pagina.
func TestLosEnlacesPublicosNoPreguntanLaCelda(t *testing.T) {
	org, router := celdaPe01(t, nil, rutasReales())
	rec := pedir(router, llamada{method: http.MethodGet, path: "/api/v1/public/mail-security/quarantine/pe-01/release"})
	if rec.Code != http.StatusForbidden || codigoDeError(rec) != "" || !strings.Contains(rec.Body.String(), "<") || org.Calls() != 0 {
		t.Fatalf("enlace publico: %d %q, consultas %d", rec.Code, rec.Body, org.Calls())
	}
}
