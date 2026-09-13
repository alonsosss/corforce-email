package tenantcell

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// rutasDeCelda son las de un servicio de celda: dos de plataforma (el cortafuegos) y una de datos
// de empresa. Cuentan lo que atienden.
func rutasDeCelda(atendido *[]string) chi.Router {
	r := chi.NewRouter()
	anota := func(w http.ResponseWriter, r *http.Request) {
		*atendido = append(*atendido, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}
	r.Route("/api/v1/svc", func(r chi.Router) {
		r.Get("/firewall", anota)
		r.Delete("/firewall/{id}", anota)
		r.Post("/firewall", anota)
		r.Get("/datos", anota)
		r.Get("/datos/{id}", anota)
	})
	return r
}

var rutasDePlataforma = []Route{
	{Method: http.MethodGet, Pattern: "/api/v1/svc/firewall"},
	{Method: http.MethodDelete, Pattern: "/api/v1/svc/firewall/{id}"},
}

type instanciaConOperadores struct {
	org      *tenantcelltest.Organization
	handler  http.Handler
	atendido []string
}

// nuevaInstancia es la instancia de pe-01 con la barrera como en main; con plataforma, abre esas
// rutas a los operadores.
func nuevaInstancia(t *testing.T, cells map[string]string, plataforma []Route) *instanciaConOperadores {
	t.Helper()
	org, url := tenantcelltest.New(t, tokenDePrueba, cells)
	resolver, _ := nuevoResolver(url)
	m, err := NewMembership("pe-01", resolver, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	s := &instanciaConOperadores{org: org}
	routes := rutasDeCelda(&s.atendido)
	if plataforma != nil {
		if err := m.AcceptOperators(routes, plataforma); err != nil {
			t.Fatal(err)
		}
	}
	s.handler = middleware.InjectFromGateway(m.Require(routes))
	return s
}

type operador struct {
	tenant, user, roles string
	cells               []string
}

func (s *instanciaConOperadores) pedir(o operador, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if o.tenant != "" {
		req.Header.Set("X-Tenant-ID", o.tenant)
	}
	if o.user != "" {
		req.Header.Set("X-User-ID", o.user)
	}
	if o.roles != "" {
		req.Header.Set("X-User-Roles", o.roles)
	}
	for _, c := range o.cells {
		req.Header.Add(middleware.HeaderOperatorCell, c)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func (s *instanciaConOperadores) rechazada(t *testing.T, caso string, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	var sv servicioDeCelda
	sv.rechazada(t, caso, rec, http.StatusForbidden, code)
	if len(s.atendido) != 0 {
		t.Fatalf("%s: se atendio %v", caso, s.atendido)
	}
}

// Con celda destino la instancia atiende al superadmin solo en sus rutas de plataforma, aunque su
// empresa (la de plataforma) sea de otra celda, y sin preguntar a organization. Una ruta de datos
// de empresa, otro metodo sobre la misma ruta o una ruta retorcida no se atienden.
func TestElOperadorConCeldaDestinoSoloLlegaALasRutasDePlataforma(t *testing.T) {
	plataforma := uuid.NewString()
	s := nuevaInstancia(t, map[string]string{plataforma: "pe-02"}, rutasDePlataforma)
	op := operador{tenant: plataforma, user: uuid.NewString(), roles: "superadmin", cells: []string{"pe-01"}}

	for _, path := range []string{"/api/v1/svc/firewall"} {
		if rec := s.pedir(op, http.MethodGet, path); rec.Code != http.StatusOK || len(s.atendido) != 1 {
			t.Fatalf("GET %s: %d %v", path, rec.Code, s.atendido)
		}
		s.atendido = nil
	}
	if rec := s.pedir(op, http.MethodDelete, "/api/v1/svc/firewall/abc"); rec.Code != http.StatusOK || len(s.atendido) != 1 {
		t.Fatalf("DELETE con parametro: %d %v", rec.Code, s.atendido)
	}
	s.atendido = nil

	antes := valorRechazos(reasonOperatorTenantRoute)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/svc/datos"},
		{http.MethodGet, "/api/v1/svc/datos/abc"},
		{http.MethodPost, "/api/v1/svc/firewall"},
		{http.MethodGet, "/api/v1/svc/firewall/abc"},
		{http.MethodGet, "/api/v1/svc/firewall/../datos"},
		{http.MethodGet, "/api/v1/svc/firewall%2F..%2Fdatos"},
		{http.MethodGet, "/api/v1/svc//firewall"},
		{http.MethodGet, "/api/v1/svc/firewall/"},
		{http.MethodGet, "/api/v1/otra"},
	} {
		s.rechazada(t, c.method+" "+c.path, s.pedir(op, c.method, c.path), CodePlatformScopeOnly)
	}
	if got := valorRechazos(reasonOperatorTenantRoute); got != antes+9 {
		t.Fatalf("operator_tenant_route: %v, se esperaba %v", got, antes+9)
	}
	if s.org.Calls() != 0 {
		t.Fatalf("la celda destino pregunto %d veces a organization", s.org.Calls())
	}
}

// La celda destino tiene que ser esta, una sola vez, y quien llama una persona con el rol
// superadmin: un administrador de empresa, un servicio sin usuario o un rol parecido no pasan.
func TestLaCeldaDestinoExigeEstaCeldaYAlSuperadmin(t *testing.T) {
	plataforma, socia := uuid.NewString(), uuid.NewString()
	s := nuevaInstancia(t, map[string]string{plataforma: "pe-01", socia: "pe-01"}, rutasDePlataforma)
	usuario := uuid.NewString()
	firewall := "/api/v1/svc/firewall"

	wrong := valorRechazos(reasonOperatorWrongCell)
	for _, cells := range [][]string{{"pe-02"}, {""}, {"PE-01"}, {"pe-01", "pe-01"}, {"pe-01,pe-02"}} {
		op := operador{tenant: plataforma, user: usuario, roles: "superadmin", cells: cells}
		s.rechazada(t, "celda destino "+strings.Join(cells, "|"), s.pedir(op, http.MethodGet, firewall), CodeTargetCellMismatch)
	}
	if got := valorRechazos(reasonOperatorWrongCell); got != wrong+5 {
		t.Fatalf("operator_wrong_cell: %v", got)
	}

	notPlatform := valorRechazos(reasonOperatorNotPlatform)
	for caso, op := range map[string]operador{
		"administrador de una empresa de la celda": {tenant: socia, user: usuario, roles: "tenant_admin", cells: []string{"pe-01"}},
		"rol parecido":           {tenant: plataforma, user: usuario, roles: "Superadmin,superadmin2", cells: []string{"pe-01"}},
		"servicio sin usuario":   {tenant: plataforma, roles: "superadmin", cells: []string{"pe-01"}},
		"sin empresa ni usuario": {cells: []string{"pe-01"}},
	} {
		s.rechazada(t, caso, s.pedir(op, http.MethodGet, firewall), CodePlatformScopeOnly)
	}
	if got := valorRechazos(reasonOperatorNotPlatform); got != notPlatform+4 {
		t.Fatalf("operator_not_platform: %v", got)
	}

	// Sin celda destino todo sigue como antes: la empresa de la celda llega a todas sus rutas.
	if rec := s.pedir(operador{tenant: socia, user: usuario, roles: "tenant_admin"}, http.MethodGet, "/api/v1/svc/datos"); rec.Code != http.StatusOK {
		t.Fatalf("empresa de la celda sin celda destino: %d", rec.Code)
	}
}

// Sin rutas de plataforma declaradas ninguna peticion con celda destino pasa, y sin celda
// destino el superadmin sigue sujeto a la celda de su empresa.
func TestSinRutasDePlataformaNoPasaNingunOperador(t *testing.T) {
	plataforma := uuid.NewString()
	s := nuevaInstancia(t, map[string]string{plataforma: "pe-02"}, nil)
	op := operador{tenant: plataforma, user: uuid.NewString(), roles: "superadmin", cells: []string{"pe-01"}}
	s.rechazada(t, "sin rutas de plataforma", s.pedir(op, http.MethodGet, "/api/v1/svc/firewall"), CodePlatformScopeOnly)

	op.cells = nil
	var sv servicioDeCelda
	sv.rechazada(t, "superadmin de otra celda sin celda destino", s.pedir(op, http.MethodGet, "/api/v1/svc/firewall"), http.StatusForbidden, CodeNotInCell)
}

// Una ruta declarada que no esta montada impide arrancar: la declaracion diria una cosa y el
// router haria otra.
func TestLasRutasDePlataformaDebenExistir(t *testing.T) {
	m, err := NewMembership("pe-01", NewResolver("http://organization:8003", tokenDePrueba, zap.NewNop()), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	var atendido []string
	routes := rutasDeCelda(&atendido)
	for caso, c := range map[string]struct {
		routes chi.Routes
		rutas  []Route
	}{
		"sin router":            {nil, rutasDePlataforma},
		"sin rutas":             {routes, nil},
		"ruta que no existe":    {routes, []Route{{Method: http.MethodGet, Pattern: "/api/v1/svc/cortafuegos"}}},
		"metodo que no existe":  {routes, []Route{{Method: http.MethodPut, Pattern: "/api/v1/svc/firewall"}}},
		"patron con otro param": {routes, []Route{{Method: http.MethodDelete, Pattern: "/api/v1/svc/firewall/{x}"}}},
		"sin prefijo":           {routes, []Route{{Method: http.MethodGet, Pattern: "/firewall"}}},
	} {
		if err := m.AcceptOperators(c.routes, c.rutas); err == nil {
			t.Errorf("%s: aceptado", caso)
		}
	}
	if err := m.AcceptOperators(routes, rutasDePlataforma); err != nil {
		t.Fatal(err)
	}
}
