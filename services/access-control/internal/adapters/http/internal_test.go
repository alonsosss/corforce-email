package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// trLifecycle hace de repositorio del ciclo de vida de los roles de una empresa.
type trLifecycle struct {
	seeded    map[uuid.UUID]*domain.Role
	nameTaken bool
	users     []uuid.UUID
	calls     int
}

func (f *trLifecycle) SeedSystemRole(_ context.Context, tenantID uuid.UUID, name, _ string) (*domain.SystemRoleSeed, error) {
	f.calls++
	if f.nameTaken {
		return nil, domain.ErrSystemRoleNameTaken
	}
	if r, ok := f.seeded[tenantID]; ok {
		return &domain.SystemRoleSeed{Role: r}, nil
	}
	r := &domain.Role{ID: uuid.New(), TenantID: tenantID, Name: name, IsSystem: true, Status: "active"}
	f.seeded[tenantID] = r
	return &domain.SystemRoleSeed{Role: r, Created: true, Granted: 3}, nil
}

func (f *trLifecycle) ReseedSystemRoles(context.Context, string) (domain.SystemRoleReseed, error) {
	f.calls++
	return domain.SystemRoleReseed{Roles: int64(len(f.seeded)), Granted: 2}, nil
}

func (f *trLifecycle) DeleteTenantRoles(_ context.Context, tenantID uuid.UUID) (*domain.TenantRolesRemoval, error) {
	f.calls++
	removal := &domain.TenantRolesRemoval{}
	if _, ok := f.seeded[tenantID]; ok {
		delete(f.seeded, tenantID)
		removal.Roles, removal.Users = 1, f.users
	}
	return removal, nil
}

type trRoles struct {
	ports.RoleRepository
	lf *trLifecycle
}

func (r trRoles) GetByID(_ context.Context, id uuid.UUID) (*domain.Role, error) {
	for _, role := range r.lf.seeded {
		if role.ID == id {
			return role, nil
		}
	}
	return nil, domain.ErrRoleNotFound
}

type trAssignments struct {
	ports.UserRoleRepository
	assigned map[uuid.UUID]uuid.UUID
	by       []uuid.UUID
	revoked  int
}

func (a *trAssignments) Assign(_ context.Context, userID, roleID, by uuid.UUID) error {
	a.assigned[userID] = roleID
	a.by = append(a.by, by)
	return nil
}

func (a *trAssignments) Revoke(_ context.Context, userID, _ uuid.UUID) error {
	delete(a.assigned, userID)
	a.revoked++
	return nil
}

type trCache struct{ invalidated []uuid.UUID }

func (c *trCache) InvalidateUsers(_ context.Context, ids []uuid.UUID) {
	c.invalidated = append(c.invalidated, ids...)
}

type trTenants map[uuid.UUID]bool

func (s trTenants) IsActive(_ context.Context, id uuid.UUID) (bool, error) { return s[id], nil }

const trToken = "token-interno-de-prueba"

type trFixture struct {
	srv    http.Handler
	lf     *trLifecycle
	asg    *trAssignments
	cache  *trCache
	active trTenants
}

// newTRFixture monta las rutas internas como en main: token interno en el router y usuario
// inyectado por el gateway, que es lo que las rutas deben rechazar.
func newTRFixture(t *testing.T) *trFixture {
	t.Helper()
	t.Setenv("INTERNAL_GATEWAY_TOKEN", trToken)
	f := &trFixture{
		lf:     &trLifecycle{seeded: map[uuid.UUID]*domain.Role{}},
		asg:    &trAssignments{assigned: map[uuid.UUID]uuid.UUID{}},
		cache:  &trCache{},
		active: trTenants{},
	}
	uc := app.NewTenantRolesUseCase(app.TenantRolesDeps{
		Lifecycle: f.lf, Roles: trRoles{lf: f.lf}, UserRoles: f.asg, Cache: f.cache, Tenants: f.active,
		SystemRoles: app.SystemRoles{Superadmin: middleware.RoleSuperadmin, TenantAdmin: middleware.RoleTenantAdmin},
	})
	r := chi.NewRouter()
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Mount("/internal/access-control", NewInternalHandler(uc).Routes())
	f.srv = r
	return f
}

func (f *trFixture) call(method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *trFixture) internal(method, path string) *httptest.ResponseRecorder {
	return f.call(method, path, map[string]string{"X-Gateway-Token": trToken})
}

func trData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible %q: %v", rec.Body.String(), err)
	}
	return env.Data
}

func systemRolePath(tenant uuid.UUID) string {
	return "/internal/access-control/tenants/" + tenant.String() + "/system-role"
}

