package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
)

// servicioDeCaptacion responde como contacts y templates: la ruta que se pidio, su propia CSP
// (con o sin frame-ancestors) y, en la comprobacion previa, su CORS.
func servicioDeCaptacion(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodOptions:
			w.Header().Set("Access-Control-Allow-Origin", "https://acme.pe")
			w.WriteHeader(http.StatusNoContent)
			return
		case strings.HasSuffix(r.URL.Path, "/sin-politica"):
			w.Header().Set("Content-Type", "text/html")
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'self' https://acme.pe")
		}
		_, _ = io.WriteString(w, "ruta="+r.URL.Path)
	}))
}

func tablaDeCaptacion(t *testing.T, upstream string) *routeTable {
	t.Helper()
	host, port := hostPort(t, upstream)
	t.Setenv("CONTACTS_HOST", host)
	t.Setenv("CONTACTS_HOST_PORT", port)
	tbl := &routeTable{
		Services: map[string]serviceSpec{"contacts": {HostEnv: "CONTACTS_HOST", DefaultHost: "contacts", DefaultPort: "8050"}},
		Routes:   []routeSpec{{Prefix: "contacts", Service: "contacts", Module: "contacts"}},
		Public: []publicRouteSpec{
			{Method: "GET", Path: "/public/contacts/forms/{form}", Service: "contacts", CORS: publicCORSService},
			{Method: "GET", Path: "/public/contacts/forms/{form}/embed", Service: "contacts", Content: publicContentEmbeddableHTML},
			{Method: "GET", Path: "/public/contacts/forms/{form}/sin-politica", Service: "contacts", Content: publicContentEmbeddableHTML},
			{Method: "GET", Path: "/public/contacts/pages/{tenant}/{slug}", Service: "contacts", Content: publicContentUntrustedHTML, Alias: "/p/{tenant}/{slug}"},
		},
	}
	if err := tbl.validate(); err != nil {
		t.Fatal(err)
	}
	if err := tbl.loadUpstreams(); err != nil {
		t.Fatal(err)
	}
	return tbl
}

func gatewayDeCaptacion(tbl *routeTable) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.SecureHeaders)
	r.Use(corsExceptService(tbl, cors.Handler(cors.Options{
		AllowedOrigins: []string{"https://app.plataforma.io"}, AllowedMethods: []string{"GET", "POST"},
		AllowCredentials: true,
	})))
	pass := func(next http.Handler) http.Handler { return next }
	mountPublicAliases(r, tbl, tokenInternoPrueba, pass)
	r.Route("/api/v1", func(r chi.Router) {
		mountPublic(r, tbl, tokenInternoPrueba, pass)
	})
	return r
}

func TestEmbeddableHTMLSoloConLaPoliticaDelServicio(t *testing.T) {
	up := servicioDeCaptacion(t)
	defer up.Close()
	gw := gatewayDeCaptacion(tablaDeCaptacion(t, up.URL))

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/public/contacts/forms/k/embed", nil))
	csp := rec.Header().Values("Content-Security-Policy")
	if len(csp) != 1 || !strings.Contains(csp[0], "frame-ancestors 'self' https://acme.pe") {
		t.Fatalf("la politica de marco es la del servicio: %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "" {
		t.Fatal("el X-Frame-Options del borde impide incrustar el formulario")
	}

	rec = httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/public/contacts/forms/k/sin-politica", nil))
	if got := rec.Header().Get("Content-Security-Policy"); got != embeddableFallbackCSP || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("sin frame-ancestors del servicio no se deja incrustar: %q", got)
	}
}

func TestCORSDelServicioYComprobacionPrevia(t *testing.T) {
	up := servicioDeCaptacion(t)
	defer up.Close()
	gw := gatewayDeCaptacion(tablaDeCaptacion(t, up.URL))

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/public/contacts/forms/k", nil)
	req.Header.Set("Origin", "https://acme.pe")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://acme.pe" {
		t.Fatalf("la comprobacion previa la contesta el servicio: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("el CORS del gateway, con credenciales, se aplica a una ruta del servicio")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/public/contacts/forms/k/embed", nil)
	req.Header.Set("Origin", "https://app.plataforma.io")
	rec = httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("el resto de rutas conserva el CORS del gateway")
	}
}

