package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseAPIKeyScopes(t *testing.T) {
	got, ok := ParseAPIKeyScopes("transactional:messages:create, transactional:messages:read")
	if !ok || len(got) != 2 || got[1] != (APIKeyScope{"transactional", "messages", "read"}) {
		t.Fatalf("lista valida: %v %v", got, ok)
	}
	if FormatAPIKeyScopes(got) != "transactional:messages:create,transactional:messages:read" {
		t.Errorf("ida y vuelta: %q", FormatAPIKeyScopes(got))
	}
	for _, bad := range []string{"transactional:messages", "a:b:c:d", "Transactional:messages:read", "a:*:read", "a:b:read,", "a: :b"} {
		if _, ok := ParseAPIKeyScopes(bad); ok {
			t.Errorf("%q no deberia admitirse", bad)
		}
	}
	if s, ok := ParseAPIKeyScopes(" "); !ok || len(s) != 0 {
		t.Errorf("vacio: %v %v", s, ok)
	}
}

// Un cliente no puede presentarse como clave de API mandando las cabeceras que solo escribe el
// gateway.
func TestLasCabecerasDeClaveSeBorranDelCliente(t *testing.T) {
	var keyID string
	h := StripInternalHeaders(InjectFromGateway(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		keyID = GetAPIKeyID(r.Context())
	})))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(HeaderAPIKeyID, "forjada")
	req.Header.Set(HeaderAPIKeyScopes, "access:roles:create")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if keyID != "" {
		t.Fatalf("la clave forjada llego al servicio: %q", keyID)
	}
}

func TestInjectFromGatewayConAlcanceIlegible(t *testing.T) {
	var ctx context.Context
	h := InjectFromGateway(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() }))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(HeaderAPIKeyID, "k1")
	req.Header.Set(HeaderAPIKeyScopes, "roto")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if GetAPIKeyID(ctx) != "k1" || len(GetAPIKeyScopes(ctx)) != 0 || APIKeyAllows(ctx, "roto", "", "") {
		t.Fatalf("un alcance ilegible debe dejar la clave marcada y sin permisos")
	}
}

func TestLimitPerUserCuentaPorClave(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)
	h := rl.LimitPerUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	do := func(key string) int {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		req = req.WithContext(WithAPIKey(req.Context(), key, nil))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if do("a") != http.StatusNoContent || do("a") != http.StatusTooManyRequests {
		t.Fatal("la segunda peticion de la misma clave debia superar el cupo")
	}
	if do("b") != http.StatusNoContent {
		t.Fatal("otra clave desde la misma IP tiene su propio cupo")
	}
}
