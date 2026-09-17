package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// policyRepo sirve la politica desde memoria. Solo implementa lo que usan la comprobacion
// de permisos y la lectura de la politica; el resto del puerto no se llama en estas rutas.
type policyRepo struct {
	ports.UserRoleRepository
	perms []domain.Permission
	err   error
	calls int
}

func (p *policyRepo) GetAccessPolicy(_ context.Context, userID, tenantID uuid.UUID) (*domain.AccessPolicy, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &domain.AccessPolicy{UserID: userID, TenantID: tenantID, Permissions: p.perms}, nil
}

func newServer(repo *policyRepo) http.Handler {
	uc := app.NewRBACUseCase(nil, nil, nil, repo, nil, nil,
		app.SystemRoles{Superadmin: middleware.RoleSuperadmin, TenantAdmin: middleware.RoleTenantAdmin}, nil, zap.NewNop())
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(uc).Routes())
	return r
}

type caller struct {
	user, tenant uuid.UUID
	roles        string
}

func (c caller) do(t *testing.T, srv http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.user != uuid.Nil {
		req.Header.Set("X-User-ID", c.user.String())
	}
	if c.tenant != uuid.Nil {
		req.Header.Set("X-Tenant-ID", c.tenant.String())
	}
	if c.roles != "" {
		req.Header.Set("X-User-Roles", c.roles)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible %q: %v", rec.Body.String(), err)
	}
	return env.Error.Code
}

func perm(resource, action string) domain.Permission {
	return domain.Permission{Module: permModule, Resource: resource, Action: action}
}

func TestEscrituraSinElPermisoDeAccionDevuelve403(t *testing.T) {
	srv := newServer(&policyRepo{perms: []domain.Permission{perm("roles", "read")}})
	actor := caller{user: uuid.New(), tenant: uuid.New(), roles: "operador"}

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/roles"},
		{http.MethodPut, "/api/v1/roles/" + uuid.NewString()},
		{http.MethodDelete, "/api/v1/roles/" + uuid.NewString()},
		{http.MethodPut, "/api/v1/roles/" + uuid.NewString() + "/permissions"},
		{http.MethodPost, "/api/v1/user-roles/assign"},
		{http.MethodPost, "/api/v1/user-roles/revoke"},
	} {
		if rec := actor.do(t, srv, tc.method, tc.path, `{}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s con solo roles/read: %d, se esperaba 403", tc.method, tc.path, rec.Code)
		}
	}
}

func TestElPermisoConcretoDejaPasar(t *testing.T) {
	srv := newServer(&policyRepo{perms: []domain.Permission{perm("roles", "create")}})
	actor := caller{user: uuid.New(), tenant: uuid.New(), roles: "operador"}

	// Cuerpo invalido: si responde 400 es que el handler llego a leerlo.
	if rec := actor.do(t, srv, http.MethodPost, "/api/v1/roles", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("con access/roles/create: %d, se esperaba 400 del handler", rec.Code)
	}
}

func TestTenantAdminPasaSinConsultarLaPolitica(t *testing.T) {
	repo := &policyRepo{err: errors.New("no deberia consultarse")}
	srv := newServer(repo)
	admin := caller{user: uuid.New(), tenant: uuid.New(), roles: middleware.RoleTenantAdmin}

	if rec := admin.do(t, srv, http.MethodPost, "/api/v1/roles", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("tenant_admin: %d, se esperaba 400 del handler", rec.Code)
	}
	if repo.calls != 0 {
		t.Fatalf("la politica se consulto %d veces para un rol del sistema", repo.calls)
	}
}

func TestPoliticaIlegibleDevuelve503(t *testing.T) {
	srv := newServer(&policyRepo{err: errors.New("base caida")})
	actor := caller{user: uuid.New(), tenant: uuid.New(), roles: "operador"}

	rec := actor.do(t, srv, http.MethodPost, "/api/v1/roles", `{"name":"x"}`)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "AUTHZ_UNAVAILABLE" {
		t.Fatalf("politica ilegible: %d %s, se esperaba 503 AUTHZ_UNAVAILABLE", rec.Code, rec.Body.String())
	}
}

func TestSinIdentidadNoHayPermiso(t *testing.T) {
	srv := newServer(&policyRepo{perms: []domain.Permission{perm("roles", "read")}})
	if rec := (caller{user: uuid.New(), roles: "operador"}).do(t, srv, http.MethodGet, "/api/v1/roles", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("sin empresa en la peticion: %d, se esperaba 403", rec.Code)
	}
}

func TestLaPoliticaPropiaEsAutoservicio(t *testing.T) {
	srv := newServer(&policyRepo{})
	actor := caller{user: uuid.New(), tenant: uuid.New(), roles: "operador"}

	if rec := actor.do(t, srv, http.MethodGet, "/api/v1/policy/"+actor.user.String(), ""); rec.Code != http.StatusOK {
		t.Fatalf("politica propia: %d, se esperaba 200", rec.Code)
	}
	if rec := actor.do(t, srv, http.MethodGet, "/api/v1/policy/"+uuid.NewString(), ""); rec.Code != http.StatusForbidden {
		t.Fatalf("politica ajena sin access/user_roles/read: %d, se esperaba 403", rec.Code)
	}
	if rec := actor.do(t, srv, http.MethodPost, "/api/v1/check-access",
		`{"user_id":"`+uuid.NewString()+`","module":"access","resource":"roles","action":"read"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("check-access ajeno sin access/user_roles/read: %d, se esperaba 403", rec.Code)
	}
}

func TestElRegistroDeDenegacionesEsSoloInterno(t *testing.T) {
	srv := newServer(&policyRepo{})

	persona := caller{user: uuid.New(), tenant: uuid.New(), roles: middleware.RoleTenantAdmin}
	if rec := persona.do(t, srv, http.MethodPost, "/api/v1/access/denials", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("una persona registrando denegaciones: %d, se esperaba 403", rec.Code)
	}
	// Sin usuario es el gateway: pasa al handler, que rechaza el cuerpo.
	if rec := (caller{}).do(t, srv, http.MethodPost, "/api/v1/access/denials", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("llamada interna: %d, se esperaba 400 del handler", rec.Code)
	}
}
