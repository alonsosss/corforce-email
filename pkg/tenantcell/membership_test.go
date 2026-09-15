package tenantcell

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// servicioDeCelda es un servicio de la celda pe-01 con la barrera montada como en main: detras
// de InjectFromGateway, delante de un handler que cuenta lo que le llega.
type servicioDeCelda struct {
	org      *tenantcelltest.Organization
	resolver *Resolver
	reloj    *relojFijo
	handler  http.Handler
	atendido int
}

func nuevoServicio(t *testing.T, cells map[string]string) *servicioDeCelda {
	t.Helper()
	org, url := tenantcelltest.New(t, tokenDePrueba, cells)
	resolver, reloj := nuevoResolver(url)
	m, err := NewMembership("pe-01", resolver, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	s := &servicioDeCelda{org: org, resolver: resolver, reloj: reloj}
	s.handler = middleware.InjectFromGateway(m.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.atendido++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"secreto":"de la celda"}}`))
	})))
	return s
}

type peticion struct{ tenant, user string }

func (s *servicioDeCelda) pedir(p peticion) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mailboxes", strings.NewReader(`{}`))
	if p.tenant != "" {
		req.Header.Set("X-Tenant-ID", p.tenant)
	}
	if p.user != "" {
		req.Header.Set("X-User-ID", p.user)
		req.Header.Set("X-User-Roles", "tenant_admin")
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

// rechazada comprueba el estado, el codigo estable, que no sale ningun dato y que el handler
// no se ejecuto.
func (s *servicioDeCelda) rechazada(t *testing.T, caso string, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Data  json.RawMessage `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: cuerpo ilegible %q", caso, rec.Body)
	}
	if rec.Code != status || body.Error.Code != code || len(body.Data) != 0 || strings.Contains(rec.Body.String(), "secreto") {
		t.Fatalf("%s: %d %s, se esperaba %d %s sin datos", caso, rec.Code, rec.Body, status, code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s: el rechazo no lleva Cache-Control: no-store", caso)
	}
	if s.atendido != 0 {
		t.Fatalf("%s: el handler atendio %d peticiones", caso, s.atendido)
	}
}

func TestSoloPasanLasEmpresasDeLaCelda(t *testing.T) {
	socia, ajena, nadie := uuid.NewString(), uuid.NewString(), uuid.NewString()
	s := nuevoServicio(t, map[string]string{socia: "pe-01", ajena: "pe-02"})
	usuario := uuid.NewString()

	if rec := s.pedir(peticion{tenant: socia, user: usuario}); rec.Code != http.StatusOK || s.atendido != 1 {
		t.Fatalf("empresa de la celda: %d, atendidas %d", rec.Code, s.atendido)
	}
	// La llamada de un servicio con la empresa (domain-service) se comprueba igual.
	if rec := s.pedir(peticion{tenant: socia}); rec.Code != http.StatusOK || s.atendido != 2 {
		t.Fatalf("servicio con empresa de la celda: %d", rec.Code)
	}
	s.atendido = 0

	foreign, unknown := valorRechazos(reasonForeign), valorRechazos(reasonUnknown)
	s.rechazada(t, "empresa de otra celda", s.pedir(peticion{tenant: ajena, user: usuario}), http.StatusForbidden, CodeNotInCell)
	s.rechazada(t, "servicio con empresa de otra celda", s.pedir(peticion{tenant: ajena}), http.StatusForbidden, CodeNotInCell)
	s.rechazada(t, "empresa desconocida", s.pedir(peticion{tenant: nadie, user: usuario}), http.StatusForbidden, CodeNotInCell)
	s.rechazada(t, "empresa mal formada", s.pedir(peticion{tenant: "../pe-01", user: usuario}), http.StatusForbidden, CodeNotInCell)
	s.rechazada(t, "persona sin empresa", s.pedir(peticion{user: usuario}), http.StatusForbidden, CodeNotInCell)
	if got := valorRechazos(reasonForeign); got != foreign+2 {
		t.Fatalf("foreign_tenant: %v, se esperaba %v", got, foreign+2)
	}
	if got := valorRechazos(reasonUnknown); got != unknown+3 {
		t.Fatalf("unknown_tenant: %v, se esperaba %v", got, unknown+3)
	}
}

func TestSinEmpresaNiUsuarioNoHayNadaQueComprobar(t *testing.T) {
	s := nuevoServicio(t, nil)
	if rec := s.pedir(peticion{}); rec.Code != http.StatusOK || s.atendido != 1 || s.org.Calls() != 0 {
		t.Fatalf("peticion sin empresa: %d, atendidas %d, consultas %d", rec.Code, s.atendido, s.org.Calls())
	}
}

