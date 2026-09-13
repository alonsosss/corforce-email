package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/alonsosss/corforce-email/services/organization/internal/ports"
	"github.com/google/uuid"
)

func indexOf(list []string, item string) int {
	for i, s := range list {
		if s == item {
			return i
		}
	}
	return -1
}

// assertNothingLeft comprueba que del alta no queda nada fuera del registro: ni base, ni
// rol, ni asignacion, ni cuenta, ni aviso de empresa creada.
func (h *tenantHarness) assertNothingLeft(t *testing.T) {
	t.Helper()
	if len(h.prov.owner) != 0 || len(h.access.roles) != 0 || len(h.access.assigned) != 0 || len(h.ident.users) != 0 {
		t.Fatalf("quedaron efectos del alta: bases %v, roles %v, asignaciones %v, cuentas %v",
			h.prov.owner, h.access.roles, h.access.assigned, h.ident.users)
	}
	if len(h.pub.created) != 0 {
		t.Fatal("un alta deshecha no anuncia tenant.created")
	}
}

// Cada punto de fallo del alta deshace, en orden inverso, lo que pudo quedar hecho,
// incluido el paso que fallo (su efecto puede existir aunque la respuesta se perdiera), y
// deja la empresa inactiva con la saga en failed.
func TestAltaFallidaSeDeshaceEnOrdenInverso(t *testing.T) {
	cases := []struct {
		failOn string
		undo   []string
	}{
		{"db.create", []string{"db.drop"}},
		{"db.migrate", []string{"db.drop"}},
		{"access.seed", []string{"access.remove", "db.drop"}},
		{"identity.create", []string{"identity.remove", "access.remove", "db.drop"}},
		{"access.assign", []string{"access.revoke", "identity.remove", "access.remove", "db.drop"}},
	}
	for _, c := range cases {
		t.Run(c.failOn, func(t *testing.T) {
			h := newTenantHarness("pe-01")
			h.log.failOn(c.failOn, 1)
			_, err := h.uc.CreateTenant(context.Background(), baseRequest())
			var simulated fakeError
			if !errors.As(err, &simulated) || string(simulated) != c.failOn {
				t.Fatalf("err = %v; want el fallo de %s", err, c.failOn)
			}
			want := append(append([]string{}, forwardCalls[:indexOf(forwardCalls, c.failOn)+1]...), c.undo...)
			if !reflect.DeepEqual(h.log.calls, want) {
				t.Fatalf("llamadas = %v; want %v", h.log.calls, want)
			}
			tenant := h.onlyTenant(t)
			if tenant.Status != domain.TenantStatusInactive {
				t.Errorf("estado = %s; un alta fallida no deja la empresa activa", tenant.Status)
			}
			saga := h.sagaOf(t, tenant.ID)
			if saga.State != domain.SagaFailed || saga.Step != domain.StepRegistered || saga.LeaseUntil != nil {
				t.Errorf("saga = %s/%s (arriendo %v); want failed/registered sin arriendo", saga.State, saga.Step, saga.LeaseUntil)
			}
			if !strings.Contains(saga.LastError, c.failOn) {
				t.Errorf("last_error = %q; debe decir que fallo %s", saga.LastError, c.failOn)
			}
			h.assertNothingLeft(t)
		})
	}
}

// Un alta fallida y deshecha se reintenta con el mismo slug: vuelve a empezar, con la misma
// empresa y el mismo id de primer administrador.
func TestAltaFallidaSeReintentaConElMismoSlug(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.log.failOn("access.seed", 1)
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo de la siembra")
	}
	first := h.onlyTenant(t)
	adminID := h.sagaOf(t, first.ID).AdminUserID

	h.log.calls = nil
	tenant, err := h.uc.CreateTenant(ctx, baseRequest())
	if err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if tenant.ID != first.ID || tenant.Status != domain.TenantStatusActive {
		t.Fatalf("reintento = %s/%s; want la misma empresa, activa", tenant.ID, tenant.Status)
	}
	if !reflect.DeepEqual(h.log.calls, forwardCalls) {
		t.Errorf("pasos del reintento = %v; want el alta completa %v", h.log.calls, forwardCalls)
	}
	saga := h.sagaOf(t, tenant.ID)
	if saga.AdminUserID != adminID || saga.Attempts != 2 || saga.State != domain.SagaCompleted {
		t.Errorf("saga = admin %s intentos %d estado %s; want el mismo admin, 2 intentos, completed",
			saga.AdminUserID, saga.Attempts, saga.State)
	}
}

