package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const unreachable = "http://127.0.0.1:1"

// userStore solo implementa la lectura por id: es lo unico que recorren estas rutas antes
// de validar el cuerpo.
type userStore struct {
	ports.UserRepository
	users map[uuid.UUID]*domain.User
}

func (s *userStore) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if u, ok := s.users[id]; ok {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

type roleStore struct {
	roles map[uuid.UUID][]string
	err   error
}

func (s *roleStore) RoleNames(_ context.Context, id uuid.UUID) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.roles[id], nil
}

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

// team es una empresa con su administrador y un usuario sin rol del sistema.
type team struct {
	tenant, admin, member uuid.UUID
	users                 *userStore
	roles                 *roleStore
}

func newTeam() *team {
	tm := &team{tenant: uuid.New(), admin: uuid.New(), member: uuid.New()}
	tm.users = &userStore{users: map[uuid.UUID]*domain.User{
		tm.admin:  {ID: tm.admin, TenantID: tm.tenant, Email: "admin@example.test", Status: domain.UserStatusActive},
		tm.member: {ID: tm.member, TenantID: tm.tenant, Email: "member@example.test", Status: domain.UserStatusActive},
	}}
	tm.roles = &roleStore{roles: map[uuid.UUID][]string{tm.admin: {middleware.RoleTenantAdmin}, tm.member: {"operador"}}}
	return tm
}

func (tm *team) server(t *testing.T, accessURL string) http.Handler {
	t.Helper()
	return tm.serverWith(t, accessURL, Config{}, "")
}

