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

// newServer monta las rutas sin caso de uso: los tests solo recorren caminos que se
// resuelven antes de llegar a el (autorizacion, identificador o cuerpo invalidos).
func newServer(accessURL string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(nil, authz.NewChecker(accessURL, "")).Routes())
	return r
}

func call(t *testing.T, srv http.Handler, method, path, body, roles string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-Tenant-ID", uuid.NewString())
	req.Header.Set("X-User-Roles", roles)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code
}

func TestLasRutasDePlataformaSiguenExigiendoSuperadmin(t *testing.T) {
	// Aunque la politica trajera el permiso (ningun rol de empresa puede tenerlo), el rol
	// superadmin sigue siendo obligatorio en las rutas que operan la plataforma.
	srv := newServer(policyStub(t, "organization/tenants/create", "organization/cells/read"))

	if code := call(t, srv, http.MethodPost, "/api/v1/organizations", `{}`, "operador"); code != http.StatusForbidden {
		t.Fatalf("alta de empresa por un rol de empresa: %d, se esperaba 403", code)
	}
	if code := call(t, srv, http.MethodGet, "/api/v1/cells", "", middleware.RoleTenantAdmin); code != http.StatusForbidden {
		t.Fatalf("celdas vistas por el tenant_admin: %d, se esperaba 403", code)
	}
}

func TestEscrituraSinPermisoDevuelve403(t *testing.T) {
	srv := newServer(policyStub(t))
	path := "/api/v1/organizations/" + uuid.NewString() + "/reseed-roles"
	if code := call(t, srv, http.MethodPost, path, "", "operador"); code != http.StatusForbidden {
		t.Fatalf("resembrar roles sin rol del sistema: %d, se esperaba 403", code)
	}
}

func TestLecturaSinPermisoDevuelve403(t *testing.T) {
	srv := newServer(policyStub(t))
	if code := call(t, srv, http.MethodGet, "/api/v1/organizations/"+uuid.NewString(), "", "operador"); code != http.StatusForbidden {
		t.Fatalf("ficha de empresa sin organization/tenants/read: %d, se esperaba 403", code)
	}
}

func TestPermisoNoComprobableDevuelve503(t *testing.T) {
	srv := newServer(unreachable)
	if code := call(t, srv, http.MethodGet, "/api/v1/organizations/"+uuid.NewString(), "", "operador"); code != http.StatusServiceUnavailable {
		t.Fatalf("access-control caido: %d, se esperaba 503", code)
	}
}

func TestLosRolesDelSistemaPasanSinConsultar(t *testing.T) {
	srv := newServer(unreachable)

	// Identificador invalido: el 400 sale del handler, despues de la autorizacion.
	if code := call(t, srv, http.MethodPost, "/api/v1/organizations/no-es-uuid/reseed-roles", "", middleware.RoleTenantAdmin); code != http.StatusBadRequest {
		t.Fatalf("tenant_admin resembrando: %d, se esperaba 400 del handler", code)
	}
	if code := call(t, srv, http.MethodGet, "/api/v1/organizations/no-es-uuid", "", middleware.RoleTenantAdmin); code != http.StatusBadRequest {
		t.Fatalf("tenant_admin leyendo su empresa: %d, se esperaba 400 del handler", code)
	}
	if code := call(t, srv, http.MethodPost, "/api/v1/cells", `{}`, middleware.RoleSuperadmin); code != http.StatusUnprocessableEntity {
		t.Fatalf("superadmin dando de alta una celda: %d, se esperaba 422 del handler", code)
	}
	if code := call(t, srv, http.MethodPost, "/api/v1/organizations", `{}`, middleware.RoleSuperadmin); code != http.StatusUnprocessableEntity {
		t.Fatalf("superadmin dando de alta una empresa: %d, se esperaba 422 del handler", code)
	}
}