// Una contrasena que identity rechaza no es un fallo del alta sino de la peticion: se deshace
// todo, la empresa no queda registrada y el slug vuelve a estar libre. El codigo de identity
// llega tal cual a quien pidio el alta.
func TestContrasenaRechazadaNoDejaRastro(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.ident.reject = &domain.AdminRejectedError{Code: "PASSWORD_BREACHED", Message: "esa contrasena aparece en filtraciones publicas"}
	_, err := h.uc.CreateTenant(ctx, baseRequest())
	var rejected *domain.AdminRejectedError
	if !errors.As(err, &rejected) || rejected.Code != "PASSWORD_BREACHED" || !errors.Is(err, domain.ErrAdminRejected) {
		t.Fatalf("err = %v; want el rechazo de identity con su codigo", err)
	}
	if len(h.tenants.tenants) != 0 || len(h.sagas.sagas) != 0 {
		t.Fatal("una contrasena rechazada no deja la empresa registrada")
	}
	h.assertNothingLeft(t)

	h.ident.reject = nil
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err != nil {
		t.Fatalf("el slug debe quedar libre: %v", err)
	}
}

// Si deshacer tambien falla, la saga queda compensando sin arriendo con el paso donde se
// corto, y el barrido termina de deshacerla.
func TestCompensacionQueFallaLaTerminaElBarrido(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.log.failOn("access.assign", 1)
	h.log.failOn("access.remove", 1)
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo de la asignacion")
	}
	tenant := h.onlyTenant(t)
	saga := h.sagaOf(t, tenant.ID)
	if saga.State != domain.SagaCompensating || saga.Step != domain.StepRoleSeeded || saga.LeaseUntil != nil {
		t.Fatalf("saga = %s/%s (arriendo %v); want compensating/role_seeded sin arriendo", saga.State, saga.Step, saga.LeaseUntil)
	}
	if len(h.prov.owner) != 1 {
		t.Fatal("la base sigue: el deshacer se corto antes de llegar a ella")
	}

	h.log.calls = nil
	n, err := h.uc.RecoverSagas(ctx)
	if err != nil || n != 1 {
		t.Fatalf("RecoverSagas = %d, %v; want 1", n, err)
	}
	if want := []string{"access.remove", "db.drop"}; !reflect.DeepEqual(h.log.calls, want) {
		t.Errorf("el barrido deshizo %v; want %v", h.log.calls, want)
	}
	saga = h.sagaOf(t, tenant.ID)
	if saga.State != domain.SagaFailed || saga.Step != domain.StepRegistered {
		t.Errorf("saga = %s/%s; want failed/registered", saga.State, saga.Step)
	}
	h.assertNothingLeft(t)
}