func TestConOrganizationCaidoSoloPasaLaEmpresaYaComprobada(t *testing.T) {
	socia, ajena, nueva := uuid.NewString(), uuid.NewString(), uuid.NewString()
	s := nuevoServicio(t, map[string]string{socia: "pe-01", ajena: "pe-02", nueva: "pe-01"})
	usuario := uuid.NewString()
	if rec := s.pedir(peticion{tenant: socia, user: usuario}); rec.Code != http.StatusOK {
		t.Fatalf("empresa de la celda: %d", rec.Code)
	}
	if rec := s.pedir(peticion{tenant: ajena, user: usuario}); rec.Code != http.StatusForbidden {
		t.Fatalf("empresa de otra celda: %d", rec.Code)
	}
	s.org.SetDown(true)
	s.reloj.avanzar(cacheTTL + time.Minute)
	s.atendido = 0

	if rec := s.pedir(peticion{tenant: socia, user: usuario}); rec.Code != http.StatusOK || s.atendido != 1 {
		t.Fatalf("empresa ya comprobada con organization caido: %d", rec.Code)
	}
	s.atendido = 0
	// La ultima respuesta de otra celda sigue rechazando: la cache nunca abre, solo sostiene.
	s.rechazada(t, "empresa de otra celda con organization caido", s.pedir(peticion{tenant: ajena, user: usuario}), http.StatusForbidden, CodeNotInCell)

	unresolved := valorRechazos(reasonUnresolved)
	s.rechazada(t, "empresa sin comprobar con organization caido", s.pedir(peticion{tenant: nueva, user: usuario}), http.StatusServiceUnavailable, CodeCellUnavailable)
	if got := valorRechazos(reasonUnresolved); got != unresolved+1 {
		t.Fatalf("unresolved: %v", got)
	}

	// Pasado el margen tampoco pasa la que estaba comprobada.
	s.reloj.avanzar(staleGrace)
	s.rechazada(t, "empresa comprobada pasado el margen", s.pedir(peticion{tenant: socia, user: usuario}), http.StatusServiceUnavailable, CodeCellUnavailable)
}

func TestLaCeldaDeLaInstanciaDebeSerUnCodigo(t *testing.T) {
	r := NewResolver("http://organization:8003", tokenDePrueba, zap.NewNop())
	for _, cell := range []string{"", "PE-01", "pe_01", "../pe-01"} {
		if _, err := NewMembership(cell, r, zap.NewNop()); err == nil {
			t.Errorf("celda %q aceptada", cell)
		}
	}
	if _, err := NewMembership("pe-01", nil, zap.NewNop()); err == nil {
		t.Error("sin resolvedor aceptado")
	}
}

func TestMembershipFromEnvFallaCerrado(t *testing.T) {
	t.Setenv("INTERNAL_GATEWAY_TOKEN", tokenDePrueba)
	for nombre, env := range map[string][2]string{
		"sin CELL_CODE":               {"", "http://organization:8003"},
		"CELL_CODE invalido":          {"PE-01", "http://organization:8003"},
		"sin ORGANIZATION_URL":        {"pe-01", ""},
		"ORGANIZATION_URL sin host":   {"pe-01", "http://"},
		"ORGANIZATION_URL sin http":   {"pe-01", "organization:8003"},
		"ORGANIZATION_URL de fichero": {"pe-01", "file:///etc/passwd"},
		"ORGANIZATION_URL con query":  {"pe-01", "http://organization:8003?x=1"},
		"ORGANIZATION_URL con ruta":   {"pe-01", "http://organization:8003/api"},
	} {
		t.Setenv("CELL_CODE", env[0])
		t.Setenv("ORGANIZATION_URL", env[1])
		if _, err := MembershipFromEnv(zap.NewNop()); err == nil {
			t.Errorf("%s: arranco", nombre)
		}
	}

	t.Setenv("CELL_CODE", " pe-01 ")
	t.Setenv("ORGANIZATION_URL", "http://organization:8003/")
	m, err := MembershipFromEnv(zap.NewNop())
	if err != nil || m.Cell() != "pe-01" {
		t.Fatalf("configuracion valida: %v", err)
	}

	// Fuera de desarrollo o prueba, sin token interno no hay barrera: no arranca.
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "")
	t.Setenv("ENVIRONMENT", "production")
	if _, err := MembershipFromEnv(zap.NewNop()); err == nil {
		t.Fatal("arranco sin token interno en produccion")
	}
}

func valorRechazos(reason string) float64 {
	return valor(refusals.WithLabelValues(reason))
}
