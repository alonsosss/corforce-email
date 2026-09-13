package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/pkg/tenantcell/tenantcelltest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Cabeceras de la prueba que hacen de token verificado: el gateway real saca usuario y roles del
// JWT, y borra las cabeceras internas del cliente.
const (
	cabeceraUsuario = "X-Prueba-User"
	cabeceraRoles   = "X-Prueba-Roles"
)

// publicadorDePrueba recoge lo que el rastro publica en audit.api.write.
type publicadorDePrueba struct{ eventos chan events.Event }

func (p *publicadorDePrueba) Publish(string, events.Event) error { return nil }

func (p *publicadorDePrueba) PublishPersistent(subject string, evt events.Event) error {
	if subject == "audit.api.write" {
		p.eventos <- evt
	}
	return nil
}

func (p *publicadorDePrueba) siguiente(t *testing.T, caso string) events.Event {
	t.Helper()
	select {
	case evt := <-p.eventos:
		return evt
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: el rastro no publico nada", caso)
		return events.Event{}
	}
}

func (p *publicadorDePrueba) ninguno(t *testing.T, caso string) {
	t.Helper()
	select {
	case evt := <-p.eventos:
		t.Fatalf("%s: el rastro publico %+v", caso, evt.Data)
	case <-time.After(150 * time.Millisecond):
	}
}

// instanciaOperador anota lo que recibe una instancia: la empresa, la celda destino que le pone
// el gateway y la cabecera del cliente, que nunca debe llegar.
func instanciaOperador(t *testing.T, g *grabador, nombre string) (host, port string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.add(nombre + " " + r.Method + " " + r.URL.Path + " tenant=" + r.Header.Get("X-Tenant-ID") +
			" operador=" + r.Header.Get(middleware.HeaderOperatorCell) + " destino=" + r.Header.Get(targetCellHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)
	return hostPort(t, srv.URL)
}

// newTargetGateway monta las rutas con sesion como main, en su orden (rastro, celda destino,
// enrutado), con mail-directory y mail-security en pe-01 (destino base) y pe-02. base vacia es
// un despliegue de una celda.
func newTargetGateway(t *testing.T, g *grabador, orgURL, base string) (http.Handler, *publicadorDePrueba) {
	t.Helper()
	t.Setenv("GATEWAY_ROUTES_FILE", "")
	t.Setenv(baseCellEnv, base)
	host, port := hostPort(t, orgURL)
	t.Setenv("ORGANIZATION_HOST", host)
	t.Setenv("ORGANIZATION_HOST_PORT", port)
	host, port = instanciaOperador(t, g, "templates")
	t.Setenv("TEMPLATES_HOST", host)
	t.Setenv("TEMPLATES_HOST_PORT", port)
	for _, s := range []struct{ name, env string }{{"mail-directory", "MAIL_DIRECTORY"}, {"mail-security", "MAIL_SECURITY"}} {
		host, port = instanciaOperador(t, g, s.name+" pe-01")
		t.Setenv(s.env+"_HOST", host)
		t.Setenv(s.env+"_HOST_PORT", port)
		cells := ""
		if base != "" {
			host, port = instanciaOperador(t, g, s.name+" pe-02")
			cells = "pe-02=" + net.JoinHostPort(host, port)
		}
		t.Setenv(s.env+"_CELL_HOSTS", cells)
	}
	tbl, err := loadRouteTable()
	if err != nil {
		t.Fatal(err)
	}
	var cells *tenantcell.Resolver
	if tbl.baseCell != "" {
		cells = tenantcell.NewResolver(tbl.serviceURL(cellDirectoryService), "token-interno", zap.NewNop())
	}
	pub := &publicadorDePrueba{eventos: make(chan events.Event, 32)}
	trail := &auditTrail{bus: pub, modules: tbl.moduleIndex(), logger: zap.NewNop(),
		exfilReads: map[string]*readWindow{}, exfilMax: 1000, exfilWindow: time.Minute}
	targets := newTargetCellGate(tbl, zap.NewNop())
	handlers := sessionHandlers(tbl, "token-interno", cells, zap.NewNop())

	r := chi.NewRouter()
	r.Use(middleware.StripInternalHeaders)
	r.Use(captureTargetCell)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := middleware.WithIdentity(r.Context(), r.Header.Get(cabeceraUsuario), r.Header.Get(cabeceraDePrueba))
				ctx = context.WithValue(ctx, middleware.CtxRoles, strings.Split(r.Header.Get(cabeceraRoles), ","))
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})
		r.Use(trail.middleware)
		r.Use(targets.middleware)
		for _, rt := range tbl.Routes {
			proxy := handlers[rt.Service]
			r.Route("/"+rt.Prefix, func(r chi.Router) { r.Handle("/*", proxy) })
		}
	})
	return r, pub
}

