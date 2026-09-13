package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

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
	sagas   *fakeSagaRepo
	prov    *fakeProvisioner
	access  *fakeAccess
	ident   *fakeIdentity
	pub     *fakePublisher
	log     *callLog
}

func newTenantHarness(defaultCell string) *tenantHarness {
	log := &callLog{}
	tenants := &fakeTenantRepo{}
	h := &tenantHarness{
		tenants: tenants,
		cells:   &fakeCellRepo{cells: cellsFixture()},
		sagas:   newFakeSagaRepo(tenants),
		prov:    &fakeProvisioner{log: log},
		access:  newFakeAccess(log),
		ident:   newFakeIdentity(log),
		pub:     &fakePublisher{},
		log:     log,
	}
	h.uc = NewOrganizationUseCase(Dependencies{
		Tenants: h.tenants, Cells: h.cells, Sagas: h.sagas, Provisioner: h.prov,
		Access: h.access, Identity: h.ident, Publisher: h.pub,
		DefaultCellCode: defaultCell, SagaLease: time.Minute,
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

// sagaOf devuelve la saga persistida de la empresa.
func (h *tenantHarness) sagaOf(t *testing.T, id uuid.UUID) *domain.TenantSaga {
	t.Helper()
	s, err := h.sagas.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("saga de %s: %v", id, err)
	}
	return s
}

// onlyTenant devuelve la unica empresa registrada.
func (h *tenantHarness) onlyTenant(t *testing.T) *domain.Tenant {
	t.Helper()
	if len(h.tenants.tenants) != 1 {
		t.Fatalf("empresas registradas = %d; se esperaba 1", len(h.tenants.tenants))
	}
	return h.tenants.tenants[0]
}

// expireLeases adelanta el reloj de las sagas mas alla de cualquier arriendo: lo que ve otra
// instancia cuando la que ejecutaba la saga murio.
func (h *tenantHarness) expireLeases() { h.sagas.now = h.sagas.now.Add(time.Hour) }

// forwardCalls es el alta completa, en orden.
var forwardCalls = []string{"db.create", "db.migrate", "access.seed", "identity.create", "access.assign"}

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
	if !reflect.DeepEqual(h.log.calls, forwardCalls) {
		t.Errorf("pasos = %v; want %v", h.log.calls, forwardCalls)
	}
	saga := h.sagaOf(t, tenant.ID)
	if saga.State != domain.SagaCompleted || saga.Step != domain.StepActivated || saga.LeaseUntil != nil {
		t.Errorf("saga = %s/%s (arriendo %v); want completed/activated sin arriendo", saga.State, saga.Step, saga.LeaseUntil)
	}
	admin := h.ident.users[tenant.ID]
	if admin.UserID != saga.AdminUserID || admin.Email != "admin@acme.test" || admin.Password != testAdminPassword ||
		admin.FirstName != "Admin" || admin.LastName != "Acme" {
		t.Errorf("primer administrador = %v; want el de la peticion con el id de la saga", admin)
	}
	if saga.RoleID == uuid.Nil || h.access.assigned[saga.AdminUserID] != saga.RoleID {
		t.Error("el rol del sistema sembrado debe quedar asignado al primer administrador")
	}
	if len(h.prov.dropped) != 0 {
		t.Errorf("bases borradas = %v; un alta completa no borra nada", h.prov.dropped)
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
			if len(h.tenants.tenants) != 0 || len(h.log.calls) != 0 {
				t.Error("sin celda resuelta no se registra la empresa ni se crea nada")
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
	h.log.calls = nil
	if _, err := h.uc.CreateTenant(context.Background(), baseRequest()); !errors.Is(err, domain.ErrTenantAlreadyExists) {
		t.Errorf("segundo alta = %v; want ErrTenantAlreadyExists", err)
	}
	if len(h.log.calls) != 0 {
		t.Errorf("un alta completada no se repite: %v", h.log.calls)
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
	h.log.calls = nil
	if err := h.uc.DeleteTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("borrar inactivo: %v", err)
	}
	if want := []string{"access.remove", "identity.remove"}; !reflect.DeepEqual(h.log.calls, want) {
		t.Errorf("pasos de la baja = %v; want %v", h.log.calls, want)
	}
	if len(h.tenants.tenants) != 0 || len(h.sagas.sagas) != 0 {
		t.Error("la baja retira la empresa y su saga del registro")
	}
	if len(h.access.roles) != 0 || len(h.ident.users) != 0 {
		t.Error("la baja retira los roles y las cuentas de la empresa")
	}
	if _, kept := h.prov.owner[tenant.DBName]; !kept || len(h.prov.dropped) != 0 {
		t.Error("borrar del registro no debe borrar la base fisica de una empresa que llego a operar")
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
