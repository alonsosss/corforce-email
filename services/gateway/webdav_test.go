package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
)

func davTable(t *testing.T, upstream string) *routeTable {
	t.Helper()
	host, port := hostPort(t, upstream)
	t.Setenv("MAIL_DAV_HOST", host)
	t.Setenv("MAIL_DAV_HOST_PORT", port)
	tbl := &routeTable{
		Services:          map[string]serviceSpec{"mail-dav": {HostEnv: "MAIL_DAV_HOST", DefaultHost: "mail-dav", DefaultPort: "8058"}},
		SelfAuthenticated: []selfAuthSpec{{Prefix: "dav", Service: "mail-dav", Methods: []string{"PROPFIND", "REPORT", "MKCOL"}}},
		WellKnown:         []wellKnownSpec{{Path: "/.well-known/carddav", Prefix: "dav"}},
	}
	if err := tbl.validate(); err != nil {
		t.Fatal(err)
	}
	if err := tbl.loadUpstreams(); err != nil {
		t.Fatal(err)
	}
	return tbl
}

type davEscenario struct {
	gw       http.Handler
	received []*http.Request
	body     []string
	users    int
	web      int
}

// gateway monta lo que el gateway real monta para DAV: la guardia de metodos, la ruta de descubrimiento,
// el prefijo autenticado por el servicio, una ruta con JWT de ejemplo y la aplicacion como comodin.
func newDavEscenario(t *testing.T) *davEscenario {
	t.Helper()
	e := &davEscenario{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.received = append(e.received, r)
		e.body = append(e.body, string(b))
		w.WriteHeader(http.StatusMultiStatus)
	}))
	t.Cleanup(upstream.Close)
	tbl := davTable(t, upstream.URL)

	r := chi.NewRouter()
	r.Use(middleware.StripInternalHeaders)
	r.Use(webdavGuard(tbl))
	pass := func(next http.Handler) http.Handler { return next }
	mountWellKnown(r, tbl, pass)
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { e.web++ }))
	r.Route("/api/v1", func(r chi.Router) {
		mountSelfAuthenticated(r, tbl, pass, "token-interno", nil, nil)
		r.Route("/users", func(r chi.Router) {
			r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { e.users++ }))
		})
	})
	e.gw = r
	return e
}

func (e *davEscenario) do(method, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://mail.acme.test"+path, strings.NewReader("<propfind/>"))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.gw.ServeHTTP(rec, req)
	return rec
}

// Los metodos WebDAV declarados llegan a mail-dav sin JWT, con el token interno y con lo que el cliente
// envia (credenciales Basic, Depth, If-Match y cuerpo) intacto.
func TestElPrefijoDavRecibeSusMetodosSinJWT(t *testing.T) {
	e := newDavEscenario(t)
	for _, method := range []string{"PROPFIND", "REPORT", "MKCOL", "GET", "PUT", "DELETE", "OPTIONS"} {
		rec := e.do(method, "/api/v1/dav/addressbooks/ana@acme.test/contacts/", "Authorization", "Basic YW5hOnNlY3JldG8=", "Depth", "1", "If-Match", `"abc"`)
		if rec.Code != http.StatusMultiStatus {
			t.Fatalf("%s: %d", method, rec.Code)
		}
		got := e.received[len(e.received)-1]
		if got.Method != method || got.Header.Get("Authorization") != "Basic YW5hOnNlY3JldG8=" || got.Header.Get("Depth") != "1" ||
			got.Header.Get("If-Match") != `"abc"` || got.Header.Get("X-Gateway-Token") != "token-interno" {
			t.Fatalf("%s llego alterado: %v", method, got.Header)
		}
		if got.URL.Path != "/api/v1/dav/addressbooks/ana@acme.test/contacts/" {
			t.Fatalf("%s: la ruta cambio: %q", method, got.URL.Path)
		}
	}
	if e.body[0] != "<propfind/>" {
		t.Fatalf("cuerpo: %q", e.body[0])
	}
	rec := e.do("PROPFIND", "/api/v1/dav")
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("el prefijo sin barra final tambien llega: %d", rec.Code)
	}
}

// Ninguna cabecera interna que envie el cliente llega al servicio: el gateway pone las suyas.
func TestElClienteNoFijaCabecerasInternas(t *testing.T) {
	e := newDavEscenario(t)
	e.do("PROPFIND", "/api/v1/dav/", "X-User-ID", "u", "X-Tenant-ID", "t", "X-Gateway-Token", "falso", "X-Internal-Token", "falso")
	got := e.received[0].Header
	if got.Get("X-User-ID") != "" || got.Get("X-Tenant-ID") != "" || got.Get("X-Internal-Token") != "" || got.Get("X-Gateway-Token") != "token-interno" {
		t.Fatalf("cabeceras internas: %v", got)
	}
}

// Los metodos de extension no pasan a ninguna otra parte: ni a rutas con JWT, cuyo RBAC los tomaria por
// lecturas, ni a la aplicacion, ni a metodos que el prefijo no declara.
func TestLosMetodosWebDAVSoloPasanDondeSeDeclaran(t *testing.T) {
	e := newDavEscenario(t)
	for _, tc := range []struct{ method, path string }{
		{"PROPFIND", "/api/v1/users/"},
		{"MKCOL", "/api/v1/users/x"},
		{"MOVE", "/api/v1/users/x"},
		{"REPORT", "/"},
		{"PROPFIND", "/index.html"},
		{"PROPPATCH", "/api/v1/dav/addressbooks/ana@acme.test/contacts/"},
		{"COPY", "/api/v1/dav/addressbooks/ana@acme.test/contacts/a.vcf"},
		{"MOVE", "/api/v1/dav/x"},
		{"LOCK", "/api/v1/dav/x"},
		{"PROPFIND", "/api/v1/davx/"},
		{"PROPFIND", "/api/v1/"},
		{"PROPFIND", "/api/v1/dav%2F..%2Fusers/"},
		{"PROPFIND", "/api/v1/users/../dav/"},
		{"PROPFIND", "/.well-known/otro"},
	} {
		rec := e.do(tc.method, tc.path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", tc.method, tc.path, rec.Code)
		}
	}
	if e.users != 0 || e.web != 0 || len(e.received) != 0 {
		t.Fatalf("algo llego a un destino: users=%d web=%d dav=%d", e.users, e.web, len(e.received))
	}
	// Los metodos de siempre siguen entrando por sus rutas.
	if e.do("GET", "/api/v1/users/x"); e.users != 1 {
		t.Fatal("GET a una ruta con JWT")
	}
	if e.do("GET", "/index.html"); e.web != 1 {
		t.Fatal("GET a la aplicacion")
	}
}

