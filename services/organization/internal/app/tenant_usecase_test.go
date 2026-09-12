package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

const testAdminPassword = "una-contrasena-larga-1"

func cellsFixture() []*domain.Cell {
	return []*domain.Cell{
		{ID: uuid.New(), Code: "pe-01", Region: "sa-east-1", Status: domain.CellStatusActive},
		{ID: uuid.New(), Code: "pe-00", Region: "sa-east-1", Status: domain.CellStatusDraining},
	}
}

type tenantHarness struct {
	uc      *OrganizationUseCase
	tenants *fakeTenantRepo
	cells   *fakeCellRepo
	prov    *fakeProvisioner
	roles   *fakeRoleSeeder
	admin   *fakeAdminSeeder
	pub     *fakePublisher
}

func newTenantHarness(defaultCell string) *tenantHarness {
	h := &tenantHarness{
		tenants: &fakeTenantRepo{},
		cells:   &fakeCellRepo{cells: cellsFixture()},
		prov:    &fakeProvisioner{},
		roles:   &fakeRoleSeeder{},
		admin:   &fakeAdminSeeder{},
		pub:     &fakePublisher{},
	}
	h.uc = NewOrganizationUseCase(Dependencies{
		Tenants: h.tenants, Cells: h.cells, Provisioner: h.prov,
		RoleSeeder: h.roles, AdminSeeder: h.admin, Publisher: h.pub,
		DefaultCellCode: defaultCell,
	})
	return h
}

func baseRequest() CreateTenantRequest {
	return CreateTenantRequest{
		Slug: "Acme-Corp", Name: "Acme",
		AdminEmail: "admin@acme.test", AdminPassword: testAdminPassword,
		Settings: map[string]string{"tz": "America/Lima", "no_admitido": "x"},
	}
}

func TestCreateTenantUsaCeldaPorDefecto(t *testing.T) {
	h := newTenantHarness("pe-01")
	tenant, err := h.uc.CreateTenant(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if tenant.CellID != h.cells.cells[0].ID {
		t.Errorf("cell_id = %s; want la celda por defecto pe-01", tenant.CellID)
	}
	if tenant.Slug != "acme-corp" || tenant.DBName != "mail_tenant_acme_corp" {
		t.Errorf("slug/db = %s/%s; want acme-corp/mail_tenant_acme_corp", tenant.Slug, tenant.DBName)
	}
	if tenant.Status != domain.TenantStatusActive {
		t.Errorf("status = %s; want active", tenant.Status)
	}
	if _, ok := tenant.Settings["no_admitido"]; ok {
		t.Error("un ajuste fuera del conjunto admitido no debe guardarse")
	}
	if tenant.Settings["tz"] != "America/Lima" {
		t.Errorf("settings.tz = %v; want America/Lima", tenant.Settings["tz"])
	}
	if len(h.prov.created) != 1 || len(h.prov.ran) != 1 || len(h.prov.dropped) != 0 {
		t.Errorf("aprovisionamiento = created=%v ran=%v dropped=%v", h.prov.created, h.prov.ran, h.prov.dropped)
	}
	if len(h.roles.seeded) != 1 || h.admin.tenantID != tenant.ID || h.admin.password != testAdminPassword {
		t.Error("el alta debe sembrar roles y crear el primer administrador con la contrasena recibida")
	}
	if len(h.pub.created) != 1 {
		t.Errorf("eventos tenant.created = %d; want 1", len(h.pub.created))
	}
}

func TestCreateTenantConCeldaExplicita(t *testing.T) {
	h := newTenantHarness("")
	req := baseRequest()
	req.CellCode = "PE-01"
	tenant, err := h.uc.CreateTenant(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if tenant.CellID != h.cells.cells[0].ID {
		t.Errorf("cell_id = %s; want pe-01", tenant.CellID)
	}
}

func TestCreateTenantRechazosDeCelda(t *testing.T) {
	cases := []struct {
		name, defaultCell, reqCell string
		want                       error
	}{
		{"sin celda ni defecto", "", "", domain.ErrCellRequired},
		{"celda desconocida", "", "xx-99", domain.ErrCellNotFound},
		{"celda en drenado", "", "pe-00", domain.ErrCellNotAssignable},
		{"defecto en drenado", "pe-00", "", domain.ErrCellNotAssignable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTenantHarness(c.defaultCell)
			req := baseRequest()
			req.CellCode = c.reqCell
			_, err := h.uc.CreateTenant(context.Background(), req)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v; want %v", err, c.want)
			}
			if len(h.prov.created) != 0 {
				t.Error("sin celda resuelta no debe crearse ninguna base")
			}
		})
	}
}

func TestCreateTenantExigePrimerAdministrador(t *testing.T) {
	h := newTenantHarness("pe-01")
	req := baseRequest()
	req.AdminPassword = ""
	if _, err := h.uc.CreateTenant(context.Background(), req); !errors.Is(err, domain.ErrAdminUserRequired) {
		t.Errorf("sin contrasena = %v; want ErrAdminUserRequired", err)
	}
	req.AdminPassword = "corta"
	if _, err := h.uc.CreateTenant(context.Background(), req); !errors.Is(err, domain.ErrAdminPasswordShort) {
		t.Errorf("contrasena corta = %v; want ErrAdminPasswordShort", err)
	}
}