func TestAliasEnLaRaiz(t *testing.T) {
	up := servicioDeCaptacion(t)
	defer up.Close()
	gw := gatewayDeCaptacion(tablaDeCaptacion(t, up.URL))
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/acme/oferta-verano", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ruta=/api/v1/public/contacts/pages/acme/oferta-verano" {
		t.Fatalf("el alias llega a la ruta declarada: %d %q", rec.Code, rec.Body.String())
	}
	if len(rec.Header().Values("Content-Security-Policy")) != 2 {
		t.Fatalf("el alias conserva la politica del servicio: %v", rec.Header().Values("Content-Security-Policy"))
	}
	rec = httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/acme%2F..%2Fadmin/x", nil))
	if strings.Contains(rec.Body.String(), "/admin") && !strings.Contains(rec.Body.String(), "%2F") {
		t.Fatalf("un parametro con barras escapa de la ruta: %q", rec.Body.String())
	}
}

func TestValidacionDeRutasPublicasDeCaptacion(t *testing.T) {
	base := publicRouteSpec{Method: "GET", Path: "/public/contacts/forms/{form}", Service: "contacts"}
	bad := map[string]func(p *publicRouteSpec){
		"cors desconocido":      func(p *publicRouteSpec) { p.CORS = "gateway" },
		"embeddable en DELETE":  func(p *publicRouteSpec) { p.Method = "DELETE"; p.Content = publicContentEmbeddableHTML },
		"untrusted en POST":     func(p *publicRouteSpec) { p.Method = "POST"; p.Content = publicContentUntrustedHTML },
		"contenido desconocido": func(p *publicRouteSpec) { p.Content = "html" },
		"alias en POST":         func(p *publicRouteSpec) { p.Method = "POST"; p.Alias = "/f/{form}" },
		"alias sin parametro":   func(p *publicRouteSpec) { p.Alias = "/f" },
		"alias de otro param":   func(p *publicRouteSpec) { p.Alias = "/f/{otro}" },
		"alias sobre /api":      func(p *publicRouteSpec) { p.Alias = "/api/{form}" },
		"alias sobre /media":    func(p *publicRouteSpec) { p.Alias = "/media/{form}" },
		"alias con ..":          func(p *publicRouteSpec) { p.Alias = "/../{form}" },
		"webhook con cors":      func(p *publicRouteSpec) { p.CORS = publicCORSService; p.Limit = publicLimitWebhook },
		"embeddable en PUT":     func(p *publicRouteSpec) { p.Content = publicContentEmbeddableHTML; p.Method = "PUT" },
	}
	for name, mutate := range bad {
		p := base
		mutate(&p)
		if err := validatePublicExtras(p); err == nil {
			t.Errorf("%s: aceptada", name)
		}
	}
	ok := base
	ok.CORS, ok.Alias = publicCORSService, "/f/{form}"
	if err := validatePublicExtras(ok); err != nil {
		t.Fatal(err)
	}
}

// La tabla embebida declara la captacion como la esperan contacts y templates.
func TestTablaEmbebidaDeclaraLaCaptacion(t *testing.T) {
	tbl, err := decodeRouteTable(defaultRoutes)
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.validate(); err != nil {
		t.Fatal(err)
	}
	want := map[string]publicRouteSpec{
		"POST /public/contacts/forms/{form}/submit":   {CORS: publicCORSService, Content: publicContentEmbeddableHTML},
		"GET /public/contacts/forms/{form}":           {CORS: publicCORSService},
		"GET /public/contacts/forms/{form}/embed":     {Content: publicContentEmbeddableHTML},
		"GET /public/templates/pages/{tenant}/{slug}": {Content: publicContentUntrustedHTML, Alias: "/p/{tenant}/{slug}"},
	}
	for _, p := range tbl.Public {
		w, ok := want[p.Method+" "+p.Path]
		if !ok {
			continue
		}
		if p.CORS != w.CORS || p.Content != w.Content || p.Alias != w.Alias || p.Limit != "" {
			t.Errorf("%s %s: %+v", p.Method, p.Path, p)
		}
		delete(want, p.Method+" "+p.Path)
	}
	if len(want) != 0 {
		t.Fatalf("rutas que faltan: %v", want)
	}
}