func TestRutasInternasDeAccessControlSoloParaServicios(t *testing.T) {
	f := newTRFixture(t)
	path := systemRolePath(uuid.New())
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"sin token", nil, http.StatusUnauthorized},
		{"token equivocado", map[string]string{"X-Gateway-Token": "otro"}, http.StatusUnauthorized},
		{"una persona por el gateway", map[string]string{"X-Gateway-Token": trToken, "X-User-ID": uuid.NewString(),
			"X-Tenant-ID": uuid.NewString(), "X-User-Roles": middleware.RoleSuperadmin}, http.StatusForbidden},
	}
	for _, c := range cases {
		if rec := f.call(http.MethodPut, path, c.headers); rec.Code != c.want {
			t.Errorf("%s: %d, se esperaba %d", c.name, rec.Code, c.want)
		}
	}
	if f.lf.calls != 0 {
		t.Fatalf("una peticion rechazada no llega al caso de uso (%d llamadas)", f.lf.calls)
	}
	if rec := f.internal(http.MethodPut, path); rec.Code != http.StatusOK {
		t.Fatalf("servicio con token: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSembrarElRolDelSistemaEsIdempotente(t *testing.T) {
	f := newTRFixture(t)
	tenant := uuid.New()
	first := f.internal(http.MethodPut, systemRolePath(tenant))
	second := f.internal(http.MethodPut, systemRolePath(tenant))
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("siembra: %d y %d", first.Code, second.Code)
	}
	a, b := trData(t, first), trData(t, second)
	if a["name"] != middleware.RoleTenantAdmin || a["created"] != true || a["permissions_granted"] != float64(3) {
		t.Errorf("primera siembra = %v", a)
	}
	if b["created"] != false || b["role_id"] != a["role_id"] {
		t.Errorf("la repeticion devuelve el mismo rol sin crearlo: %v frente a %v", b, a)
	}
}

func TestSembrarSobreUnRolPropioConElMismoNombreEsConflicto(t *testing.T) {
	f := newTRFixture(t)
	f.lf.nameTaken = true
	rec := f.internal(http.MethodPut, systemRolePath(uuid.New()))
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "SYSTEM_ROLE_NAME_TAKEN" {
		t.Fatalf("nombre ocupado: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAsignarYRetirarRolDeLaEmpresa(t *testing.T) {
	f := newTRFixture(t)
	tenant, user := uuid.New(), uuid.New()
	roleID := trData(t, f.internal(http.MethodPut, systemRolePath(tenant)))["role_id"].(string)
	userRole := "/internal/access-control/tenants/" + tenant.String() + "/users/" + user.String() + "/roles/" + roleID

	for i := 0; i < 2; i++ {
		if rec := f.internal(http.MethodPut, userRole); rec.Code != http.StatusOK {
			t.Fatalf("asignar (vez %d): %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if f.asg.assigned[user].String() != roleID || f.asg.by[0] != uuid.Nil {
		t.Fatalf("asignado %v por %v; want el rol, sin actor", f.asg.assigned[user], f.asg.by)
	}

	other := "/internal/access-control/tenants/" + uuid.NewString() + "/users/" + user.String() + "/roles/" + roleID
	if rec := f.internal(http.MethodPut, other); rec.Code != http.StatusNotFound {
		t.Errorf("asignar un rol de otra empresa: %d, se esperaba 404", rec.Code)
	}

	if rec := f.internal(http.MethodDelete, userRole); rec.Code != http.StatusOK || f.asg.revoked != 1 {
		t.Fatalf("retirar: %d (%d retiradas)", rec.Code, f.asg.revoked)
	}
	gone := "/internal/access-control/tenants/" + tenant.String() + "/users/" + user.String() + "/roles/" + uuid.NewString()
	if rec := f.internal(http.MethodDelete, gone); rec.Code != http.StatusOK || f.asg.revoked != 1 {
		t.Errorf("retirar un rol que ya no existe: %d (%d retiradas); se esperaba 200 sin tocar nada", rec.Code, f.asg.revoked)
	}
	if rec := f.internal(http.MethodPut, "/internal/access-control/tenants/no-es-uuid/users/"+user.String()+"/roles/"+roleID); rec.Code != http.StatusBadRequest {
		t.Errorf("empresa no valida: %d, se esperaba 400", rec.Code)
	}
}

func TestRetirarLosRolesDeUnaEmpresa(t *testing.T) {
	f := newTRFixture(t)
	tenant := uuid.New()
	f.internal(http.MethodPut, systemRolePath(tenant))
	f.lf.users = []uuid.UUID{uuid.New(), uuid.New()}
	path := "/internal/access-control/tenants/" + tenant.String() + "/roles"

	f.active[tenant] = true
	if rec := f.internal(http.MethodDelete, path); rec.Code != http.StatusConflict || errorCode(t, rec) != "TENANT_ACTIVE" {
		t.Fatalf("empresa activa: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.lf.seeded) != 1 {
		t.Fatal("los roles de una empresa activa no se tocan")
	}

	f.active[tenant] = false
	rec := f.internal(http.MethodDelete, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("retirar: %d %s", rec.Code, rec.Body.String())
	}
	if d := trData(t, rec); d["roles_removed"] != float64(1) || d["users_affected"] != float64(2) {
		t.Errorf("respuesta = %v", d)
	}
	if len(f.cache.invalidated) != 2 {
		t.Errorf("politicas invalidadas = %d; want las de los 2 usuarios afectados", len(f.cache.invalidated))
	}
	if d := trData(t, f.internal(http.MethodDelete, path)); d["roles_removed"] != float64(0) {
		t.Errorf("repetir no retira nada: %v", d)
	}
}

func TestResembrarLosRolesDelSistema(t *testing.T) {
	f := newTRFixture(t)
	f.internal(http.MethodPut, systemRolePath(uuid.New()))
	rec := f.internal(http.MethodPost, "/internal/access-control/system-role/reseed")
	if rec.Code != http.StatusOK {
		t.Fatalf("resiembra: %d %s", rec.Code, rec.Body.String())
	}
	if d := trData(t, rec); d["roles"] != float64(1) || d["permissions_granted"] != float64(2) {
		t.Errorf("respuesta = %v", d)
	}
}
