package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// unreachable es una direccion donde no escucha nadie: si un test que la usa pasa, el
// permiso no se consulto (o, si se consulto, respondio 503).
const unreachable = "http://127.0.0.1:1"

// tenantInvalido hace que el handler responda 400 antes de tocar el caso de uso: los tests
// montan las rutas sin caso de uso y un 400 prueba que la autorizacion dejo pasar.
const tenantInvalido = "no-es-uuid"

// catalogo son los permisos del modulo audit sembrados en
// migrations/registry/005_seed_permissions.sql.
var catalogo = []string{
	"audit/logs/read", "audit/logs/create",
	"audit/security_events/read", "audit/security_events/acknowledge",
	"audit/integrity/read", "audit/integrity/verify",
}

type ruta struct {
	method, path, body string
	perm               string
}

const base = "/api/v1/audit"

var rutas = []ruta{
	{http.MethodGet, base + "/logs", "", "audit/logs/read"},
	{http.MethodPost, base + "/logs", `{}`, "audit/logs/create"},
	{http.MethodPost, base + "/logs/bulk", `{}`, "audit/logs/create"},
	{http.MethodGet, base + "/logs/no-es-uuid", "", "audit/logs/read"},
	{http.MethodGet, base + "/security-events", "", "audit/security_events/read"},
	{http.MethodGet, base + "/security-events/unacknowledged", "", "audit/security_events/read"},
	{http.MethodGet, base + "/security-events/no-es-uuid", "", "audit/security_events/read"},
	{http.MethodPost, base + "/security-events/no-es-uuid/acknowledge", "", "audit/security_events/acknowledge"},
	{http.MethodGet, base + "/integrity", "", "audit/integrity/verify"},
	{http.MethodGet, base + "/summary", "", "audit/logs/read"},
	{http.MethodGet, base + "/user-activity/no-es-uuid", "", "audit/logs/read"},
	{http.MethodGet, base + "/changes/no-es-uuid", "", "audit/logs/read"},
}

// policyStub hace de access-control: sirve a pkg/authz la politica con los permisos
// dados como "module/resource/action".
func policyStub(t *testing.T, perms ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		list := make([]map[string]string, 0, len(perms))
		for _, p := range perms {
			parts := strings.SplitN(p, "/", 3)
			list = append(list, map[string]string{"module": parts[0], "resource": parts[1], "action": parts[2]})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"permissions": list}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func menos(perms []string, quitar string) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		if p != quitar {
			out = append(out, p)
		}
	}
	return out
}

func newServer(accessURL string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount(base, NewHandler(nil, authz.NewChecker(accessURL, "")).Routes())
	return r
}

func call(t *testing.T, srv http.Handler, rt ruta, roles string) int {
	t.Helper()
	req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-Tenant-ID", tenantInvalido)
	req.Header.Set("X-User-Roles", roles)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code
}

func TestCadaRutaExigeSuPermisoDeAccion(t *testing.T) {
	for _, rt := range rutas {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			if code := call(t, newServer(policyStub(t)), rt, "auditor"); code != http.StatusForbidden {
				t.Fatalf("sin ningun permiso: %d, se esperaba 403", code)
			}
			// Todo el catalogo del modulo menos el permiso de la ruta: ningun otro lo
			// sustituye (leer eventos no deja reconocerlos, integrity/read no verifica).
			if code := call(t, newServer(policyStub(t, menos(catalogo, rt.perm)...)), rt, "auditor"); code != http.StatusForbidden {
				t.Fatalf("sin %s y con el resto del modulo: %d, se esperaba 403", rt.perm, code)
			}
			if code := call(t, newServer(policyStub(t, rt.perm)), rt, "auditor"); code != http.StatusBadRequest {
				t.Fatalf("con %s: %d, se esperaba 400 del handler", rt.perm, code)
			}
		})
	}
}

func TestLosRolesDelSistemaPasanSinConsultar(t *testing.T) {
	srv := newServer(unreachable)
	for _, role := range []string{middleware.RoleTenantAdmin, middleware.RoleSuperadmin} {
		for _, rt := range rutas {
			if code := call(t, srv, rt, role); code != http.StatusBadRequest {
				t.Errorf("%s en %s %s: %d, se esperaba 400 del handler", role, rt.method, rt.path, code)
			}
		}
	}
}

func TestPermisoNoComprobableDevuelve503(t *testing.T) {
	srv := newServer(unreachable)
	for _, rt := range []ruta{rutas[4], rutas[7]} {
		if code := call(t, srv, rt, "auditor"); code != http.StatusServiceUnavailable {
			t.Fatalf("access-control caido en %s %s: %d, se esperaba 503", rt.method, rt.path, code)
		}
	}
}