func (tm *team) serverWith(t *testing.T, accessURL string, cfg Config, stepUpMode string) http.Handler {
	t.Helper()
	t.Setenv("STEP_UP_MODE", stepUpMode)
	authUC, err := app.NewAuthUseCase(app.AuthDeps{Users: tm.users, Roles: tm.roles, Hasher: testHasher(t), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	userUC := app.NewUserUseCase(app.UserDeps{Users: tm.users, Logger: zap.NewNop()})
	h := NewHandler(authUC, userUC, nil, authz.NewChecker(accessURL, ""), cfg)
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", h.Routes())
	return r
}

func (tm *team) call(t *testing.T, srv http.Handler, actor uuid.UUID, roles, method, path, body string) int {
	t.Helper()
	return tm.callWith(t, srv, actor, roles, method, path, body, nil)
}

func (tm *team) callWith(t *testing.T, srv http.Handler, actor uuid.UUID, roles, method, path, body string, headers map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", actor.String())
	req.Header.Set("X-Tenant-ID", tm.tenant.String())
	req.Header.Set("X-User-Roles", roles)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code
}

func TestEscrituraSinPermisoDevuelve403(t *testing.T) {
	tm := newTeam()
	srv := tm.server(t, policyStub(t, "identity/users/read"))
	actor := uuid.New()

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users/" + tm.member.String() + "/deactivate"},
		{http.MethodDelete, "/api/v1/users/" + tm.member.String()},
		{http.MethodPost, "/api/v1/users/" + tm.member.String() + "/reset-password"},
		{http.MethodPatch, "/api/v1/users/" + tm.member.String()},
		{http.MethodPut, "/api/v1/sessions/policy"},
		{http.MethodDelete, "/api/v1/sessions/" + uuid.NewString()},
	} {
		if code := tm.call(t, srv, actor, "operador", tc.method, tc.path, `{}`); code != http.StatusForbidden {
			t.Errorf("%s %s con solo users/read: %d, se esperaba 403", tc.method, tc.path, code)
		}
	}
}

func TestElPermisoConcretoDejaPasar(t *testing.T) {
	tm := newTeam()
	srv := tm.server(t, policyStub(t, "identity/users/create"))
	// Cuerpo vacio: el 422 de validacion prueba que se llego al handler.
	if code := tm.call(t, srv, uuid.New(), "operador", http.MethodPost, "/api/v1/users", `{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("alta con identity/users/create: %d, se esperaba 422 del handler", code)
	}
}

func TestTenantAdminPasaSinConsultar(t *testing.T) {
	tm := newTeam()
	srv := tm.server(t, unreachable)
	if code := tm.call(t, srv, tm.admin, middleware.RoleTenantAdmin, http.MethodPost, "/api/v1/users", `{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("alta por el tenant_admin: %d, se esperaba 422 del handler", code)
	}
	if code := tm.call(t, srv, tm.admin, middleware.RoleTenantAdmin, http.MethodPost,
		"/api/v1/users/"+tm.member.String()+"/reset-password", `{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("reinicio de contrasena por el tenant_admin: %d, se esperaba 422 del handler", code)
	}
}

func TestLasSesionesDeTodasLasEmpresasSonDelSuperadmin(t *testing.T) {
	tm := newTeam()
	srv := tm.server(t, unreachable)
	if code := tm.call(t, srv, tm.admin, middleware.RoleTenantAdmin, http.MethodGet, "/api/v1/sessions/platform", ""); code != http.StatusForbidden {
		t.Fatalf("tenant_admin en la vista de plataforma: %d, se esperaba 403", code)
	}
}

func TestUnRolDeEmpresaNoAdministraAUnAdministrador(t *testing.T) {
	tm := newTeam()
	srv := tm.server(t, policyStub(t, "identity/users/reset_password", "identity/users/delete", "identity/users/update"))
	actor := uuid.New()

	for _, tc := range []struct{ method, suffix string }{
		{http.MethodPost, "/reset-password"},
		{http.MethodPost, "/deactivate"},
		{http.MethodDelete, ""},
		{http.MethodPatch, ""},
	} {
		path := "/api/v1/users/" + tm.admin.String() + tc.suffix
		if code := tm.call(t, srv, actor, "operador", tc.method, path, `{"first_name":"x"}`); code != http.StatusForbidden {
			t.Errorf("%s %s sobre el tenant_admin: %d, se esperaba 403", tc.method, path, code)
		}
	}
	// Sobre un usuario sin rol del sistema el permiso basta: llega a validar el cuerpo.
	if code := tm.call(t, srv, actor, "operador", http.MethodPost,
		"/api/v1/users/"+tm.member.String()+"/reset-password", `{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("reinicio sobre un usuario comun: %d, se esperaba 422 del handler", code)
	}
}

func TestRolesDelObjetivoIlegiblesDevuelve503(t *testing.T) {
	tm := newTeam()
	tm.roles.err = errors.New("base caida")
	srv := tm.server(t, policyStub(t, "identity/users/reset_password"))
	if code := tm.call(t, srv, uuid.New(), "operador", http.MethodPost,
		"/api/v1/users/"+tm.member.String()+"/reset-password", `{}`); code != http.StatusServiceUnavailable {
		t.Fatalf("roles del objetivo ilegibles: %d, se esperaba 503", code)
	}
}

// En enforce, la accion critica solo pasa con un token de step-up del propio usuario
// firmado por identity; su token de acceso no vale como step-up.
func TestStepUpExigeUnTokenDeStepUpPropio(t *testing.T) {
	signer, err := auth.NewEphemeralSigner()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := auth.IssuerKeySet(signer, "")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewVerifier(keys)
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokenService(signer, verifier, 5*time.Minute, time.Hour)

	tm := newTeam()
	srv := tm.serverWith(t, unreachable, Config{StepUp: verifier}, "enforce")
	path := "/api/v1/users/" + tm.member.String() + "/reset-password"
	call := func(token string) int {
		headers := map[string]string{}
		if token != "" {
			headers["X-Step-Up"] = token
		}
		return tm.callWith(t, srv, tm.admin, middleware.RoleTenantAdmin, http.MethodPost, path, `{}`, headers)
	}

	propio, _ := tokens.GenerateStepUp(tm.admin.String(), tm.tenant.String())
	ajeno, _ := tokens.GenerateStepUp(tm.member.String(), tm.tenant.String())
	acceso, _ := tokens.GeneratePair(tm.admin.String(), tm.tenant.String(), []string{middleware.RoleTenantAdmin})

	if code := call(""); code != http.StatusForbidden {
		t.Fatalf("sin step-up: %d, se esperaba 403", code)
	}
	if code := call(propio); code != http.StatusUnprocessableEntity {
		t.Fatalf("step-up propio: %d, se esperaba 422 del handler", code)
	}
	if code := call(ajeno); code != http.StatusForbidden {
		t.Fatalf("step-up de otro usuario: %d, se esperaba 403", code)
	}
	if code := call(acceso.AccessToken); code != http.StatusForbidden {
		t.Fatalf("token de acceso como step-up: %d, se esperaba 403", code)
	}
}

func TestLaFichaPropiaEsAutoservicio(t *testing.T) {
	tm := newTeam()
	srv := tm.server(t, policyStub(t))
	if code := tm.call(t, srv, tm.member, "operador", http.MethodGet, "/api/v1/users/"+tm.member.String(), ""); code != http.StatusOK {
		t.Fatalf("ficha propia: %d, se esperaba 200", code)
	}
	if code := tm.call(t, srv, tm.member, "operador", http.MethodGet, "/api/v1/users/"+tm.admin.String(), ""); code != http.StatusForbidden {
		t.Fatalf("ficha ajena sin identity/users/read: %d, se esperaba 403", code)
	}
	if code := tm.call(t, srv, tm.member, "operador", http.MethodPatch, "/api/v1/users/"+tm.member.String(), `{"status":"inactive"}`); code != http.StatusForbidden {
		t.Fatalf("cambiar el propio estado: %d, se esperaba 403", code)
	}
}
