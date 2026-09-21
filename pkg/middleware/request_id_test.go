package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// El identificador de peticion lo elige el cliente y acaba en el registro de acceso, en el rastro
// de auditoria (audit.audit_logs.request_id es varchar(100)) y en la cabecera de respuesta: uno
// largo o con caracteres raros tiraria el apunte de auditoria de esa escritura.
func TestElIdentificadorDePeticionDelClienteSeAcotaYSeSanea(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = GetRequestID(r.Context())
	}))

	casos := []struct {
		nombre, valor string
		conserva      bool
	}{
		{"uuid", "3f2b8c1e-5d0a-4f6e-9a7b-1c2d3e4f5a6b", true},
		{"token de un proxy", "req_01H8.abc-DEF", true},
		{"vacio", "", false},
		{"demasiado largo", strings.Repeat("a", maxRequestIDLen+1), false},
		{"muy largo", strings.Repeat("a", 5000), false},
		{"con espacios", "a b", false},
		{"con comillas", `a"b`, false},
		{"con barra", "a/b", false},
		{"no ascii", "peticion-ñ", false},
	}
	for _, c := range casos {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if c.valor != "" {
			req.Header.Set("X-Request-ID", c.valor)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if c.conserva && seen != c.valor {
			t.Errorf("%s: se esperaba conservar %q, salio %q", c.nombre, c.valor, seen)
		}
		if !c.conserva && (seen == c.valor || len(seen) == 0 || len(seen) > maxRequestIDLen) {
			t.Errorf("%s: se esperaba un identificador nuevo, salio %q", c.nombre, seen)
		}
		if rec.Header().Get("X-Request-ID") != seen {
			t.Errorf("%s: la respuesta lleva %q y el contexto %q", c.nombre, rec.Header().Get("X-Request-ID"), seen)
		}
	}
}
