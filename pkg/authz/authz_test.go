package authz

import (
	"strings"
	"testing"
)

func TestCheckerFromEnv(t *testing.T) {
	t.Setenv("ENVIRONMENT", "test")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "")
	t.Setenv(accessControlURLEnv, "")
	c, err := CheckerFromEnv()
	if err != nil || c.accessURL != defaultAccessControlURL {
		t.Fatalf("sin variable: %v", err)
	}
	t.Setenv(accessControlURLEnv, "http://ac.interno:7002/")
	if c, err = CheckerFromEnv(); err != nil || c.accessURL != "http://ac.interno:7002" {
		t.Fatalf("con barra final: %v", err)
	}
	for _, raw := range []string{"access-control:8002", "http://access-control:8002/api", "http://access-control:0", "http://u@access-control:8002"} {
		t.Setenv(accessControlURLEnv, raw)
		if _, err := CheckerFromEnv(); err == nil || !strings.Contains(err.Error(), accessControlURLEnv) {
			t.Errorf("%q: %v; se esperaba un error que nombre %s", raw, err, accessControlURLEnv)
		}
	}
}

// El token sigue la regla comun de middleware.InternalGatewayToken: sin el, solo development o
// test arrancan; fuera, el comprobador no se construye.
func TestCheckerFromEnvToken(t *testing.T) {
	t.Setenv(accessControlURLEnv, "")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "")
	for _, env := range []string{"staging", "production", ""} {
		t.Setenv("ENVIRONMENT", env)
		if _, err := CheckerFromEnv(); err == nil || !strings.Contains(err.Error(), "INTERNAL_GATEWAY_TOKEN") {
			t.Errorf("ENVIRONMENT=%q sin token: %v; se esperaba un error que nombre INTERNAL_GATEWAY_TOKEN", env, err)
		}
	}
	t.Setenv("ENVIRONMENT", "development")
	if _, err := CheckerFromEnv(); err != nil {
		t.Fatalf("development sin token: %v", err)
	}
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("INTERNAL_GATEWAY_TOKEN", "token-de-prueba")
	if c, err := CheckerFromEnv(); err != nil || c.token != "token-de-prueba" {
		t.Fatalf("production con token: %v", err)
	}
}
