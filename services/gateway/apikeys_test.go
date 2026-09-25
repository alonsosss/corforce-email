package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/apikey"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const testKey = "cfm_abcdefgh2345_secreto"

type stubKeys struct {
	p     *apikey.Principal
	err   error
	calls int
	ip    string
}

func (s *stubKeys) Resolve(_ context.Context, token, ip string) (*apikey.Principal, error) {
	s.calls++
	s.ip = ip
	if s.err != nil {
		return nil, s.err
	}
	if token != testKey {
		return nil, apikey.ErrInvalid
	}
	return s.p, nil
}

func sendPrincipal(actions ...string) *apikey.Principal {
	p := &apikey.Principal{ID: "5d8c7a1e-0b7e-4a52-9c3e-2f7b1c9d0e11", TenantID: "7f0b1e2c-0000-4000-8000-0000000000aa", Prefix: "abcdefgh2345"}
	for _, a := range actions {
		p.Scopes = append(p.Scopes, middleware.APIKeyScope{Module: "transactional", Resource: "messages", Action: a})
	}
	return p
}

type keyHarness struct {
	h        http.Handler
	keys     *stubKeys
	upstream *http.Request
	jwtCalls int
}

// newKeyHarness monta la cadena del gateway para las rutas con sesion: autenticacion (clave o JWT),
// sesion y RBAC, y un proxy real hacia un servicio de prueba que anota lo que recibe.
func newKeyHarness(t *testing.T, keys *stubKeys, ratePerMin int) *keyHarness {
	t.Helper()
	k := &keyHarness{keys: keys}
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.upstream = r.Clone(context.Background())
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(service.Close)

	table, err := decodeRouteTable(defaultRoutes)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.validate(); err != nil {
		t.Fatal(err)
	}
	gate, err := newAPIKeyGate(table, keys, middleware.NewRateLimiter(ratePerMin, time.Minute), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	// access-control no responde: una peticion con JWT fallaria cerrada, una con clave no le pregunta.
	enforcer := newRBACEnforcer("http://127.0.0.1:1", "tok", "", "", "", table.moduleIndex(), table.readPostIndex(), zap.NewNop())
	jwt := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k.jwtCalls++
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
		})
	}
	r := chi.NewRouter()
	r.Use(middleware.StripInternalHeaders)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(gate.authenticate(jwt))
		r.Use(enforcer.sessionCheck)
		r.Use(enforcer.middleware)
		proxy := reverseProxy(service.URL, "tok")
		for _, rt := range table.Routes {
			r.Handle("/"+rt.Prefix+"/*", proxy)
		}
	})
	k.h = r
	return k
}

func (k *keyHarness) do(method, path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set(middleware.HeaderAPIKeyScopes, "access:roles:create")
	req.Header.Set("X-User-ID", "forjado")
	rec := httptest.NewRecorder()
	k.h.ServeHTTP(rec, req)
	return rec
}

func TestClaveEnRutaPermitida(t *testing.T) {
	k := newKeyHarness(t, &stubKeys{p: sendPrincipal("create", "read")}, 100)
	rec := k.do(http.MethodPost, "/api/v1/transactional/messages", testKey)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("envio con clave: %d %s", rec.Code, rec.Body.String())
	}
	up := k.upstream
	if up.Header.Get(middleware.HeaderAPIKeyID) != sendPrincipal().ID || up.Header.Get("X-Tenant-ID") != sendPrincipal().TenantID {
		t.Fatalf("el servicio recibe la clave y la empresa: %v", up.Header)
	}
	if up.Header.Get(middleware.HeaderAPIKeyScopes) != "transactional:messages:create,transactional:messages:read" {
		t.Fatalf("el alcance lo escribe el gateway, no el cliente: %q", up.Header.Get(middleware.HeaderAPIKeyScopes))
	}
	if up.Header.Get("Authorization") != "" || up.Header.Get("X-User-ID") != "" || up.Header.Get("X-User-Roles") != "" {
		t.Fatalf("sin clave, usuario ni roles hacia el servicio: %v", up.Header)
	}
	if rec := k.do(http.MethodGet, "/api/v1/transactional/messages/"+sendPrincipal().ID, testKey); rec.Code != http.StatusAccepted {
		t.Fatalf("lectura del estado: %d", rec.Code)
	}
	if rec := k.do(http.MethodGet, "/api/v1/transactional/messages/"+sendPrincipal().ID+"/events", testKey); rec.Code != http.StatusAccepted {
		t.Fatalf("eventos del mensaje: %d", rec.Code)
	}
}

