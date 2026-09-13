package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	testSuperadmin  = "superadmin"
	testTenantAdmin = "tenant_admin"
)

// fakeUserRoles entrega el acceso minimo que necesita GetUserAccess. Sin account, la
// cuenta esta activa con validFrom como instante de revocacion.
type fakeUserRoles struct {
	roles        []*domain.Role
	modules      []string
	writeActions map[string][]string
	validFrom    time.Time
	account      *domain.UserAccount
	accountErr   error
	rolesRead    bool
}

func (f *fakeUserRoles) Assign(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error { return nil }
func (f *fakeUserRoles) Revoke(context.Context, uuid.UUID, uuid.UUID) error            { return nil }
func (f *fakeUserRoles) ListRoles(context.Context, uuid.UUID, uuid.UUID) ([]*domain.Role, error) {
	f.rolesRead = true
	return f.roles, nil
}
func (f *fakeUserRoles) ListPermissions(context.Context, uuid.UUID, uuid.UUID) ([]*domain.Permission, error) {
	return nil, nil
}
func (f *fakeUserRoles) GetAccessPolicy(context.Context, uuid.UUID, uuid.UUID) (*domain.AccessPolicy, error) {
	return nil, nil
}
func (f *fakeUserRoles) ListAccessibleModules(context.Context, uuid.UUID, uuid.UUID) ([]string, error) {
	return f.modules, nil
}
func (f *fakeUserRoles) ListWriteActionsByModule(context.Context, uuid.UUID, uuid.UUID) (map[string][]string, error) {
	return f.writeActions, nil
}
func (f *fakeUserRoles) UserAccount(context.Context, uuid.UUID, uuid.UUID) (domain.UserAccount, error) {
	if f.accountErr != nil {
		return domain.UserAccount{}, f.accountErr
	}
	if f.account != nil {
		return *f.account, nil
	}
	return domain.UserAccount{Status: "active", TokensValidFrom: f.validFrom}, nil
}
func (f *fakeUserRoles) ListUsersWithPermission(context.Context, uuid.UUID, string, string) ([]uuid.UUID, error) {
	return nil, nil
}

// fakeGate simula el estado de contratacion de modulos del tenant.
type fakeGate struct {
	availability domain.ModuleAvailability
	err          error
}

func (f *fakeGate) EffectiveModules(context.Context, uuid.UUID) (domain.ModuleAvailability, error) {
	return f.availability, f.err
}

func newAccessUC(roles *fakeUserRoles, gate *fakeGate) *RBACUseCase {
	return NewRBACUseCase(nil, nil, nil, roles, nil, gate,
		SystemRoles{Superadmin: testSuperadmin, TenantAdmin: testTenantAdmin}, zap.NewNop())
}

func operatorRoles() *fakeUserRoles {
	return &fakeUserRoles{
		roles:   []*domain.Role{{Name: "operador_campanas"}},
		modules: []string{"campaigns", "identity", "mailboxes"},
		writeActions: map[string][]string{
			"campaigns": {"create", "update"},
			"mailboxes": {"create"},
		},
	}
}

// El catalogo reclama campaigns (apagado) y mailboxes (encendido); identity no lo reclama
// nadie, asi que nunca se filtra.
func restrictedGate() *fakeGate {
	return &fakeGate{availability: domain.ModuleAvailability{
		Restricted: true,
		Gated:      map[string]bool{"campaigns": false, "mailboxes": true},
	}}
}

func TestSoloLosRolesEstructuralesSonAdmin(t *testing.T) {
	cases := []struct {
		role  string
		admin bool
	}{
		{testSuperadmin, true},
		{testTenantAdmin, true},
		{"operador_campanas", false},
	}
	for _, c := range cases {
		roles := &fakeUserRoles{roles: []*domain.Role{{Name: c.role}}}
		access, err := newAccessUC(roles, &fakeGate{}).GetUserAccess(context.Background(), uuid.New(), uuid.New())
		if err != nil {
			t.Fatalf("GetUserAccess(%s): %v", c.role, err)
		}
		if access.IsAdmin != c.admin {
			t.Errorf("IsAdmin(%s) = %v, want %v", c.role, access.IsAdmin, c.admin)
		}
	}
}

func TestLosModulosNoContratadosSeFiltranYSeReportan(t *testing.T) {
	access, err := newAccessUC(operatorRoles(), restrictedGate()).GetUserAccess(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GetUserAccess: %v", err)
	}
	if got := access.Modules; len(got) != 2 || got[0] != "identity" || got[1] != "mailboxes" {
		t.Errorf("Modules = %v; campaigns debe quedar fuera e identity conservarse", got)
	}
	if _, ok := access.WriteActions["campaigns"]; ok {
		t.Errorf("WriteActions conserva campaigns: %v", access.WriteActions)
	}
	if got := access.WriteModules; len(got) != 1 || got[0] != "mailboxes" {
		t.Errorf("WriteModules = %v, want [mailboxes]", got)
	}
	if got := access.DisabledModules; len(got) != 1 || got[0] != "campaigns" {
		t.Errorf("DisabledModules = %v, want [campaigns]", got)
	}
}

// El administrador de la empresa queda sujeto al catalogo igual que cualquier usuario:
// contratar un modulo es una decision comercial, no un permiso que el pueda concederse.
func TestElAdministradorDeLaEmpresaTambienSeFiltra(t *testing.T) {
	roles := operatorRoles()
	roles.roles = []*domain.Role{{Name: testTenantAdmin}}
	access, err := newAccessUC(roles, restrictedGate()).GetUserAccess(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GetUserAccess: %v", err)
	}
	if !access.IsAdmin {
		t.Fatal("IsAdmin = false para el administrador de la empresa")
	}
	if len(access.DisabledModules) != 1 {
		t.Errorf("DisabledModules = %v; el administrador tambien debe verlos apagados", access.DisabledModules)
	}
}

func TestElSuperadminTransciendeElCatalogo(t *testing.T) {
	roles := operatorRoles()
	roles.roles = []*domain.Role{{Name: testSuperadmin}}
	access, err := newAccessUC(roles, restrictedGate()).GetUserAccess(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GetUserAccess: %v", err)
	}
	if len(access.Modules) != 3 || len(access.DisabledModules) != 0 {
		t.Errorf("Modules = %v, DisabledModules = %v; el superadmin no se filtra", access.Modules, access.DisabledModules)
	}
}

// Una empresa sin estado explicito conserva todos sus modulos: es la garantia de que
// activar el catalogo no cambia a quien ya opera.
func TestSinEstadoExplicitoNoSeFiltraNada(t *testing.T) {
	access, err := newAccessUC(operatorRoles(), &fakeGate{}).GetUserAccess(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GetUserAccess: %v", err)
	}
	if len(access.Modules) != 3 || len(access.WriteModules) != 2 || len(access.DisabledModules) != 0 {
		t.Errorf("acceso alterado sin catalogo: modules=%v write=%v disabled=%v",
			access.Modules, access.WriteModules, access.DisabledModules)
	}
}

// Un fallo leyendo el catalogo no puede dejar sin pantallas a la empresa: fail-open.
func TestFalloLeyendoElCatalogoNoFiltra(t *testing.T) {
	gate := &fakeGate{err: errors.New("registro no disponible")}
	access, err := newAccessUC(operatorRoles(), gate).GetUserAccess(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GetUserAccess no debe fallar por el catalogo: %v", err)
	}
	if len(access.Modules) != 3 || len(access.DisabledModules) != 0 {
		t.Errorf("Modules = %v, DisabledModules = %v; un fallo no puede apagar modulos", access.Modules, access.DisabledModules)
	}
}

// Sin tenant en el contexto no hay catalogo que consultar.
func TestSinTenantNoSeConsultaElCatalogo(t *testing.T) {
	access, err := newAccessUC(operatorRoles(), restrictedGate()).GetUserAccess(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatalf("GetUserAccess: %v", err)
	}
	if len(access.Modules) != 3 {
		t.Errorf("Modules = %v; sin tenant no se filtra", access.Modules)
	}
}

func TestElInstanteDeRevocacionViajaConElAcceso(t *testing.T) {
	roles := operatorRoles()
	roles.validFrom = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	access, err := newAccessUC(roles, &fakeGate{}).GetUserAccess(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GetUserAccess: %v", err)
	}
	if !access.TokensValidFrom.Equal(roles.validFrom) {
		t.Errorf("TokensValidFrom = %v, want %v", access.TokensValidFrom, roles.validFrom)
	}
}

// Una cuenta que no existe en la empresa es una respuesta definitiva: no se resuelven sus
// roles, que pueden seguir asignados tras borrar la cuenta.
func TestUnaCuentaInexistenteNoTieneAcceso(t *testing.T) {
	roles := operatorRoles()
	roles.accountErr = domain.ErrUserNotFound
	access, err := newAccessUC(roles, &fakeGate{}).GetUserAccess(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, domain.ErrUserNotFound) || access != nil {
		t.Fatalf("GetUserAccess = %v, %v; se esperaba ErrUserNotFound", access, err)
	}
	if roles.rolesRead {
		t.Error("se leyeron los roles de una cuenta inexistente")
	}
}

// Solo una cuenta activa conserva el acceso, el mismo criterio con el que identity renueva.
func TestUnaCuentaNoActivaNoTieneAcceso(t *testing.T) {
	for _, status := range []string{"inactive", "locked", "pending", ""} {
		roles := operatorRoles()
		roles.account = &domain.UserAccount{Status: status}
		_, err := newAccessUC(roles, &fakeGate{}).GetUserAccess(context.Background(), uuid.New(), uuid.New())
		if !errors.Is(err, domain.ErrUserNotActive) {
			t.Errorf("estado %q: err = %v, se esperaba ErrUserNotActive", status, err)
		}
	}
}

// Un fallo leyendo la cuenta no es definitivo: el acceso se resuelve sin instante de
// revocacion, como antes, para que una caida de la base no expulse a toda la empresa.
func TestUnFalloLeyendoLaCuentaNoCierraLaSesion(t *testing.T) {
	roles := operatorRoles()
	roles.accountErr = errors.New("registro no disponible")
	access, err := newAccessUC(roles, &fakeGate{}).GetUserAccess(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("GetUserAccess: %v", err)
	}
	if !access.TokensValidFrom.IsZero() || len(access.Modules) != 3 {
		t.Errorf("acceso = %+v; se esperaba el de siempre sin instante de revocacion", access)
	}
}

func TestHasPermissionAdmiteComodines(t *testing.T) {
	policy := &domain.AccessPolicy{Permissions: []domain.Permission{
		{Module: "campaigns", Resource: "*", Action: "*"},
		{Module: "mailboxes", Resource: "aliases", Action: "*"},
		{Module: "domains", Resource: "dns", Action: "read"},
		{Module: "contacts", Resource: "*", Action: "read"},
	}}
	cases := []struct {
		module, resource, action string
		want                     bool
	}{
		{"campaigns", "sends", "delete", true},
		{"mailboxes", "aliases", "create", true},
		{"mailboxes", "quotas", "create", false},
		{"domains", "dns", "read", true},
		{"domains", "dns", "update", false},
		{"templates", "*", "read", false},
		{"contacts", "lists", "read", true},
		{"contacts", "lists", "delete", false},
		// Un comodin estrecho no cubre uno mas amplio: quien tiene contacts/*/read no
		// puede conceder contacts/*/*.
		{"contacts", "*", "*", false},
		{"mailboxes", "*", "*", false},
	}
	for _, c := range cases {
		if got := policy.HasPermission(c.module, c.resource, c.action); got != c.want {
			t.Errorf("HasPermission(%s,%s,%s) = %v, want %v", c.module, c.resource, c.action, got, c.want)
		}
	}
}