// Una instancia que muere a mitad del alta (aqui, el registro cae justo despues de sembrar
// el rol) deja la saga en su ultimo paso guardado y con el arriendo vigente: mientras dure,
// el reintento es un conflicto; al vencer, el reintento de la peticion sigue desde ese paso
// sin repetir la base, y repetir la siembra no crea otro rol.
func TestAltaCortadaSeRetomaDesdeSuPaso(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.sagas.saveFailAt = 3
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo del registro")
	}
	tenant := h.onlyTenant(t)
	if saga := h.sagaOf(t, tenant.ID); saga.State != domain.SagaRunning || saga.Step != domain.StepDatabaseMigrated {
		t.Fatalf("saga = %s/%s; want running/database_migrated", saga.State, saga.Step)
	}
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); !errors.Is(err, domain.ErrTenantBusy) {
		t.Fatalf("con el arriendo vigente = %v; want ErrTenantBusy", err)
	}

	h.expireLeases()
	h.log.calls = nil
	got, err := h.uc.CreateTenant(ctx, baseRequest())
	if err != nil {
		t.Fatalf("reintento tras la caida: %v", err)
	}
	if want := []string{"access.seed", "identity.create", "access.assign"}; !reflect.DeepEqual(h.log.calls, want) {
		t.Errorf("pasos del reintento = %v; want %v", h.log.calls, want)
	}
	if got.Status != domain.TenantStatusActive || len(h.access.roles) != 1 || len(h.prov.created) != 1 {
		t.Errorf("empresa %s, roles %d, bases creadas %d; want activa, un rol, una base", got.Status, len(h.access.roles), len(h.prov.created))
	}
}

// Si la instancia muere con el primer usuario ya creado, el barrido termina el alta sin la
// contrasena: solo falta asignar el rol y activar.
func TestBarridoTerminaUnAltaConElUsuarioYaCreado(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.sagas.saveFailAt = 5
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo del registro")
	}
	tenant := h.onlyTenant(t)
	if saga := h.sagaOf(t, tenant.ID); saga.Step != domain.StepUserCreated {
		t.Fatalf("paso guardado = %s; want user_created", saga.Step)
	}

	h.expireLeases()
	h.log.calls = nil
	if n, err := h.uc.RecoverSagas(ctx); err != nil || n != 1 {
		t.Fatalf("RecoverSagas = %d, %v; want 1", n, err)
	}
	if want := []string{"access.assign"}; !reflect.DeepEqual(h.log.calls, want) {
		t.Errorf("el barrido hizo %v; want %v", h.log.calls, want)
	}
	if tenant.Status != domain.TenantStatusActive || h.sagaOf(t, tenant.ID).State != domain.SagaCompleted {
		t.Error("el barrido deja la empresa activa y la saga completada")
	}
	if len(h.pub.created) != 1 {
		t.Errorf("eventos tenant.created = %d; want 1", len(h.pub.created))
	}
}

// Si la instancia muere antes de crear el primer usuario, el barrido no puede seguir (la
// contrasena no se guarda): deshace el alta, incluido el paso que pudo quedar a medias.
func TestBarridoDeshaceUnAltaCortadaAntesDelUsuario(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.sagas.saveFailAt = 3
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo del registro")
	}
	tenant := h.onlyTenant(t)

	h.expireLeases()
	h.log.calls = nil
	if n, err := h.uc.RecoverSagas(ctx); err != nil || n != 1 {
		t.Fatalf("RecoverSagas = %d, %v; want 1", n, err)
	}
	if want := []string{"access.remove", "db.drop"}; !reflect.DeepEqual(h.log.calls, want) {
		t.Errorf("el barrido deshizo %v; want %v", h.log.calls, want)
	}
	saga := h.sagaOf(t, tenant.ID)
	if saga.State != domain.SagaFailed || saga.LastError == "" {
		t.Errorf("saga = %s (%q); want failed con el motivo", saga.State, saga.LastError)
	}
	h.assertNothingLeft(t)
}

// Una activacion que falla no se deshace (pudo confirmarse): la saga sigue en curso y el
// barrido la activa cuando vence el arriendo.
func TestActivacionFallidaNoSeDeshace(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.sagas.completeErr = errors.New("registro caido al activar")
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo de la activacion")
	}
	tenant := h.onlyTenant(t)
	if len(h.prov.owner) != 1 || len(h.access.assigned) != 1 || len(h.ident.users) != 1 {
		t.Fatal("una activacion fallida no deshace nada")
	}

	h.sagas.completeErr = nil
	h.expireLeases()
	h.log.calls = nil
	if n, err := h.uc.RecoverSagas(ctx); err != nil || n != 1 {
		t.Fatalf("RecoverSagas = %d, %v; want 1", n, err)
	}
	if len(h.log.calls) != 0 || tenant.Status != domain.TenantStatusActive {
		t.Errorf("llamadas %v, estado %s; want solo la activacion", h.log.calls, tenant.Status)
	}
}