// Fuera de la lista cerrada una clave no se acepta ni se prueba: ni otro modulo, ni otra ruta del
// mismo modulo, ni otro metodo.
func TestClaveEnRutaNoPermitida(t *testing.T) {
	keys := &stubKeys{p: sendPrincipal("create", "read")}
	k := newKeyHarness(t, keys, 100)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/transactional/messages"},
		{http.MethodGet, "/api/v1/transactional/stats"},
		{http.MethodDelete, "/api/v1/transactional/messages/x"},
		{http.MethodPost, "/api/v1/campaigns"},
		{http.MethodGet, "/api/v1/access/api-keys"},
		{http.MethodPost, "/api/v1/roles"},
	} {
		rec := k.do(tc.method, tc.path, testKey)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "API_KEY_ROUTE_NOT_ALLOWED") {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	if keys.calls != 0 || k.jwtCalls != 0 || k.upstream != nil {
		t.Fatalf("no se resuelve, no se prueba como JWT y no llega al servicio: %d %d", keys.calls, k.jwtCalls)
	}
}

func TestClaveSinAlcance(t *testing.T) {
	k := newKeyHarness(t, &stubKeys{p: sendPrincipal("read")}, 100)
	if rec := k.do(http.MethodPost, "/api/v1/transactional/messages", testKey); rec.Code != http.StatusForbidden || k.upstream != nil {
		t.Fatalf("una clave de solo lectura no envia: %d", rec.Code)
	}
	other := sendPrincipal()
	other.Scopes = []middleware.APIKeyScope{{Module: "campaigns", Resource: "campaigns", Action: "create"}}
	k = newKeyHarness(t, &stubKeys{p: other}, 100)
	if rec := k.do(http.MethodPost, "/api/v1/transactional/messages", testKey); rec.Code != http.StatusForbidden {
		t.Fatalf("el alcance de otro modulo no sirve: %d", rec.Code)
	}
}

func TestClaveInvalidaOSinComprobar(t *testing.T) {
	k := newKeyHarness(t, &stubKeys{p: sendPrincipal("create")}, 100)
	if rec := k.do(http.MethodPost, "/api/v1/transactional/messages", "cfm_abcdefgh2345_otra"); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "API_KEY_INVALID") {
		t.Fatalf("invalida: %d %s", rec.Code, rec.Body.String())
	}
	k = newKeyHarness(t, &stubKeys{err: errors.Join(apikey.ErrUnavailable, errors.New("caido"))}, 100)
	if rec := k.do(http.MethodPost, "/api/v1/transactional/messages", testKey); rec.Code != http.StatusServiceUnavailable || k.upstream != nil {
		t.Fatalf("access-control caido: falla cerrado: %d", rec.Code)
	}
}

func TestCupoPorClave(t *testing.T) {
	k := newKeyHarness(t, &stubKeys{p: sendPrincipal("create")}, 1)
	if rec := k.do(http.MethodPost, "/api/v1/transactional/messages", testKey); rec.Code != http.StatusAccepted {
		t.Fatalf("primera: %d", rec.Code)
	}
	rec := k.do(http.MethodPost, "/api/v1/transactional/messages", testKey)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("segunda por encima del cupo: %d", rec.Code)
	}
}

// Un bearer que no es una clave sigue por el JWT, en cualquier ruta.
func TestSinClaveSigueElJWT(t *testing.T) {
	keys := &stubKeys{p: sendPrincipal("create")}
	k := newKeyHarness(t, keys, 100)
	if rec := k.do(http.MethodPost, "/api/v1/transactional/messages", "eyJhbGciOiJFZERTQSJ9.x.y"); rec.Code != http.StatusUnauthorized || k.jwtCalls != 1 {
		t.Fatalf("JWT: %d %d", rec.Code, k.jwtCalls)
	}
	if rec := k.do(http.MethodPost, "/api/v1/transactional/messages", ""); rec.Code != http.StatusUnauthorized || k.jwtCalls != 2 || keys.calls != 0 {
		t.Fatalf("sin cabecera: %d", rec.Code)
	}
}

