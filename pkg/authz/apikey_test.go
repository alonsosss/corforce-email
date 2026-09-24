package authz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
)

// Una clave de API se juzga solo por su alcance: sin consultar access-control (la URL no
// responde) y sin que un rol inyectado por error la convierta en administradora.
func TestLaClaveSeJuzgaSoloPorSuAlcance(t *testing.T) {
	c := NewChecker("http://127.0.0.1:1", "")
	ctx := middleware.WithAPIKey(context.Background(), "k1", []middleware.APIKeyScope{
		{Module: "transactional", Resource: "messages", Action: "create"},
	})
	ctx = context.WithValue(ctx, middleware.CtxRoles, []string{middleware.RoleTenantAdmin})
	if ok, err := c.Allowed(ctx, "transactional", "messages", "create"); !ok || err != nil {
		t.Fatalf("permiso del alcance: %v, %v", ok, err)
	}
	for _, p := range [][3]string{{"transactional", "messages", "read"}, {"transactional", "stats", "read"}, {"access", "roles", "create"}} {
		if ok, err := c.Allowed(ctx, p[0], p[1], p[2]); ok || err != nil {
			t.Errorf("%v fuera del alcance concedido: %v, %v", p, ok, err)
		}
	}
}

func TestRequirePermissionConClave(t *testing.T) {
	c := NewChecker("http://127.0.0.1:1", "")
	h := middleware.InjectFromGateway(c.RequirePermission("transactional", "messages", "read")(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	for scopes, want := range map[string]int{
		"transactional:messages:read":   http.StatusNoContent,
		"transactional:messages:create": http.StatusForbidden,
		"":                              http.StatusForbidden,
		"transactional:messages:read,esto no es un permiso":         http.StatusForbidden,
		"transactional:messages:create,transactional:messages:read": http.StatusNoContent,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(middleware.HeaderAPIKeyID, "k1")
		req.Header.Set(middleware.HeaderAPIKeyScopes, scopes)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("alcance %q: %d, se esperaba %d", scopes, rec.Code, want)
		}
	}
}
