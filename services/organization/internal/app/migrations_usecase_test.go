package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

func tenantsFixture() []*domain.Tenant {
	return []*domain.Tenant{
		{ID: uuid.New(), Slug: "uno", DBName: "mail_tenant_uno", Status: domain.TenantStatusActive},
		{ID: uuid.New(), Slug: "dos", DBName: "mail_tenant_dos", Status: domain.TenantStatusActive},
		{ID: uuid.New(), Slug: "tres", DBName: "mail_tenant_tres", Status: domain.TenantStatusActive},
		{ID: uuid.New(), Slug: "inactivo", DBName: "mail_tenant_inactivo", Status: domain.TenantStatusSuspended},
	}
}

func newMigrationsUC(repo *fakeTenantRepo, prov *fakeProvisioner) *OrganizationUseCase {
	return NewOrganizationUseCase(Dependencies{Tenants: repo, Provisioner: prov})
}

// Un tenant bloqueado por otra instancia no es un fallo, y un tenant en error no debe
// abortar el barrido de los demas.
func TestMigrateAllTenantsClasificaResultados(t *testing.T) {
	prov := &fakeProvisioner{runErr: map[string]error{
		"mail_tenant_dos":  domain.ErrMigrationsLocked,
		"mail_tenant_tres": errors.New("boom"),
	}}
	uc := newMigrationsUC(&fakeTenantRepo{tenants: tenantsFixture()}, prov)

	res, err := uc.MigrateAllTenants(context.Background())
	if err != nil {
		t.Fatalf("MigrateAllTenants error inesperado: %v", err)
	}
	if res.Migrated != 1 || res.Locked != 1 || res.Failed != 1 {
		t.Errorf("resultado = %+v; want migrated=1 locked=1 failed=1", res)
	}
	if len(prov.ran) != 3 {
		t.Errorf("se migraron %d bases (%v); el tenant suspendido no debe migrarse", len(prov.ran), prov.ran)
	}
}

func TestTenantMigrationStatusesClasificaEstado(t *testing.T) {
	prov := &fakeProvisioner{status: map[string]domain.TenantMigrationStatus{
		"mail_tenant_uno":  {Applied: 12},
		"mail_tenant_dos":  {Applied: 11, Pending: []string{"mailboxes/07_x.sql"}},
		"mail_tenant_tres": {Applied: 12, Baselined: true},
	}}
	uc := newMigrationsUC(&fakeTenantRepo{tenants: tenantsFixture()}, prov)

	got, err := uc.TenantMigrationStatuses(context.Background())
	if err != nil {
		t.Fatalf("TenantMigrationStatuses error inesperado: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("se reportaron %d tenants; want 3 (el suspendido queda fuera)", len(got))
	}
	want := map[string]string{"uno": "ok", "dos": "pending", "tres": "ok"}
	for _, info := range got {
		if info.Status != want[info.Slug] {
			t.Errorf("tenant %s: status=%q, want %q", info.Slug, info.Status, want[info.Slug])
		}
		if info.Slug == "tres" && !info.Baselined {
			t.Error("el tenant baselineado debe reportarse como tal para verificarlo a mano")
		}
	}
}

func TestMigrateTenantDesconocido(t *testing.T) {
	uc := newMigrationsUC(&fakeTenantRepo{tenants: tenantsFixture()}, &fakeProvisioner{})
	if err := uc.MigrateTenant(context.Background(), uuid.New()); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Errorf("MigrateTenant(desconocido) = %v; want ErrTenantNotFound", err)
	}
}

// El resembrado de roles alcanza a todos los tenants, activos o no: un tenant
// suspendido vuelve a la actividad con su catalogo al dia.
func TestReseedAllRolesAlcanzaTodosLosTenants(t *testing.T) {
	seeder := &fakeRoleSeeder{}
	uc := NewOrganizationUseCase(Dependencies{Tenants: &fakeTenantRepo{tenants: tenantsFixture()}, RoleSeeder: seeder})
	ok, failed := uc.ReseedAllRoles(context.Background())
	if ok != 4 || failed != 0 {
		t.Errorf("ReseedAllRoles = (%d, %d); want (4, 0)", ok, failed)
	}
	if len(seeder.seeded) != 4 {
		t.Errorf("se sembraron %d tenants; want 4", len(seeder.seeded))
	}
}
