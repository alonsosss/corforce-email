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

const politicaDelServicio = "default-src 'none'; sandbox"

// servicioConScript responde un documento con un <script> y su propia CSP, como haria el HTML
// de un correo que se colara por el render.
func servicioConScript() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", politicaDelServicio)
		_, _ = io.WriteString(w, `<html><body><script>alert(1)</script><p>correo</p></body></html>`)
	}))
}

func pedirDocumento(t *testing.T, tbl *routeTable, path string) *http.Response {
	t.Helper()
	r := chi.NewRouter()
	r.Use(middleware.SecureHeaders)
	r.Route("/api/v1", func(r chi.Router) {
		mountPublic(r, tbl, tokenInternoPrueba, func(next http.Handler) http.Handler { return next })
	})
	front := httptest.NewServer(r)
	t.Cleanup(front.Close)
	req, err := http.NewRequest(http.MethodGet, front.URL+"/api/v1"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// Una ruta publica con content untrusted_html llega al navegador con la politica del servicio
// ademas de la del borde (se aplica la interseccion) y sin que el gateway marque sus <script>
// con el nonce de la aplicacion. Una ruta publica cualquiera conserva el comportamiento de
// siempre: una sola politica, la del borde.
func TestPublicaDeContenidoDeTercerosConservaLaPoliticaDelServicio(t *testing.T) {
	upstream := servicioConScript()
	defer upstream.Close()
	host, port := hostPort(t, upstream.URL)
	t.Setenv("TRANSACTIONAL_HOST", host)
	t.Setenv("TRANSACTIONAL_HOST_PORT", port)
	tbl := &routeTable{
		Services: map[string]serviceSpec{"transactional": {HostEnv: "TRANSACTIONAL_HOST", DefaultHost: "transactional", DefaultPort: "8045"}},
		Routes:   []routeSpec{{Prefix: "transactional", Service: "transactional", Module: "transactional"}},
		Public: []publicRouteSpec{
			{Method: "GET", Path: "/public/transactional/view", Service: "transactional", Content: publicContentUntrustedHTML},
			{Method: "GET", Path: "/public/transactional/unsubscribe", Service: "transactional"},
		},
	}
	if err := tbl.validate(); err != nil {
		t.Fatal(err)
	}
	if err := tbl.loadUpstreams(); err != nil {
		t.Fatal(err)
	}

	resp := pedirDocumento(t, tbl, "/public/transactional/view")
	body, _ := io.ReadAll(resp.Body)
	policies := resp.Header.Values("Content-Security-Policy")
	if len(policies) != 2 || !contiene(policies, politicaDelServicio) {
		t.Fatalf("se conservan las dos politicas: %q", policies)
	}
	if strings.Contains(string(body), "nonce=") {
		t.Fatalf("un script del documento no recibe el nonce: %s", body)
	}

	resp = pedirDocumento(t, tbl, "/public/transactional/unsubscribe")
	body, _ = io.ReadAll(resp.Body)
	policies = resp.Header.Values("Content-Security-Policy")
	if len(policies) != 1 || contiene(policies, politicaDelServicio) {
		t.Fatalf("una ruta publica normal lleva solo la politica del borde: %q", policies)
	}
	if !strings.Contains(string(body), "nonce=") {
		t.Fatalf("una ruta publica normal sigue marcada con el nonce: %s", body)
	}
}

func TestLaTablaRealDeclaraElCorreoEnElNavegadorComoContenidoDeTerceros(t *testing.T) {
	tbl, err := decodeRouteTable(defaultRoutes)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tbl.Public {
		if p.Path == "/public/transactional/view" {
			if p.Method != "GET" || p.Content != publicContentUntrustedHTML {
				t.Fatalf("la ruta del correo en el navegador: %+v", p)
			}
			return
		}
	}
	t.Fatal("falta la ruta publica del correo en el navegador")
}

func contiene(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
