package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeRoles struct{ byID map[uuid.UUID]*domain.Role }

func (f *fakeRoles) Create(context.Context, *domain.Role) error { return nil }
func (f *fakeRoles) GetByID(_ context.Context, id uuid.UUID) (*domain.Role, error) {
	if r, ok := f.byID[id]; ok {
		return r, nil
	}
	return nil, domain.ErrRoleNotFound
}
func (f *fakeRoles) GetByName(context.Context, uuid.UUID, string) (*domain.Role, error) {
	return nil, domain.ErrRoleNotFound
}
func (f *fakeRoles) List(context.Context, uuid.UUID) ([]*domain.Role, error) { return nil, nil }
func (f *fakeRoles) Update(context.Context, *domain.Role) error              { return nil }
func (f *fakeRoles) Delete(context.Context, uuid.UUID) error                 { return nil }

type fakePerms struct{ all []*domain.Permission }

func (f *fakePerms) List(context.Context) ([]*domain.Permission, error) { return f.all, nil }
func (f *fakePerms) ListByModule(_ context.Context, module string) ([]*domain.Permission, error) {
	out := []*domain.Permission{}
	for _, p := range f.all {
		if p.Module == module {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakePerms) GetByIDs(_ context.Context, ids []uuid.UUID) ([]*domain.Permission, error) {
	out := []*domain.Permission{}
	for _, id := range ids {
		for _, p := range f.all {
			if p.ID == id {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

type fakeRolePerms struct {
	replaced [][]uuid.UUID
	perms    []*domain.Permission
}

func (f *fakeRolePerms) ListPermissions(context.Context, uuid.UUID) ([]*domain.Permission, error) {
	return f.perms, nil
}
func (f *fakeRolePerms) ReplaceAll(_ context.Context, _ uuid.UUID, ids []uuid.UUID) error {
	f.replaced = append(f.replaced, ids)
	return nil
}

var admin = Actor{UserID: uuid.New(), Privileged: true}

// fakeGrants es la politica de quien concede y lo que asigna o retira.
type fakeGrants struct {
	fakeUserRoles
	held     []domain.Permission
	assigned int
	revoked  int
}

func (f *fakeGrants) GetAccessPolicy(context.Context, uuid.UUID, uuid.UUID) (*domain.AccessPolicy, error) {
	return &domain.AccessPolicy{Permissions: f.held}, nil
}
func (f *fakeGrants) Assign(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	f.assigned++
	return nil
}
func (f *fakeGrants) Revoke(context.Context, uuid.UUID, uuid.UUID) error {
	f.revoked++
	return nil
}

type rolePermsFixture struct {
	uc        *RBACUseCase
	rolePerms *fakeRolePerms
	tenantID  uuid.UUID
	roleID    uuid.UUID
	tenantP   *domain.Permission
	platformP *domain.Permission
	grants    *fakeGrants
	roles     *fakeRoles
}

func newRolePermsFixture() rolePermsFixture {
	tenantID, roleID := uuid.New(), uuid.New()
	tenantP := &domain.Permission{ID: uuid.New(), Module: "billing", Resource: "usage", Action: "read", Scope: domain.PermissionScopeTenant}
	platformP := &domain.Permission{ID: uuid.New(), Module: "billing", Resource: "plans", Action: "create", Scope: domain.PermissionScopePlatform}
	roles := &fakeRoles{byID: map[uuid.UUID]*domain.Role{roleID: {ID: roleID, TenantID: tenantID, Name: "finanzas"}}}
	rolePerms := &fakeRolePerms{}
	grants := &fakeGrants{}
	uc := NewRBACUseCase(roles, &fakePerms{all: []*domain.Permission{tenantP, platformP}}, rolePerms, grants, nil, nil,
		SystemRoles{Superadmin: testSuperadmin, TenantAdmin: testTenantAdmin}, zap.NewNop())
	return rolePermsFixture{uc: uc, rolePerms: rolePerms, tenantID: tenantID, roleID: roleID, tenantP: tenantP, platformP: platformP, grants: grants, roles: roles}
}

func TestUnRolDeEmpresaNoRecibePermisosDePlataforma(t *testing.T) {
	f := newRolePermsFixture()
	err := f.uc.SetRolePermissions(context.Background(), admin, f.tenantID, f.roleID, []uuid.UUID{f.tenantP.ID, f.platformP.ID})
	if !errors.Is(err, domain.ErrPlatformPermission) {
		t.Fatalf("err = %v, want ErrPlatformPermission", err)
	}
	if len(f.rolePerms.replaced) != 0 {
		t.Fatal("se escribieron permisos pese al rechazo")
	}
}

func TestUnPermisoInexistenteSeRechazaSinEscribir(t *testing.T) {
	f := newRolePermsFixture()
	err := f.uc.SetRolePermissions(context.Background(), admin, f.tenantID, f.roleID, []uuid.UUID{f.tenantP.ID, uuid.New()})
	if !errors.Is(err, domain.ErrPermissionNotFound) {
		t.Fatalf("err = %v, want ErrPermissionNotFound", err)
	}
	if len(f.rolePerms.replaced) != 0 {
		t.Fatal("se escribieron permisos pese al rechazo")
	}
}

func TestLosPermisosRepetidosSeAsignanUnaVez(t *testing.T) {
	f := newRolePermsFixture()
	if err := f.uc.SetRolePermissions(context.Background(), admin, f.tenantID, f.roleID, []uuid.UUID{f.tenantP.ID, f.tenantP.ID}); err != nil {
		t.Fatalf("SetRolePermissions: %v", err)
	}
	if got := f.rolePerms.replaced; len(got) != 1 || len(got[0]) != 1 || got[0][0] != f.tenantP.ID {
		t.Fatalf("replaced = %v, want un solo permiso", got)
	}
}

func TestVaciarLosPermisosDeUnRolNoConsultaElCatalogo(t *testing.T) {
	f := newRolePermsFixture()
	if err := f.uc.SetRolePermissions(context.Background(), admin, f.tenantID, f.roleID, nil); err != nil {
		t.Fatalf("SetRolePermissions: %v", err)
	}
	if len(f.rolePerms.replaced) != 1 || len(f.rolePerms.replaced[0]) != 0 {
		t.Fatalf("replaced = %v, want una sustitucion vacia", f.rolePerms.replaced)
	}
}

func TestElCatalogoOcultaLosPermisosDePlataformaAQuienNoOperaLaPlataforma(t *testing.T) {
	f := newRolePermsFixture()
	tenantView, err := f.uc.ListPermissions(context.Background(), false)
	if err != nil {
		t.Fatalf("ListPermissions: %v", err)
	}
	if len(tenantView) != 1 || tenantView[0].ID != f.tenantP.ID {
		t.Fatalf("vista de empresa = %v, want solo el permiso de empresa", tenantView)
	}
	platformView, err := f.uc.ListPermissionsByModule(context.Background(), "billing", true)
	if err != nil {
		t.Fatalf("ListPermissionsByModule: %v", err)
	}
	if len(platformView) != 2 {
		t.Fatalf("vista de plataforma = %d permisos, want 2", len(platformView))
	}
}

func operator() Actor { return Actor{UserID: uuid.New()} }

func TestNadieConcedeUnPermisoQueNoTiene(t *testing.T) {
	f := newRolePermsFixture()
	err := f.uc.SetRolePermissions(context.Background(), operator(), f.tenantID, f.roleID, []uuid.UUID{f.tenantP.ID})
	if !errors.Is(err, domain.ErrPermissionNotHeld) {
		t.Fatalf("err = %v, want ErrPermissionNotHeld", err)
	}
	if len(f.rolePerms.replaced) != 0 {
		t.Fatal("se escribieron permisos pese al rechazo")
	}
}

func TestSeConcedeLoQueSeTieneAunqueSeaPorComodin(t *testing.T) {
	f := newRolePermsFixture()
	f.grants.held = []domain.Permission{{Module: "billing", Resource: "*", Action: "*"}}
	if err := f.uc.SetRolePermissions(context.Background(), operator(), f.tenantID, f.roleID, []uuid.UUID{f.tenantP.ID}); err != nil {
		t.Fatalf("SetRolePermissions: %v", err)
	}
}

func TestSoloUnRolDelSistemaMueveUnRolDelSistema(t *testing.T) {
	f := newRolePermsFixture()
	sysID := uuid.New()
	f.roles.byID[sysID] = &domain.Role{ID: sysID, TenantID: f.tenantID, Name: testTenantAdmin, IsSystem: true}
	if err := f.uc.AssignRoleToUser(context.Background(), operator(), f.tenantID, uuid.New(), sysID); !errors.Is(err, domain.ErrSystemRoleAssignment) {
		t.Fatalf("asignar: err = %v, want ErrSystemRoleAssignment", err)
	}
	if err := f.uc.RevokeRoleFromUser(context.Background(), operator(), f.tenantID, uuid.New(), sysID); !errors.Is(err, domain.ErrSystemRoleAssignment) {
		t.Fatalf("retirar: err = %v, want ErrSystemRoleAssignment", err)
	}
	if err := f.uc.AssignRoleToUser(context.Background(), admin, f.tenantID, uuid.New(), sysID); err != nil {
		t.Fatalf("el administrador asigna: %v", err)
	}
	if f.grants.assigned != 1 || f.grants.revoked != 0 {
		t.Fatalf("assigned=%d revoked=%d, want 1 y 0", f.grants.assigned, f.grants.revoked)
	}
}

func TestNoSeAsignaUnRolConPermisosQueNoSeTienen(t *testing.T) {
	f := newRolePermsFixture()
	f.rolePerms.perms = []*domain.Permission{f.tenantP}
	if err := f.uc.AssignRoleToUser(context.Background(), operator(), f.tenantID, uuid.New(), f.roleID); !errors.Is(err, domain.ErrPermissionNotHeld) {
		t.Fatalf("err = %v, want ErrPermissionNotHeld", err)
	}
	f.grants.held = []domain.Permission{{Module: "billing", Resource: "usage", Action: "read"}}
	if err := f.uc.AssignRoleToUser(context.Background(), operator(), f.tenantID, uuid.New(), f.roleID); err != nil {
		t.Fatalf("con el permiso en su politica: %v", err)
	}
	if f.grants.assigned != 1 {
		t.Fatalf("assigned = %d, want 1", f.grants.assigned)
	}
}