// Una baja que falla a medias sigue en curso con su paso guardado y la empresa registrada;
// el siguiente DELETE sigue desde ahi sin repetir lo hecho.
func TestBajaSeRetomaDondeQuedo(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	tenant, err := h.uc.CreateTenant(ctx, baseRequest())
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := h.uc.SetTenantStatus(ctx, tenant.ID, domain.TenantStatusInactive); err != nil {
		t.Fatalf("dar de baja: %v", err)
	}
	h.log.failOn("identity.remove", 1)
	if err := h.uc.DeleteTenant(ctx, tenant.ID); err == nil {
		t.Fatal("se esperaba el fallo de identity")
	}
	saga := h.sagaOf(t, tenant.ID)
	if saga.Operation != domain.SagaDelete || saga.State != domain.SagaRunning ||
		saga.Step != domain.StepRolesRemoved || saga.LeaseUntil != nil {
		t.Fatalf("saga = %s %s/%s (arriendo %v); want delete running/roles_removed sin arriendo",
			saga.Operation, saga.State, saga.Step, saga.LeaseUntil)
	}
	if _, err := h.tenants.GetByID(ctx, tenant.ID); err != nil {
		t.Fatal("la empresa sigue registrada hasta terminar la baja")
	}

	h.log.calls = nil
	if err := h.uc.DeleteTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("reintento de la baja: %v", err)
	}
	if want := []string{"identity.remove"}; !reflect.DeepEqual(h.log.calls, want) {
		t.Errorf("pasos del reintento = %v; want %v", h.log.calls, want)
	}
	if len(h.tenants.tenants) != 0 || len(h.sagas.sagas) != 0 {
		t.Error("la baja termina retirando la empresa y su saga")
	}
}

// Una baja que fallo la termina el barrido: sus pasos no necesitan nada de la peticion.
func TestBarridoTerminaUnaBaja(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	tenant, err := h.uc.CreateTenant(ctx, baseRequest())
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := h.uc.SetTenantStatus(ctx, tenant.ID, domain.TenantStatusSuspended); err != nil {
		t.Fatalf("suspender: %v", err)
	}
	h.log.failOn("access.remove", 1)
	if err := h.uc.DeleteTenant(ctx, tenant.ID); err == nil {
		t.Fatal("se esperaba el fallo de access-control")
	}
	if n, err := h.uc.RecoverSagas(ctx); err != nil || n != 1 {
		t.Fatalf("RecoverSagas = %d, %v; want 1", n, err)
	}
	if len(h.tenants.tenants) != 0 || len(h.access.roles) != 0 || len(h.ident.users) != 0 {
		t.Error("el barrido termina la baja")
	}
}

// Borrar una empresa cuyo alta no llego a completarse (aqui, con la compensacion cortada)
// retira tambien la base que ese alta creo.
func TestBorrarUnAltaSinCompletarRetiraSuBase(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.log.failOn("identity.create", 1)
	h.log.failOn("access.remove", 1)
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo del alta")
	}
	tenant := h.onlyTenant(t)
	if len(h.prov.owner) != 1 {
		t.Fatal("la compensacion cortada deja la base")
	}

	h.log.calls = nil
	if err := h.uc.DeleteTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if want := []string{"access.remove", "identity.remove", "db.drop"}; !reflect.DeepEqual(h.log.calls, want) {
		t.Errorf("pasos de la baja = %v; want %v", h.log.calls, want)
	}
	if len(h.tenants.tenants) != 0 {
		t.Error("la empresa sale del registro")
	}
	h.assertNothingLeft(t)
}

