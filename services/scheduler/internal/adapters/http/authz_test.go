package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// unreachable es una direccion donde no escucha nadie: si un test que la usa pasa, el
// permiso no se consulto (o, si se consulto, respondio 503).
const unreachable = "http://127.0.0.1:1"

// catalogo son los permisos del modulo scheduler sembrados en
// migrations/registry/005_seed_permissions.sql.
var catalogo = []string{
	"scheduler/jobs/read", "scheduler/jobs/create", "scheduler/jobs/update",
	"scheduler/jobs/delete", "scheduler/jobs/run",
	"scheduler/executions/read", "scheduler/executions/cancel", "scheduler/executions/retry",
	"scheduler/tasks/read", "scheduler/tasks/create", "scheduler/tasks/cancel",
}

type ruta struct {
	method, path, body string
	perm               string
	// status es la respuesta del handler cuando la autorizacion deja pasar: identificador
	// o cuerpo invalidos, o un listado vacio.
	status int
}

const base = "/api/v1/scheduler"

var rutas = []ruta{
	{http.MethodGet, base + "/jobs", "", "scheduler/jobs/read", http.StatusOK},
	{http.MethodPost, base + "/jobs", `{}`, "scheduler/jobs/create", http.StatusUnprocessableEntity},
	{http.MethodGet, base + "/jobs/no-es-uuid", "", "scheduler/jobs/read", http.StatusBadRequest},
	{http.MethodPut, base + "/jobs/no-es-uuid", `{}`, "scheduler/jobs/update", http.StatusBadRequest},
	{http.MethodPost, base + "/jobs/no-es-uuid/enable", "", "scheduler/jobs/update", http.StatusBadRequest},
	{http.MethodPost, base + "/jobs/no-es-uuid/disable", "", "scheduler/jobs/update", http.StatusBadRequest},
	{http.MethodPost, base + "/jobs/no-es-uuid/run", "", "scheduler/jobs/run", http.StatusBadRequest},
	{http.MethodGet, base + "/jobs/no-es-uuid/history", "", "scheduler/executions/read", http.StatusBadRequest},
	{http.MethodGet, base + "/executions", "", "scheduler/executions/read", http.StatusOK},
	{http.MethodGet, base + "/executions/no-es-uuid", "", "scheduler/executions/read", http.StatusBadRequest},
	{http.MethodPost, base + "/executions/no-es-uuid/cancel", "", "scheduler/executions/cancel", http.StatusBadRequest},
	{http.MethodPost, base + "/executions/no-es-uuid/retry", "", "scheduler/executions/retry", http.StatusBadRequest},
	{http.MethodGet, base + "/tasks", "", "scheduler/tasks/read", http.StatusOK},
	{http.MethodPost, base + "/tasks", `{}`, "scheduler/tasks/create", http.StatusUnprocessableEntity},
	{http.MethodGet, base + "/tasks/no-es-uuid", "", "scheduler/tasks/read", http.StatusBadRequest},
	{http.MethodPost, base + "/tasks/no-es-uuid/cancel", "", "scheduler/tasks/cancel", http.StatusBadRequest},
	{http.MethodGet, base + "/handlers", "", "scheduler/jobs/read", http.StatusOK},
	{http.MethodGet, base + "/meta", "", "scheduler/jobs/read", http.StatusOK},
}

type jobsVacios struct{ ports.JobDefinitionRepository }

func (jobsVacios) List(context.Context, domain.JobFilter) ([]*domain.JobOverview, int64, error) {
	return nil, 0, nil
}

type ejecucionesVacias struct{ ports.JobExecutionRepository }

func (ejecucionesVacias) ListRunning(context.Context, uuid.UUID, int, int) ([]*domain.JobExecution, int64, error) {
	return nil, 0, nil
}

type tareasVacias struct{ ports.ScheduledTaskRepository }

func (tareasVacias) ListPending(context.Context, domain.TaskFilter) ([]*domain.ScheduledTask, int64, error) {
	return nil, 0, nil
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

func authzServer(accessURL string) http.Handler {
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{
		Jobs: jobsVacios{}, Executions: ejecucionesVacias{}, Tasks: tareasVacias{}, Logger: zap.NewNop(),
	})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(Deps{UC: uc, Perms: authz.NewChecker(accessURL, "")}).Routes())
	return r
}

func call(t *testing.T, srv http.Handler, rt ruta, roles string) int {
	t.Helper()
	req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-Tenant-ID", uuid.NewString())
	req.Header.Set("X-User-Roles", roles)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code
}

func TestCadaRutaExigeSuPermisoDeAccion(t *testing.T) {
	for _, rt := range rutas {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			if code := call(t, authzServer(policyStub(t)), rt, "operador"); code != http.StatusForbidden {
				t.Fatalf("sin ningun permiso: %d, se esperaba 403", code)
			}
			// Todo el catalogo del modulo menos el permiso de la ruta: ningun otro lo
			// sustituye (leer no deja lanzar, actualizar no deja cancelar).
			if code := call(t, authzServer(policyStub(t, menos(catalogo, rt.perm)...)), rt, "operador"); code != http.StatusForbidden {
				t.Fatalf("sin %s y con el resto del modulo: %d, se esperaba 403", rt.perm, code)
			}
			if code := call(t, authzServer(policyStub(t, rt.perm)), rt, "operador"); code != rt.status {
				t.Fatalf("con %s: %d, se esperaba %d del handler", rt.perm, code, rt.status)
			}
		})
	}
}

func TestLosRolesDelSistemaPasanSinConsultar(t *testing.T) {
	srv := authzServer(unreachable)
	for _, role := range []string{middleware.RoleTenantAdmin, middleware.RoleSuperadmin} {
		for _, rt := range rutas {
			if code := call(t, srv, rt, role); code != rt.status {
				t.Errorf("%s en %s %s: %d, se esperaba %d del handler", role, rt.method, rt.path, code, rt.status)
			}
		}
	}
}

func TestPermisoNoComprobableDevuelve503(t *testing.T) {
	srv := authzServer(unreachable)
	for _, rt := range []ruta{rutas[0], rutas[6]} {
		if code := call(t, srv, rt, "operador"); code != http.StatusServiceUnavailable {
			t.Fatalf("access-control caido en %s %s: %d, se esperaba 503", rt.method, rt.path, code)
		}
	}
}