func TestTablaDeRutasDeClave(t *testing.T) {
	base := func() *routeTable {
		t, err := decodeRouteTable(defaultRoutes)
		if err != nil {
			panic(err)
		}
		return t
	}
	for name, mutate := range map[string]func(*routeTable){
		"metodo raro": func(t *routeTable) {
			t.APIKeyRoutes = append(t.APIKeyRoutes, methodPathSpec{Method: "TRACE", Path: "/transactional/x"})
		},
		"comodin": func(t *routeTable) {
			t.APIKeyRoutes = append(t.APIKeyRoutes, methodPathSpec{Method: "GET", Path: "/transactional/*"})
		},
		"relativa": func(t *routeTable) {
			t.APIKeyRoutes = append(t.APIKeyRoutes, methodPathSpec{Method: "GET", Path: "transactional/x"})
		},
		"con subida": func(t *routeTable) {
			t.APIKeyRoutes = append(t.APIKeyRoutes, methodPathSpec{Method: "GET", Path: "/transactional/../roles"})
		},
		"prefijo sin modulo": func(t *routeTable) {
			t.APIKeyRoutes = append(t.APIKeyRoutes, methodPathSpec{Method: "GET", Path: "/access/my-modules"})
		},
		"prefijo desconocido": func(t *routeTable) {
			t.APIKeyRoutes = append(t.APIKeyRoutes, methodPathSpec{Method: "GET", Path: "/nadie/x"})
		},
		"repetida": func(t *routeTable) { t.APIKeyRoutes = append(t.APIKeyRoutes, t.APIKeyRoutes[0]) },
		// La misma ruta en las dos listas daria los mismos poderes a las dos familias.
		"en las dos familias": func(t *routeTable) {
			t.ProvisioningRoutes = append(t.ProvisioningRoutes, t.APIKeyRoutes[0])
		},
		"aprovisionamiento con comodin": func(t *routeTable) {
			t.ProvisioningRoutes = append(t.ProvisioningRoutes, methodPathSpec{Method: "POST", Path: "/transactional/*"})
		},
		"patron que chi no admite": func(t *routeTable) {
			t.APIKeyRoutes = append(t.APIKeyRoutes, methodPathSpec{Method: "GET", Path: "/transactional/{id"})
		},
	} {
		tbl := base()
		mutate(tbl)
		if err := tbl.validate(); err == nil {
			t.Errorf("%s: la tabla deberia rechazarse", name)
		}
	}
	if err := base().validate(); err != nil {
		t.Fatalf("la tabla embebida: %v", err)
	}
}

// Las dos familias de credencial no comparten ninguna ruta: la de envio no entra en las de
// aprovisionamiento y la de aprovisionamiento no entra en las de envio (docs/adr/0017).
func TestFamiliasDeCredencialConRutasDisjuntas(t *testing.T) {
	tbl, err := decodeRouteTable(defaultRoutes)
	if err != nil {
		t.Fatal(err)
	}
	// Una ruta de aprovisionamiento de prueba, colgada de un prefijo con modulo que ya existe.
	tbl.ProvisioningRoutes = append(tbl.ProvisioningRoutes, methodPathSpec{Method: "POST", Path: "/organizations/aprovisionar"})
	if err := tbl.validate(); err != nil {
		t.Fatal(err)
	}
	gate, err := newAPIKeyGate(tbl, &stubKeys{}, middleware.NewRateLimiter(100, time.Minute), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	envio := httptest.NewRequest(http.MethodPost, "/api/v1/transactional/messages", nil)
	aprov := httptest.NewRequest(http.MethodPost, "/api/v1/organizations/aprovisionar", nil)
	for _, c := range []struct {
		nombre string
		req    *http.Request
		kind   string
		quiere bool
	}{
		{"envio en su ruta", envio, apikey.KindSending, true},
		{"envio en la de aprovisionamiento", aprov, apikey.KindSending, false},
		{"aprovisionamiento en la suya", aprov, apikey.KindProvisioning, true},
		{"aprovisionamiento en la de envio", envio, apikey.KindProvisioning, false},
		{"familia desconocida", envio, "inventada", false},
	} {
		if got := gate.allowedRoute(c.req, c.kind); got != c.quiere {
			t.Errorf("%s: %v", c.nombre, got)
		}
	}
}

// Una credencial cuyo prefijo dice una familia y cuya clave guardada es de la otra no autentica,
// aunque la ruta admita esa familia.
func TestFamiliaDelTokenDebeSerLaDeLaClave(t *testing.T) {
	p := sendPrincipal("create")
	p.Kind = apikey.KindProvisioning
	k := newKeyHarness(t, &stubKeys{p: p}, 100)
	rec := k.do(http.MethodPost, "/api/v1/transactional/messages", testKey)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "API_KEY_INVALID") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if k.upstream != nil {
		t.Fatal("no debe llegar al servicio")
	}
}
