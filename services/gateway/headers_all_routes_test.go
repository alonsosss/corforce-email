package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const (
	tokenInternoPrueba = "token-interno-de-prueba"
	usuarioDelToken    = "7f0b1e2c-0000-4000-8000-000000000001"
	empresaDelToken    = "7f0b1e2c-0000-4000-8000-0000000000aa"
	rolDelToken        = "empleado"
	ipDelVisitante     = "203.0.113.7"
	ipFalsa            = "198.51.100.1"
)

// destinoGrabador recibe todo lo que el gateway reenvia y guarda la ultima peticion.
type destinoGrabador struct {
	mu   sync.Mutex
	last http.Header
}

func (d *destinoGrabador) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.last = r.Header.Clone()
	d.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (d *destinoGrabador) recibido() http.Header {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// gatewayConLaTablaReal monta, con la tabla de rutas embebida (la de produccion) y en el mismo orden
// que main.go, todo lo que el gateway monta para la peticion de un cliente. Todos los servicios
// apuntan a un mismo destino que graba lo recibido. El RBAC y la comprobacion de sesion no estan: no
// tocan las cabeceras.
func gatewayConLaTablaReal(t *testing.T, destino http.Handler) (http.Handler, *routeTable, string) {
	t.Helper()
	upstream := httptest.NewServer(destino)
	t.Cleanup(upstream.Close)
	host, port := hostPort(t, upstream.URL)

	tbl, err := decodeRouteTable(defaultRoutes)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tbl.Services {
		t.Setenv(s.HostEnv, host)
		t.Setenv(s.HostEnv+"_PORT", port)
	}
	if err := tbl.validate(); err != nil {
		t.Fatal(err)
	}
	if err := tbl.loadUpstreams(); err != nil {
		t.Fatal(err)
	}
	if err := tbl.loadCellTargets(); err != nil {
		t.Fatal(err)
	}

	signer := testSigner(t, "prueba")
	public, err := auth.EncodePublicKey(signer.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(auth.EnvPublicKeys, "prueba:"+public)
	jwtAuth, _, err := jwtAuthFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := identityTokens(t, signer).GeneratePair(usuarioDelToken, empresaDelToken, []string{rolDelToken})
	if err != nil {
		t.Fatal(err)
	}

	logger := zap.NewNop()
	pass := func(next http.Handler) http.Handler { return next }
	r := chi.NewRouter()
	r.Use(middleware.StripInternalHeaders)
	r.Use(webdavGuard(tbl))
	r.Use(captureTargetCell)
	r.Use(middleware.CaptureClientIP(middleware.TrustedProxyCIDRs("")))
	mountWellKnown(r, tbl, pass)
	r.Handle("/*", reverseProxy(tbl.serviceURL(tbl.Frontend), tokenInternoPrueba))
	r.Route("/api/v1", func(r chi.Router) {
		mountPublic(r, tbl, tokenInternoPrueba)
		mountSelfAuthenticated(r, tbl, pass, tokenInternoPrueba, nil, logger)
		r.Group(func(r chi.Router) {
			r.Use(jwtAuth.Authenticate)
			handlers := sessionHandlers(tbl, tokenInternoPrueba, nil, logger)
			for _, rt := range tbl.Routes {
				proxy := handlers[rt.Service]
				r.Route("/"+rt.Prefix, func(r chi.Router) { r.Handle("/*", proxy) })
			}
		})
	})
	return r, tbl, pair.AccessToken
}

type peticionDePrueba struct {
	method, path string
	conSesion    bool
}

// todasLasPeticiones enumera, desde la tabla, una peticion por cada ruta publica, cada prefijo
// autenticado por el servicio (con cada uno de sus metodos), cada ruta con sesion y la aplicacion.
func todasLasPeticiones(tbl *routeTable) []peticionDePrueba {
	concreta := strings.NewReplacer("{cell}", "pe-01", "{tenantID}", empresaDelToken, "{domain}", "acme.test")
	var out []peticionDePrueba
	for _, p := range tbl.Public {
		out = append(out, peticionDePrueba{method: p.Method, path: apiPrefix + concreta.Replace(p.Path)})
	}
	basicos := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}
	for _, s := range tbl.SelfAuthenticated {
		for _, m := range append(append([]string{}, basicos...), s.Methods...) {
			out = append(out, peticionDePrueba{method: m, path: apiPrefix + s.Prefix + "/recurso"})
		}
	}
	for _, rt := range tbl.Routes {
		for _, m := range basicos {
			out = append(out, peticionDePrueba{method: m, path: apiPrefix + rt.Prefix + "/recurso", conSesion: true})
		}
	}
	return append(out, peticionDePrueba{method: http.MethodGet, path: "/recurso-de-la-aplicacion"})
}

// En NINGUNA ruta un cliente externo fija las cabeceras internas ni las quita: ni las de identidad
// (X-User-ID, X-Tenant-ID, X-User-Roles), ni el token del gateway, ni la celda del operador, ni la IP
// del visitante; ni escribiendolas ni nombrandolas en Connection para que el proxy las borre. Las de
// identidad solo llegan con sesion y son las del token.
func TestElClienteNoFijaNiQuitaCabecerasInternasEnNingunaRuta(t *testing.T) {
	destino := &destinoGrabador{}
	gw, tbl, token := gatewayConLaTablaReal(t, destino)

	forjadas := map[string]string{
		"X-User-ID": "usuario-forjado", "X-Tenant-ID": "empresa-forjada", "X-User-Roles": middleware.RoleSuperadmin,
		"X-Gateway-Token": "token-forjado", "X-Internal-Token": "interno-forjado", middleware.HeaderOperatorCell: "pe-02",
		targetCellHeader: "pe-02", "X-Real-IP": ipFalsa, "X-Forwarded-For": ipFalsa,
	}
	quitar := "X-User-ID, X-Tenant-ID, X-User-Roles, X-Gateway-Token, X-Real-IP, X-Forwarded-Host, " + middleware.HeaderOperatorCell

	peticiones := todasLasPeticiones(tbl)
	if len(peticiones) < 60 {
		t.Fatalf("la enumeracion de rutas quedo corta: %d", len(peticiones))
	}
	for _, p := range peticiones {
		req := httptest.NewRequest(p.method, "http://mail.acme.test"+p.path, nil)
		req.RemoteAddr = ipDelVisitante + ":40000"
		for name, value := range forjadas {
			req.Header.Set(name, value)
		}
		req.Header.Set("Connection", quitar)
		if p.conSesion {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		destino.last = nil
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, req)

		got := destino.recibido()
		if got == nil {
			// Una ruta que no llego al servicio (405 de un metodo no declarado, 301 del descubrimiento) no
			// filtra nada.
			continue
		}
		fallo := func(format string, args ...any) {
			t.Errorf("%s %s: "+format, append([]any{p.method, p.path}, args...)...)
		}
		if got.Get("X-Gateway-Token") != tokenInternoPrueba {
			fallo("X-Gateway-Token = %q", got.Get("X-Gateway-Token"))
		}
		for _, name := range []string{"X-Internal-Token", middleware.HeaderOperatorCell, targetCellHeader} {
			if got.Get(name) != "" {
				fallo("%s llego al servicio: %q", name, got.Get(name))
			}
		}
		if p.conSesion {
			if got.Get("X-User-ID") != usuarioDelToken || got.Get("X-Tenant-ID") != empresaDelToken || got.Get("X-User-Roles") != rolDelToken {
				fallo("identidad %q %q %q, la del token es %q %q %q", got.Get("X-User-ID"), got.Get("X-Tenant-ID"), got.Get("X-User-Roles"),
					usuarioDelToken, empresaDelToken, rolDelToken)
			}
		} else if got.Get("X-User-ID") != "" || got.Get("X-Tenant-ID") != "" || got.Get("X-User-Roles") != "" {
			fallo("una ruta sin sesion recibio identidad: %q %q %q", got.Get("X-User-ID"), got.Get("X-Tenant-ID"), got.Get("X-User-Roles"))
		}
		if got.Get("X-Real-IP") != ipDelVisitante {
			fallo("X-Real-IP = %q, se esperaba la IP de la conexion %q", got.Get("X-Real-IP"), ipDelVisitante)
		}
		if strings.Contains(got.Get("X-Forwarded-For"), ipFalsa) {
			fallo("X-Forwarded-For arrastra lo que envio el cliente: %q", got.Get("X-Forwarded-For"))
		}
		if got.Get("X-Forwarded-Host") != "mail.acme.test" {
			fallo("X-Forwarded-Host = %q", got.Get("X-Forwarded-Host"))
		}
	}
}