// /.well-known/carddav redirige al prefijo con cualquier metodo, sin llegar a ningun servicio.
func TestElDescubrimientoRedirigeAlPrefijo(t *testing.T) {
	e := newDavEscenario(t)
	for _, method := range []string{"GET", "HEAD", "PROPFIND", "OPTIONS", "POST"} {
		rec := e.do(method, "/.well-known/carddav")
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/api/v1/dav/" {
			t.Errorf("%s: %d %q", method, rec.Code, rec.Header().Get("Location"))
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: la redireccion no debe quedarse en cache: %q", method, rec.Header().Get("Cache-Control"))
		}
	}
	if len(e.received) != 0 || e.web != 0 {
		t.Fatal("el descubrimiento lo resuelve el gateway")
	}
	if rec := e.do("GET", "/.well-known/carddav/x"); rec.Code == http.StatusMovedPermanently {
		t.Fatal("solo la ruta exacta redirige")
	}
}

func TestValidacionDeDescubrimientoYMetodos(t *testing.T) {
	base := func() *routeTable {
		return &routeTable{
			Services:          map[string]serviceSpec{"mail-dav": {HostEnv: "MAIL_DAV_HOST", DefaultHost: "mail-dav", DefaultPort: "8058"}},
			SelfAuthenticated: []selfAuthSpec{{Prefix: "dav", Service: "mail-dav", Methods: []string{"PROPFIND"}}},
			WellKnown:         []wellKnownSpec{{Path: "/.well-known/carddav", Prefix: "dav"}},
		}
	}
	if err := base().validate(); err != nil {
		t.Fatalf("la tabla buena: %v", err)
	}
	for name, mutate := range map[string]func(*routeTable){
		"metodo desconocido":       func(t *routeTable) { t.SelfAuthenticated[0].Methods = []string{"PROPFIND", "TRACE"} },
		"metodo habitual":          func(t *routeTable) { t.SelfAuthenticated[0].Methods = []string{"GET"} },
		"metodo en minusculas":     func(t *routeTable) { t.SelfAuthenticated[0].Methods = []string{"propfind"} },
		"ruta fuera de well-known": func(t *routeTable) { t.WellKnown[0].Path = "/carddav" },
		"ruta con mayusculas":      func(t *routeTable) { t.WellKnown[0].Path = "/.well-known/CardDAV" },
		"ruta con subruta":         func(t *routeTable) { t.WellKnown[0].Path = "/.well-known/carddav/x" },
		"ruta con punto punto":     func(t *routeTable) { t.WellKnown[0].Path = "/.well-known/../x" },
		"ruta repetida": func(t *routeTable) {
			t.WellKnown = append(t.WellKnown, wellKnownSpec{Path: "/.well-known/carddav", Prefix: "dav"})
		},
		"prefijo desconocido": func(t *routeTable) { t.WellKnown[0].Prefix = "nadie" },
		"prefijo con JWT": func(t *routeTable) {
			t.Routes = []routeSpec{{Prefix: "users", Service: "mail-dav", Module: "identity"}}
			t.WellKnown[0].Prefix = "users"
		},
	} {
		tbl := base()
		mutate(tbl)
		if err := tbl.validate(); err == nil {
			t.Errorf("%s: debia rechazarse", name)
		}
	}
}

// La tabla real declara DAV como el ADR 0004: prefijo autenticado por el servicio, sin modulo, con
// solo los metodos que mail-dav implementa, y el descubrimiento de CardDAV.
func TestLaTablaRealDeclaraCardDAV(t *testing.T) {
	tbl, err := decodeRouteTable(defaultRoutes)
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.validate(); err != nil {
		t.Fatal(err)
	}
	var dav *selfAuthSpec
	for i := range tbl.SelfAuthenticated {
		if tbl.SelfAuthenticated[i].Prefix == "dav" {
			dav = &tbl.SelfAuthenticated[i]
		}
	}
	if dav == nil || dav.Service != "mail-dav" || strings.Join(dav.Methods, ",") != "PROPFIND,REPORT,MKCOL" || len(dav.StrictLimit) != 0 {
		t.Fatalf("prefijo dav: %+v", dav)
	}
	if tbl.moduleIndex()["dav"] != "" {
		t.Fatal("dav no se gatea por modulo: lo autentica mail-dav contra mail-auth")
	}
	if len(tbl.WellKnown) != 1 || tbl.WellKnown[0] != (wellKnownSpec{Path: "/.well-known/carddav", Prefix: "dav"}) {
		t.Fatalf("well_known: %+v", tbl.WellKnown)
	}
	if s := tbl.Services["mail-dav"]; s.HostEnv != "MAIL_DAV_HOST" || s.DefaultHost != "mail-dav" || s.DefaultPort != "8058" || s.CellHostsEnv != "" {
		t.Fatalf("servicio mail-dav: %+v", s)
	}
}
