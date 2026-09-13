package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
)

// El prefijo autenticado por el servicio llega sin JWT, con el token interno y la ruta
// escapada intacta, conserva la CSP del servicio y manda solo su inicio de sesion por el
// limitador estricto. Las rutas gateadas siguen exigiendo JWT y con una sola CSP.
func TestSelfAuthenticatedEnrutaSinDebilitarOtrasRutas(t *testing.T) {
	var gotToken, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Gateway-Token")
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	strictHits := 0
	strict := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			strictHits++
			next.ServeHTTP(w, r)
		})
	}
	specs := []selfAuthSpec{{Prefix: "webmail", Service: "webmail", StrictLimit: []methodPathSpec{{Method: "POST", Path: "/session"}}}}

	r := chi.NewRouter()
	r.Use(middleware.SecureHeaders)
	r.Route("/api/v1", func(r chi.Router) {
		mountSelfAuthenticated(r, specs, func(string) string { return upstream.URL }, strict, "token-interno")
		r.Handle("/otro/*", reverseProxy(upstream.URL, "token-interno"))
		r.Group(func(r chi.Router) {
			r.Use(middleware.NewJWTAuth("secreto-de-prueba-de-al-menos-treinta-y-dos").Authenticate)
			r.Route("/mailboxes", func(r chi.Router) {
				r.Handle("/*", reverseProxy(upstream.URL, "token-interno"))
			})
		})
	})
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	rec := do(http.MethodGet, "/api/v1/webmail/folders/INBOX%2FProyectos/messages")
	if rec.Code != http.StatusOK {
		t.Fatalf("webmail sin JWT: status = %d", rec.Code)
	}
	if gotToken != "token-interno" {
		t.Fatalf("el servicio debe recibir el token interno, llego %q", gotToken)
	}
	if gotPath != "/api/v1/webmail/folders/INBOX%2FProyectos/messages" {
		t.Fatalf("la ruta escapada debe llegar intacta, llego %q", gotPath)
	}
	csp := rec.Header().Values("Content-Security-Policy")
	if len(csp) != 2 || !strings.Contains(strings.Join(csp, "|"), "default-src 'none'; sandbox") {
		t.Fatalf("deben quedar la CSP del borde y la del servicio: %q", csp)
	}
	if strictHits != 0 {
		t.Fatalf("una lectura no pasa por el limitador estricto")
	}

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if rec := do(method, "/api/v1/webmail/session"); rec.Code != http.StatusOK {
			t.Fatalf("%s /session: status = %d", method, rec.Code)
		}
	}
	if strictHits != 0 {
		t.Fatalf("GET y DELETE de /session no pasan por el limitador estricto")
	}
	if rec := do(http.MethodPost, "/api/v1/webmail/session"); rec.Code != http.StatusOK || strictHits != 1 {
		t.Fatalf("POST /session: status = %d, limitador estricto = %d", rec.Code, strictHits)
	}

	if rec := do(http.MethodGet, "/api/v1/mailboxes/x"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("una ruta gateada sin JWT debe dar 401, dio %d", rec.Code)
	}
	other := do(http.MethodGet, "/api/v1/otro/x")
	if csp := other.Header().Values("Content-Security-Policy"); len(csp) != 1 || strings.Contains(csp[0], "sandbox") {
		t.Fatalf("fuera de self_authenticated manda solo la CSP del borde: %q", csp)
	}
}
