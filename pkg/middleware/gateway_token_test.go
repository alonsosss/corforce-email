package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

var gatewayTokenEnvironments = map[string]bool{
	"development": true, "test": true, "Development": true,
	"production": false, "staging": false, "": false, "prod": false,
}

func TestInternalGatewayTokenSoloFaltaEnDesarrollo(t *testing.T) {
	for environment, allowed := range gatewayTokenEnvironments {
		t.Setenv("ENVIRONMENT", environment)
		t.Setenv("INTERNAL_GATEWAY_TOKEN", "")
		token, err := InternalGatewayToken()
		if allowed && (err != nil || token != "") {
			t.Errorf("ENVIRONMENT=%q sin token: %q, %v", environment, token, err)
		}
		if !allowed && !errors.Is(err, ErrGatewayTokenRequired) {
			t.Errorf("ENVIRONMENT=%q sin token: %v, se esperaba ErrGatewayTokenRequired", environment, err)
		}

		t.Setenv("INTERNAL_GATEWAY_TOKEN", "gateway-token-0123456789")
		if token, err := InternalGatewayToken(); err != nil || token != "gateway-token-0123456789" {
			t.Errorf("ENVIRONMENT=%q con token: %q, %v", environment, token, err)
		}
	}
}

func serveGatewayToken(header string) (code int, reached bool) {
	h := RequireGatewayToken(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		req.Header.Set("X-Gateway-Token", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, reached
}

// Sin token configurado, fuera de desarrollo o prueba el servicio responde 503 a todo en vez
// de quedar abierto a quien alcance su puerto.
func TestRequireGatewayTokenSinConfigurar(t *testing.T) {
	for environment, allowed := range gatewayTokenEnvironments {
		t.Setenv("ENVIRONMENT", environment)
		t.Setenv("INTERNAL_GATEWAY_TOKEN", "")
		code, reached := serveGatewayToken("cualquiera")
		if allowed && (code != http.StatusNoContent || !reached) {
			t.Errorf("ENVIRONMENT=%q: %d, alcanzado %v", environment, code, reached)
		}
		if !allowed && (code != http.StatusServiceUnavailable || reached) {
			t.Errorf("ENVIRONMENT=%q: %d, alcanzado %v; se esperaba 503", environment, code, reached)
		}
	}
}

func TestRequireGatewayTokenConfigurado(t *testing.T) {
	t.Setenv("ENVIRONMENT", "staging")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "gateway-token-0123456789")
	cases := map[string]int{"": http.StatusUnauthorized, "otro": http.StatusUnauthorized, "gateway-token-0123456789": http.StatusNoContent}
	for header, want := range cases {
		if code, reached := serveGatewayToken(header); code != want || reached != (want == http.StatusNoContent) {
			t.Errorf("cabecera %q: %d, alcanzado %v; se esperaba %d", header, code, reached, want)
		}
	}
}