// Una empresa dada de alta antes de las sagas no tiene fila de saga: la baja estrena una, y
// su base (sin marca) se conserva.
func TestBorrarUnaEmpresaAnteriorALasSagas(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	legacy := &domain.Tenant{
		ID: uuid.New(), Slug: "antigua", DBName: "mail_tenant_antigua",
		Status: domain.TenantStatusInactive, CellID: h.cells.cells[0].ID,
	}
	h.tenants.tenants = append(h.tenants.tenants, legacy)
	h.prov.owner = map[string]uuid.UUID{legacy.DBName: uuid.Nil}

	if err := h.uc.DeleteTenant(ctx, legacy.ID); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if want := []string{"access.remove", "identity.remove"}; !reflect.DeepEqual(h.log.calls, want) {
		t.Errorf("pasos de la baja = %v; want %v", h.log.calls, want)
	}
	if _, kept := h.prov.owner[legacy.DBName]; !kept || len(h.tenants.tenants) != 0 {
		t.Error("la empresa sale del registro y su base se conserva")
	}
}

// El estado de una empresa con el alta sin completar no se cambia a mano: activarla la
// pondria en servicio sin administrador.
func TestUnaEmpresaAMediasNoSeActivaAMano(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.log.failOn("db.migrate", 1)
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo de migracion")
	}
	tenant := h.onlyTenant(t)
	if _, err := h.uc.SetTenantStatus(ctx, tenant.ID, domain.TenantStatusActive); !errors.Is(err, domain.ErrTenantBusy) {
		t.Fatalf("activar a mano = %v; want ErrTenantBusy", err)
	}
	if tenant.Status != domain.TenantStatusInactive {
		t.Error("sigue inactiva")
	}
}

func TestReintentoEnOtraCeldaEsConflicto(t *testing.T) {
	ctx := context.Background()
	h := newTenantHarness("pe-01")
	h.cells.cells = append(h.cells.cells, &domain.Cell{ID: uuid.New(), Code: "pe-02", Status: domain.CellStatusActive})
	h.log.failOn("db.migrate", 1)
	if _, err := h.uc.CreateTenant(ctx, baseRequest()); err == nil {
		t.Fatal("se esperaba el fallo de migracion")
	}
	req := baseRequest()
	req.CellCode = "pe-02"
	if _, err := h.uc.CreateTenant(ctx, req); !errors.Is(err, domain.ErrProvisioningMismatch) {
		t.Fatalf("reintento en otra celda = %v; want ErrProvisioningMismatch", err)
	}
}

// Una base con el nombre de la empresa que no creo este alta (la conservada de una empresa
// borrada con el mismo slug) no se adopta ni se borra al deshacer.
func TestBaseAjenaConElMismoNombreNoSeToca(t *testing.T) {
	h := newTenantHarness("pe-01")
	h.prov.owner = map[string]uuid.UUID{"mail_tenant_acme_corp": uuid.Nil}
	_, err := h.uc.CreateTenant(context.Background(), baseRequest())
	if !errors.Is(err, domain.ErrDatabaseOccupied) {
		t.Fatalf("err = %v; want ErrDatabaseOccupied", err)
	}
	if _, kept := h.prov.owner["mail_tenant_acme_corp"]; !kept {
		t.Fatal("deshacer el alta no borra una base ajena")
	}
	if saga := h.sagaOf(t, h.onlyTenant(t).ID); saga.State != domain.SagaFailed {
		t.Errorf("saga = %s; want failed", saga.State)
	}
}

// La contrasena del primer administrador no sale en ninguna forma de imprimirlo.
func TestLaContrasenaDelAdministradorNoSeImprime(t *testing.T) {
	admin := ports.FirstAdmin{UserID: uuid.New(), Email: "admin@acme.test", Password: testAdminPassword}
	for _, s := range []string{fmt.Sprint(admin), fmt.Sprintf("%+v", admin), fmt.Sprintf("%#v", admin), fmt.Sprintf("%v", &admin)} {
		if strings.Contains(s, testAdminPassword) {
			t.Fatalf("la contrasena aparece al imprimir: %s", s)
		}
	}
}