type quien struct{ tenant, user, roles string }

// pedirConDestino hace la peticion como quien, con una cabecera X-Target-Cell por cada valor.
func pedirConDestino(h http.Handler, q quien, method, path string, targets ...string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	req.Header.Set(cabeceraDePrueba, q.tenant)
	req.Header.Set(cabeceraUsuario, q.user)
	req.Header.Set(cabeceraRoles, q.roles)
	for _, t := range targets {
		req.Header.Add(targetCellHeader, t)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func varyIncluyeDestino(rec *httptest.ResponseRecorder) bool {
	for _, v := range rec.Header().Values("Vary") {
		if strings.EqualFold(v, targetCellHeader) {
			return true
		}
	}
	return false
}

type escenarioDestino struct {
	gw         http.Handler
	pub        *publicadorDePrueba
	g          *grabador
	org        *tenantcelltest.Organization
	operador   quien
	empresaPe2 quien
}

func nuevoEscenarioDestino(t *testing.T, base string) *escenarioDestino {
	t.Helper()
	plataforma, t02, t03 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	org, orgURL := tenantcelltest.New(t, "token-interno", map[string]string{plataforma: "pe-01", t02: "pe-02", t03: "pe-03"})
	g := &grabador{}
	gw, pub := newTargetGateway(t, g, orgURL, base)
	return &escenarioDestino{
		gw: gw, pub: pub, g: g, org: org,
		operador:   quien{tenant: plataforma, user: uuid.NewString(), roles: middleware.RoleSuperadmin},
		empresaPe2: quien{tenant: t02, user: uuid.NewString(), roles: middleware.RoleTenantAdmin},
	}
}

// El superadmin llega a la instancia de la celda que nombra, con la celda en X-Operator-Cell y
// sin la cabecera del cliente; cada peticion queda en el rastro, lecturas incluidas. Sin
// cabecera sigue yendo a la celda de su empresa.
func TestElSuperadminEligeLaCeldaDestino(t *testing.T) {
	e := nuevoEscenarioDestino(t, "pe-01")
	op := e.operador

	for _, c := range []struct{ method, path, cell, instance string }{
		{http.MethodGet, "/api/v1/mail-security/firewall/networks", "pe-02", "mail-security pe-02"},
		{http.MethodGet, "/api/v1/mail-security/firewall/networks", "pe-01", "mail-security pe-01"},
		{http.MethodPost, "/api/v1/mail-security/firewall/networks", "pe-02", "mail-security pe-02"},
		{http.MethodGet, "/api/v1/mailboxes", "pe-02", "mail-directory pe-02"},
	} {
		caso := c.method + " " + c.path + " en " + c.cell
		rec := pedirConDestino(e.gw, op, c.method, c.path, c.cell)
		hits := e.g.take()
		want := c.instance + " " + c.method + " " + c.path + " tenant=" + op.tenant + " operador=" + c.cell + " destino="
		if rec.Code != http.StatusOK || len(hits) != 1 || hits[0] != want {
			t.Fatalf("%s: %d %s %v, se esperaba %q", caso, rec.Code, rec.Body, hits, want)
		}
		if !varyIncluyeDestino(rec) {
			t.Fatalf("%s: la respuesta no varia por %s: %v", caso, targetCellHeader, rec.Header().Values("Vary"))
		}
		evt := e.pub.siguiente(t, caso)
		d, _ := evt.Data.(map[string]interface{})
		if evt.Type != "audit.api.write" || evt.TenantID != op.tenant || evt.UserID != op.user || d["target_cell"] != c.cell ||
			d["method"] != c.method || d["path"] != c.path || d["status"] != http.StatusOK || d["roles"] != middleware.RoleSuperadmin {
			t.Fatalf("%s: apunte de auditoria %+v %+v", caso, evt, d)
		}
	}

	// Sin cabecera: la celda de su empresa (plataforma, pe-01), sin celda destino, y una lectura
	// no se audita.
	rec := pedirConDestino(e.gw, op, http.MethodGet, "/api/v1/mail-security/firewall/networks")
	if hits := e.g.take(); rec.Code != http.StatusOK || len(hits) != 1 ||
		hits[0] != "mail-security pe-01 GET /api/v1/mail-security/firewall/networks tenant="+op.tenant+" operador= destino=" {
		t.Fatalf("superadmin sin celda destino: %d %v", rec.Code, hits)
	}
	e.pub.ninguno(t, "lectura sin celda destino")
}

// Celda destino mal formada, sin instancia o en una ruta que no es de un servicio de celda: no sale
// hacia ninguna instancia, nunca cae en la celda de la empresa y queda en el rastro con el
// resultado. organization no se consulta: la celda destino no depende de la empresa.
func TestLaCeldaDestinoInvalidaNoSaleANingunaCelda(t *testing.T) {
	e := nuevoEscenarioDestino(t, "pe-01")
	op := e.operador
	firewall := "/api/v1/mail-security/firewall/networks"
	consultas := e.org.Calls()

	for _, c := range []struct {
		caso, path string
		targets    []string
		status     int
		code       string
		reason     string
		audit      string
	}{
		{"celda sin instancia de ningun servicio", firewall, []string{"pe-09"}, http.StatusServiceUnavailable, tenantcell.CodeCellUnavailable, targetNotServed, "pe-09"},
		{"celda registrada sin instancia", firewall, []string{"pe-03"}, http.StatusServiceUnavailable, tenantcell.CodeCellUnavailable, targetNotServed, "pe-03"},
		{"mayusculas", firewall, []string{"PE-02"}, http.StatusBadRequest, codeInvalidTargetCell, targetMalformed, invalidTargetAudit},
		{"con ruta", firewall, []string{"../pe-02"}, http.StatusBadRequest, codeInvalidTargetCell, targetMalformed, invalidTargetAudit},
		{"vacia", firewall, []string{""}, http.StatusBadRequest, codeInvalidTargetCell, targetMalformed, invalidTargetAudit},
		{"lista", firewall, []string{"pe-02,pe-01"}, http.StatusBadRequest, codeInvalidTargetCell, targetMalformed, invalidTargetAudit},
		{"dos cabeceras", firewall, []string{"pe-02", "pe-02"}, http.StatusBadRequest, codeInvalidTargetCell, targetMalformed, invalidTargetAudit},
		{"demasiado larga", firewall, []string{strings.Repeat("a", tenantcell.MaxCodeLen+1)}, http.StatusBadRequest, codeInvalidTargetCell, targetMalformed, invalidTargetAudit},
		{"servicio que no es de celda", "/api/v1/templates/x", []string{"pe-02"}, http.StatusBadRequest, codeTargetCellNotApplicable, targetNotCellService, "pe-02"},
		{"plano de control", "/api/v1/organizations", []string{"pe-02"}, http.StatusBadRequest, codeTargetCellNotApplicable, targetNotCellService, "pe-02"},
	} {
		antes := counterValue("cell_target_refusals_total", map[string]string{"reason": c.reason})
		rec := pedirConDestino(e.gw, op, http.MethodGet, c.path, c.targets...)
		if rec.Code != c.status || codigoDeError(rec) != c.code || len(e.g.take()) != 0 {
			t.Fatalf("%s: %d %s, se esperaba %d %s sin llegar a ninguna instancia", c.caso, rec.Code, rec.Body, c.status, c.code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: el rechazo no lleva Cache-Control: no-store", c.caso)
		}
		if got := counterValue("cell_target_refusals_total", map[string]string{"reason": c.reason}); got != antes+1 {
			t.Fatalf("%s: metrica %s %v, se esperaba %v", c.caso, c.reason, got, antes+1)
		}
		d, _ := e.pub.siguiente(t, c.caso).Data.(map[string]interface{})
		if d["target_cell"] != c.audit || d["status"] != c.status || d["user_id"] != op.user {
			t.Fatalf("%s: apunte de auditoria %+v", c.caso, d)
		}
	}
	if e.org.Calls() != consultas {
		t.Fatalf("una celda destino rechazada pregunto a organization")
	}
}

// Una sesion que no es de superadmin no elige celda: 403 aunque la celda exista y sea la suya,
// sin salir hacia ninguna instancia y con el intento en el rastro de su empresa. La celda
// destino interna que mande el cliente se borra y la empresa va a su celda.
func TestUnaEmpresaNoEligeCeldaDestino(t *testing.T) {
	e := nuevoEscenarioDestino(t, "pe-01")
	emp := e.empresaPe2
	firewall := "/api/v1/mail-security/firewall/networks"

	for _, cell := range []string{"pe-01", "pe-02"} {
		antes := counterValue("cell_target_refusals_total", map[string]string{"reason": targetNotOperator})
		rec := pedirConDestino(e.gw, emp, http.MethodGet, firewall, cell)
		if rec.Code != http.StatusForbidden || codigoDeError(rec) != codeTargetCellForbidden || len(e.g.take()) != 0 {
			t.Fatalf("empresa con celda destino %s: %d %s", cell, rec.Code, rec.Body)
		}
		if got := counterValue("cell_target_refusals_total", map[string]string{"reason": targetNotOperator}); got != antes+1 {
			t.Fatalf("metrica not_operator: %v", got)
		}
		evt := e.pub.siguiente(t, "intento de una empresa")
		d, _ := evt.Data.(map[string]interface{})
		if evt.TenantID != emp.tenant || d["target_cell"] != cell || d["status"] != http.StatusForbidden {
			t.Fatalf("apunte del intento: %+v %+v", evt, d)
		}
	}
	// Tampoco un rol que solo se parece al de plataforma.
	parecido := quien{tenant: emp.tenant, user: emp.user, roles: "Superadmin,tenant_admin"}
	if rec := pedirConDestino(e.gw, parecido, http.MethodGet, firewall, "pe-01"); rec.Code != http.StatusForbidden || len(e.g.take()) != 0 {
		t.Fatalf("rol parecido: %d", rec.Code)
	}
	e.pub.siguiente(t, "rol parecido")

	// La cabecera interna del cliente no llega: la empresa va a su celda como siempre.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, firewall, nil)
	req.Header.Set(cabeceraDePrueba, emp.tenant)
	req.Header.Set(cabeceraUsuario, emp.user)
	req.Header.Set(cabeceraRoles, emp.roles)
	req.Header.Set(middleware.HeaderOperatorCell, "pe-01")
	e.gw.ServeHTTP(rec, req)
	if hits := e.g.take(); rec.Code != http.StatusOK || len(hits) != 1 ||
		hits[0] != "mail-security pe-02 GET "+firewall+" tenant="+emp.tenant+" operador= destino=" {
		t.Fatalf("celda destino interna del cliente: %d %v", rec.Code, hits)
	}
	e.pub.ninguno(t, "lectura sin celda destino")
}

// En un despliegue de una celda el gateway no sabe que celda sirve el destino base: no admite
// celda destino y no pregunta a organization.
func TestUnaSolaCeldaNoAdmiteCeldaDestino(t *testing.T) {
	e := nuevoEscenarioDestino(t, "")
	rec := pedirConDestino(e.gw, e.operador, http.MethodGet, "/api/v1/mail-security/firewall/networks", "pe-01")
	if rec.Code != http.StatusServiceUnavailable || codigoDeError(rec) != tenantcell.CodeCellUnavailable || len(e.g.take()) != 0 {
		t.Fatalf("una celda con celda destino: %d %s", rec.Code, rec.Body)
	}
	if e.org.Calls() != 0 {
		t.Fatalf("una celda pregunto a organization")
	}
	e.pub.siguiente(t, "una celda")
	if rec := pedirConDestino(e.gw, e.operador, http.MethodGet, "/api/v1/mail-security/firewall/networks"); rec.Code != http.StatusOK ||
		len(e.g.take()) != 1 || !varyIncluyeDestino(rec) {
		t.Fatalf("una celda sin celda destino: %d", rec.Code)
	}
}

// La cabecera del cliente no sigue hacia ningun servicio, tampoco por rutas sin sesion.
func TestLaCeldaDestinoDelClienteNoSigue(t *testing.T) {
	var got []string
	var pedida requestedTarget
	h := captureTargetCell(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values(targetCellHeader)
		pedida, _ = requestedTargetFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/contacts/confirm", nil)
	req.Header.Add(targetCellHeader, "pe-02")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(got) != 0 || len(pedida.values) != 1 || pedida.auditValue() != "pe-02" {
		t.Fatalf("cabecera %v, pedida %+v", got, pedida)
	}
}
