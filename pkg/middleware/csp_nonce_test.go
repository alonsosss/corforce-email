package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// El nonce es de un solo uso: dos respuestas nunca comparten valor, o un atacante podria
// reutilizar el de una pagina anterior para colar un script.
func TestNonceEsDistintoPorRespuesta(t *testing.T) {
	var vistos []string
	h := SecureHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vistos = append(vistos, CSPNonce(r.Context()))
	}))
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "'nonce-"+vistos[i]+"'") {
			t.Fatalf("la politica no declara el nonce de la respuesta: %s", csp)
		}
	}
	if vistos[0] == vistos[1] || vistos[1] == vistos[2] {
		t.Errorf("nonce repetido entre respuestas: %v", vistos)
	}
}

// La politica no debe admitir inline ni eval bajo ninguna circunstancia.
func TestLaPoliticaNuncaAdmiteInlineNiEval(t *testing.T) {
	rec := httptest.NewRecorder()
	SecureHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	script := csp[strings.Index(csp, "script-src"):]
	script = script[:strings.Index(script, ";")]
	for _, prohibido := range []string{"'unsafe-inline'", "'unsafe-eval'"} {
		if strings.Contains(script, prohibido) {
			t.Errorf("script-src admite %s: %s", prohibido, script)
		}
	}
	if !strings.Contains(script, "'strict-dynamic'") {
		t.Errorf("falta strict-dynamic (la confianza debe venir del nonce, no del origen): %s", script)
	}
	for _, dir := range []string{"object-src 'none'", "base-uri 'self'", "form-action 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, dir) {
			t.Errorf("falta la directiva %q", dir)
		}
	}
}

// Las fotos del modulo biometrico —la de una marcacion, el rostro con el que
// quedo registrado un trabajador— se piden con la cabecera de sesion, asi que no
// pueden ir en un <img src> directo: se descargan y se muestran desde memoria,
// con una URL blob:. Si la politica no la admite, el navegador bloquea la imagen
// SIN error visible: la peticion aparece correcta en la red y el recuadro sale
// roto, que es como se descubrio.
func TestLaPoliticaAdmiteImagenesEnMemoria(t *testing.T) {
	rec := httptest.NewRecorder()
	SecureHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	var imgSrc string
	for _, d := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "img-src") {
			imgSrc = strings.TrimSpace(d)
		}
	}
	if imgSrc == "" {
		t.Fatal("la politica no declara img-src")
	}
	if !strings.Contains(imgSrc, "blob:") {
		t.Errorf("img-src = %q: sin blob: las fotos del biometrico no se ven", imgSrc)
	}
}
