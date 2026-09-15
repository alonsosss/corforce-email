package authz

import (
	"strings"
	"testing"
)

func TestCheckerFromEnv(t *testing.T) {
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