func TestCreateTenantDuplicado(t *testing.T) {
	h := newTenantHarness("pe-01")
	if _, err := h.uc.CreateTenant(context.Background(), baseRequest()); err != nil {
		t.Fatalf("primer alta: %v", err)
	}
	if _, err := h.uc.CreateTenant(context.Background(), baseRequest()); !errors.Is(err, domain.ErrTenantAlreadyExists) {
		t.Errorf("segundo alta = %v; want ErrTenantAlreadyExists", err)
	}
}

// Si las migraciones de la base nueva fallan, la base se borra y no queda registro.
func TestCreateTenantDeshaceBaseSiFallanMigraciones(t *testing.T) {
	h := newTenantHarness("pe-01")
	h.prov.runErr = map[string]error{"mail_tenant_acme_corp": errors.New("boom")}
	if _, err := h.uc.CreateTenant(context.Background(), baseRequest()); err == nil {
		t.Fatal("se esperaba error de migracion")
	}
	if len(h.prov.dropped) != 1 || len(h.tenants.tenants) != 0 {
		t.Errorf("dropped=%v tenants=%d; la base debe borrarse y el registro quedar vacio", h.prov.dropped, len(h.tenants.tenants))
	}
}

func TestSetTenantStatusPublicaSoloCambios(t *testing.T) {
	h := newTenantHarness("pe-01")
	tenant, err := h.uc.CreateTenant(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	ctx := context.Background()
	if _, err := h.uc.SetTenantStatus(ctx, tenant.ID, domain.TenantStatusActive); err != nil {
		t.Fatalf("estado repetido: %v", err)
	}
	if _, err := h.uc.SetTenantStatus(ctx, tenant.ID, domain.TenantStatusSuspended); err != nil {
		t.Fatalf("suspender: %v", err)
	}
	if _, err := h.uc.SetTenantStatus(ctx, tenant.ID, "borrado"); !errors.Is(err, domain.ErrInvalidTenantStatus) {
		t.Errorf("estado invalido = %v; want ErrInvalidTenantStatus", err)
	}
	if len(h.pub.statuses) != 1 || h.pub.statuses[0] != "active->suspended" {
		t.Errorf("eventos de estado = %v; want [active->suspended]", h.pub.statuses)
	}
}

func TestDeleteTenantExigeQueNoEsteActivo(t *testing.T) {
	h := newTenantHarness("pe-01")
	tenant, err := h.uc.CreateTenant(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	ctx := context.Background()
	if err := h.uc.DeleteTenant(ctx, tenant.ID); !errors.Is(err, domain.ErrTenantStillActive) {
		t.Errorf("borrar activo = %v; want ErrTenantStillActive", err)
	}
	if _, err := h.uc.SetTenantStatus(ctx, tenant.ID, domain.TenantStatusInactive); err != nil {
		t.Fatalf("dar de baja: %v", err)
	}
	if err := h.uc.DeleteTenant(ctx, tenant.ID); err != nil {
		t.Errorf("borrar inactivo: %v", err)
	}
	if len(h.prov.dropped) != 0 {
		t.Error("borrar del registro no debe borrar la base fisica")
	}
}

func TestCells(t *testing.T) {
	h := newTenantHarness("")
	ctx := context.Background()
	cell, err := h.uc.CreateCell(ctx, CreateCellRequest{Code: "EU-01", Region: "eu-west-1", DBHost: "pg-eu-01", DBPort: 5432})
	if err != nil {
		t.Fatalf("CreateCell: %v", err)
	}
	if cell.Code != "eu-01" || cell.Status != domain.CellStatusActive {
		t.Errorf("celda = %+v; want codigo en minusculas y estado active", cell)
	}
	if _, err := h.uc.CreateCell(ctx, CreateCellRequest{Code: "eu-01"}); !errors.Is(err, domain.ErrCellCodeExists) {
		t.Errorf("codigo repetido = %v; want ErrCellCodeExists", err)
	}
	if _, err := h.uc.CreateCell(ctx, CreateCellRequest{Code: "EU 01"}); !errors.Is(err, domain.ErrInvalidCellCode) {
		t.Errorf("codigo invalido = %v; want ErrInvalidCellCode", err)
	}
	status := domain.CellStatusDraining
	updated, err := h.uc.UpdateCell(ctx, cell.ID, UpdateCellRequest{Status: &status})
	if err != nil || updated.Status != domain.CellStatusDraining {
		t.Errorf("UpdateCell = (%+v, %v); want draining", updated, err)
	}
	bad := "parada"
	if _, err := h.uc.UpdateCell(ctx, cell.ID, UpdateCellRequest{Status: &bad}); !errors.Is(err, domain.ErrInvalidCellStatus) {
		t.Errorf("estado invalido = %v; want ErrInvalidCellStatus", err)
	}
	if _, err := h.uc.UpdateCell(ctx, cell.ID, UpdateCellRequest{}); !errors.Is(err, domain.ErrNothingToUpdate) {
		t.Errorf("PATCH vacio = %v; want ErrNothingToUpdate", err)
	}
	if _, err := h.uc.UpdateCell(ctx, uuid.New(), UpdateCellRequest{Status: &status}); !errors.Is(err, domain.ErrCellNotFound) {
		t.Errorf("celda desconocida = %v; want ErrCellNotFound", err)
	}
}
