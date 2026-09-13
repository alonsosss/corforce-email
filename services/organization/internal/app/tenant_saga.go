package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/alonsosss/corforce-email/services/organization/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Alta y baja de una empresa como saga. organization no escribe en identity ni en
// access_control: cada paso lo hace el dueno del dato por su API interna, todos son
// idempotentes, y el paso alcanzado queda en organization.tenant_sagas.
//
//	Alta:  registrada (inactiva) -> base creada -> base migrada -> rol sembrado ->
//	       primer usuario -> rol asignado -> activa (y tenant.created)
//	Deshacer, en orden inverso: retirar la asignacion, las cuentas, los roles y la base
//	(solo si la creo este alta).
//	Baja:  roles retirados -> cuentas retiradas -> base borrada (solo la de un alta sin
//	       completar) -> registro retirado. No se deshace: se retoma hasta terminar.
//
// Un fallo del alta la deshace y la deja en failed: el reintento con el mismo slug vuelve a
// empezar y DELETE la retira. Una caida a mitad deja la saga con el arriendo sin renovar;
// al vencer, el reintento de la peticion la retoma desde su paso, y si nadie la reintenta
// el barrido (RecoverSagas) la termina cuando el primer usuario ya existe o la deshace
// cuando no, porque su contrasena no se guarda nunca.

// sagaRecoveryBatch es cuantas sagas sin dueno retoma cada pasada del barrido.
const sagaRecoveryBatch = 20

// maxSagaErrorLen acota el error que se guarda en la saga.
const maxSagaErrorLen = 1000

// errAdminUnavailable: el alta necesita la contrasena del primer administrador y la saga se
// retomo sin la peticion original. Solo un reintento de la peticion la trae.
var errAdminUnavailable = errors.New("el alta necesita la contrasena del primer administrador: reintenta la peticion de alta")

type sagaStep struct {
	done string
	run  func(ctx context.Context) error
}

// undoStep deshace el efecto de un paso del alta. Sin undo, el efecto lo retira el deshacer
// de un paso anterior (la base borrada se lleva sus migraciones).
type undoStep struct {
	step string
	undo func(ctx context.Context) error
}

// createRun reune lo que necesita una ejecucion del alta.
type createRun struct {
	tenant *domain.Tenant
	target domain.DBTarget
	saga   *domain.TenantSaga
	// admin es nil cuando la saga se retoma sin la peticion original: entonces solo puede
	// avanzar si el primer usuario ya existe.
	admin *ports.FirstAdmin
}

// sagaContext separa la ejecucion de una saga de la cancelacion de quien la pidio (una
// conexion cortada no deja el alta a medias) y la acota por el arriendo: pasado ese tiempo
// otra instancia puede tomarla.
func (uc *OrganizationUseCase) sagaContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), uc.sagaLease)
}

func sagaErrorText(err error) string {
	msg := err.Error()
	if len(msg) > maxSagaErrorLen {
		msg = strings.ToValidUTF8(msg[:maxSagaErrorLen], "")
	}
	return msg
}

// release suelta el arriendo sin cambiar nada mas: el barrido retomara la saga.
func (uc *OrganizationUseCase) release(ctx context.Context, saga *domain.TenantSaga) {
	if err := uc.sagas.Save(ctx, saga, 0); err != nil && !errors.Is(err, domain.ErrLeaseLost) {
		uc.logger.Warn("no se pudo soltar el arriendo de la saga",
			zap.String("tenant_id", saga.TenantID.String()), zap.Error(err))
	}
}

func firstAdmin(req CreateTenantRequest, t *domain.Tenant, s *domain.TenantSaga) *ports.FirstAdmin {
	return &ports.FirstAdmin{
		UserID:    s.AdminUserID,
		Email:     req.AdminEmail,
		Password:  req.AdminPassword,
		FirstName: strOrDefault(req.AdminFirstName, "Admin"),
		LastName:  strOrDefault(req.AdminLastName, t.Name),
	}
}

// retryCreate retoma el alta pendiente de un slug ya registrado: la que fallo y se deshizo
// vuelve a empezar, y la que se corto sigue desde su ultimo paso con la contrasena de esta
// peticion. El nombre, la celda y los ajustes son los del primer intento; pedir otra celda
// es un conflicto. Una empresa completada, o en baja, sigue siendo un slug ocupado.
func (uc *OrganizationUseCase) retryCreate(ctx context.Context, tenant *domain.Tenant, req CreateTenantRequest) (*domain.Tenant, error) {
	saga, err := uc.sagas.Get(ctx, tenant.ID)
	if errors.Is(err, domain.ErrSagaNotFound) {
		return nil, domain.ErrTenantAlreadyExists
	}
	if err != nil {
		return nil, fmt.Errorf("saga de la empresa: %w", err)
	}
	if saga.Operation != domain.SagaCreate || saga.State == domain.SagaCompleted {
		return nil, domain.ErrTenantAlreadyExists
	}
	cell, err := uc.cells.GetByID(ctx, tenant.CellID)
	if err != nil {
		return nil, fmt.Errorf("celda de la empresa: %w", err)
	}
	if code := strings.ToLower(strings.TrimSpace(req.CellCode)); code != "" && code != cell.Code {
		return nil, domain.ErrProvisioningMismatch
	}
	claimed, err := uc.sagas.Claim(ctx, tenant.ID, uc.sagaLease)
	if err != nil {
		return nil, err
	}
	if claimed.Operation != domain.SagaCreate || claimed.State == domain.SagaCompleted {
		uc.release(ctx, claimed)
		return nil, domain.ErrTenantAlreadyExists
	}
	return uc.runCreate(ctx, &createRun{
		tenant: tenant, target: domain.DBTargetFor(tenant, cell), saga: claimed, admin: firstAdmin(req, tenant, claimed),
	})
}

// runCreate ejecuta el alta desde donde la dejo la saga. Un alta que compensaba termina de
// deshacerse antes de volver a empezar, y una fallida empieza de nuevo.
func (uc *OrganizationUseCase) runCreate(ctx context.Context, run *createRun) (*domain.Tenant, error) {
	runCtx, cancel := uc.sagaContext(ctx)
	defer cancel()
	saga := run.saga

	if saga.State == domain.SagaCompensating {
		if err := uc.compensateCreate(runCtx, run); err != nil {
			return nil, err
		}
	}
	if saga.State == domain.SagaFailed {
		saga.State, saga.Step, saga.LastError = domain.SagaRunning, domain.StepRegistered, ""
		if err := uc.sagas.Save(runCtx, saga, uc.sagaLease); err != nil {
			return nil, fmt.Errorf("reiniciar el alta: %w", err)
		}
	}

	for _, step := range uc.createSteps(run) {
		if saga.Reached(step.done) {
			continue
		}
		if err := step.run(runCtx); err != nil {
			return nil, uc.abortCreate(ctx, run, step.done, err)
		}
		saga.Step = step.done
		if err := uc.sagas.Save(runCtx, saga, uc.sagaLease); err != nil {
			return nil, fmt.Errorf("registrar el paso %s del alta: %w", step.done, err)
		}
	}

	if err := uc.sagas.CompleteCreate(runCtx, saga); err != nil {
		// Sin deshacer: si la activacion llego a confirmarse, deshacer borraria una empresa
		// ya activa. La saga sigue en curso y, al vencer el arriendo, el barrido la retoma y
		// la activa, porque todos sus pasos estan hechos.
		return nil, fmt.Errorf("activar la empresa: %w", err)
	}
	if fresh, err := uc.tenants.GetByID(runCtx, run.tenant.ID); err == nil {
		run.tenant = fresh
	} else {
		run.tenant.Status = domain.TenantStatusActive
	}
	tenant := run.tenant
	uc.publish("tenant.created", tenant.ID, func() error {
		return uc.publisher.TenantCreated(runCtx, tenant)
	})
	return tenant, nil
}

func (uc *OrganizationUseCase) createSteps(run *createRun) []sagaStep {
	t, saga := run.tenant, run.saga
	return []sagaStep{
		{domain.StepDatabaseCreated, func(ctx context.Context) error {
			if err := uc.provisioner.CreateDatabase(ctx, run.target, t.ID); err != nil {
				return fmt.Errorf("aprovisionar base: %w", err)
			}
			return nil
		}},
		{domain.StepDatabaseMigrated, func(ctx context.Context) error {
			if err := uc.provisioner.RunMigrations(ctx, run.target); err != nil {
				return fmt.Errorf("migrar base nueva: %w", err)
			}
			return nil
		}},
		{domain.StepRoleSeeded, func(ctx context.Context) error {
			roleID, err := uc.access.SeedTenantAdminRole(ctx, t.ID)
			if err != nil {
				return fmt.Errorf("sembrar el rol del sistema: %w", err)
			}
			saga.RoleID = roleID
			return nil
		}},
		{domain.StepUserCreated, func(ctx context.Context) error {
			if run.admin == nil {
				return errAdminUnavailable
			}
			if err := uc.identity.CreateFirstUser(ctx, t.ID, *run.admin); err != nil {
				return fmt.Errorf("crear primer administrador: %w", err)
			}
			return nil
		}},
		{domain.StepRoleAssigned, func(ctx context.Context) error {
			if saga.RoleID == uuid.Nil {
				return errors.New("asignar el rol del sistema: la saga no tiene el rol sembrado")
			}
			if err := uc.access.AssignRole(ctx, t.ID, saga.AdminUserID, saga.RoleID); err != nil {
				return fmt.Errorf("asignar el rol del sistema: %w", err)
			}
			return nil
		}},
	}
}

func (uc *OrganizationUseCase) createUndo(run *createRun) []undoStep {
	t, saga := run.tenant, run.saga
	return []undoStep{
		{domain.StepDatabaseCreated, func(ctx context.Context) error {
			return uc.provisioner.DropOwnedDatabase(ctx, run.target, t.ID)
		}},
		{domain.StepDatabaseMigrated, nil},
		{domain.StepRoleSeeded, func(ctx context.Context) error {
			return uc.access.RemoveTenantRoles(ctx, t.ID)
		}},
		{domain.StepUserCreated, func(ctx context.Context) error {
			return uc.identity.RemoveTenantUsers(ctx, t.ID)
		}},
		{domain.StepRoleAssigned, func(ctx context.Context) error {
			if saga.RoleID == uuid.Nil {
				return nil
			}
			return uc.access.RevokeRole(ctx, t.ID, saga.AdminUserID, saga.RoleID)
		}},
	}
}

// abortCreate deshace un alta que fallo en el paso attempted. El efecto de ese paso puede
// existir aunque la llamada fallase (una respuesta perdida), asi que se deshace tambien.
// Devuelve siempre la causa: es lo que tiene que saber quien pidio el alta.
func (uc *OrganizationUseCase) abortCreate(ctx context.Context, run *createRun, attempted string, cause error) error {
	compCtx, cancel := uc.sagaContext(ctx)
	defer cancel()
	saga := run.saga
	saga.State, saga.Step, saga.LastError = domain.SagaCompensating, attempted, sagaErrorText(cause)
	if err := uc.sagas.Save(compCtx, saga, uc.sagaLease); err != nil {
		uc.logger.Error("alta de empresa fallida sin registrar la compensacion; la retomara el barrido",
			zap.String("tenant", run.tenant.Slug), zap.Error(err), zap.NamedError("causa", cause))
		return cause
	}
	if err := uc.compensateCreate(compCtx, run); err != nil {
		uc.logger.Error("alta de empresa fallida y sin deshacer del todo; la retomara el barrido",
			zap.String("tenant", run.tenant.Slug), zap.Error(err), zap.NamedError("causa", cause))
		return cause
	}
	if errors.Is(cause, domain.ErrAdminRejected) {
		// Una contrasena rechazada no es un fallo del alta sino de la peticion: deshecha, no
		// queda nada de la empresa y el slug vuelve a estar libre.
		if err := uc.sagas.DeleteTenant(compCtx, saga); err != nil {
			uc.logger.Warn("alta rechazada deshecha pero sin retirar del registro",
				zap.String("tenant", run.tenant.Slug), zap.Error(err))
		}
		return cause
	}
	uc.logger.Warn("alta de empresa fallida y deshecha",
		zap.String("tenant", run.tenant.Slug), zap.String("paso", attempted), zap.Error(cause))
	return cause
}

// compensateCreate deshace, del paso mas alto al primero, lo que el alta pudo dejar hecho, y
// registra cada paso deshecho: si falla a medias, el siguiente intento sigue desde ahi. Al
// terminar, el alta queda en failed.
func (uc *OrganizationUseCase) compensateCreate(ctx context.Context, run *createRun) error {
	saga := run.saga
	saga.State = domain.SagaCompensating
	undo := uc.createUndo(run)
	for i := len(undo) - 1; i >= 0; i-- {
		u := undo[i]
		if !saga.Reached(u.step) {
			continue
		}
		if u.undo != nil {
			if err := u.undo(ctx); err != nil {
				saga.LastError = sagaErrorText(fmt.Errorf("deshacer %s: %w", u.step, err))
				uc.release(ctx, saga)
				return fmt.Errorf("deshacer %s: %w", u.step, err)
			}
		}
		saga.Step = domain.PreviousCreateStep(u.step)
		if err := uc.sagas.Save(ctx, saga, uc.sagaLease); err != nil {
			return fmt.Errorf("registrar que se deshizo %s: %w", u.step, err)
		}
	}
	saga.State, saga.Step = domain.SagaFailed, domain.StepRegistered
	if err := uc.sagas.Save(ctx, saga, 0); err != nil {
		return fmt.Errorf("cerrar la compensacion del alta: %w", err)
	}
	return nil
}

// claimDeletion toma la saga de la empresa para borrarla. Una empresa sin saga (dada de alta
// antes de que existieran) estrena una; un alta sin completar se convierte en baja y retira
// tambien la base que creo; una baja cortada se retoma donde quedo.
func (uc *OrganizationUseCase) claimDeletion(ctx context.Context, tenantID uuid.UUID) (*domain.TenantSaga, error) {
	saga, err := uc.sagas.Claim(ctx, tenantID, uc.sagaLease)
	if errors.Is(err, domain.ErrSagaNotFound) {
		saga = &domain.TenantSaga{
			TenantID: tenantID, Operation: domain.SagaDelete, State: domain.SagaRunning, Step: domain.StepDeletionStarted,
		}
		if err := uc.sagas.Insert(ctx, saga, uc.sagaLease); err != nil {
			return nil, err
		}
		return saga, nil
	}
	if err != nil {
		return nil, err
	}
	if saga.Operation == domain.SagaCreate {
		saga.DropDatabase = saga.State != domain.SagaCompleted
		saga.Operation, saga.Step, saga.LastError = domain.SagaDelete, domain.StepDeletionStarted, ""
	}
	saga.State = domain.SagaRunning
	if err := uc.sagas.Save(ctx, saga, uc.sagaLease); err != nil {
		return nil, err
	}
	return saga, nil
}

func (uc *OrganizationUseCase) deleteSteps(tenant *domain.Tenant, saga *domain.TenantSaga) []sagaStep {
	return []sagaStep{
		{domain.StepRolesRemoved, func(ctx context.Context) error {
			if err := uc.access.RemoveTenantRoles(ctx, tenant.ID); err != nil {
				return fmt.Errorf("retirar los roles: %w", err)
			}
			return nil
		}},
		{domain.StepUsersRemoved, func(ctx context.Context) error {
			if err := uc.identity.RemoveTenantUsers(ctx, tenant.ID); err != nil {
				return fmt.Errorf("retirar las cuentas: %w", err)
			}
			return nil
		}},
		{domain.StepDatabaseDropped, func(ctx context.Context) error {
			if !saga.DropDatabase {
				return nil
			}
			target, err := uc.targetFor(ctx, tenant)
			if err != nil {
				return err
			}
			if err := uc.provisioner.DropOwnedDatabase(ctx, target, tenant.ID); err != nil {
				return fmt.Errorf("borrar la base del alta sin completar: %w", err)
			}
			return nil
		}},
	}
}

// runDelete avanza la baja desde su paso y, al final, retira la empresa del registro con su
// saga. Un fallo suelta el arriendo con la baja en curso: el barrido la reintenta.
func (uc *OrganizationUseCase) runDelete(ctx context.Context, tenant *domain.Tenant, saga *domain.TenantSaga) error {
	for _, step := range uc.deleteSteps(tenant, saga) {
		if saga.Reached(step.done) {
			continue
		}
		if err := step.run(ctx); err != nil {
			saga.LastError = sagaErrorText(err)
			uc.release(ctx, saga)
			return err
		}
		saga.Step = step.done
		if err := uc.sagas.Save(ctx, saga, uc.sagaLease); err != nil {
			return fmt.Errorf("registrar el paso %s de la baja: %w", step.done, err)
		}
	}
	if err := uc.sagas.DeleteTenant(ctx, saga); err != nil {
		return fmt.Errorf("retirar la empresa del registro: %w", err)
	}
	return nil
}

// RecoverSagas retoma las sagas que se quedaron sin dueno: la instancia que las ejecutaba
// murio (su arriendo vencio) o un fallo solto el arriendo para reintentarlo despues. Una baja
// se termina y una compensacion tambien; un alta en curso se termina si el primer usuario ya
// existe y se deshace si no. Varias instancias pueden barrer a la vez: cada saga la toma una
// sola. Devuelve cuantas cerro.
func (uc *OrganizationUseCase) RecoverSagas(ctx context.Context) (int, error) {
	stale, err := uc.sagas.ListStale(ctx, sagaRecoveryBatch)
	if err != nil {
		return 0, fmt.Errorf("sagas sin dueno: %w", err)
	}
	recovered := 0
	for _, s := range stale {
		if err := uc.recoverSaga(ctx, s.TenantID); err != nil {
			if !errors.Is(err, domain.ErrTenantBusy) {
				uc.logger.Warn("saga de empresa sin recuperar",
					zap.String("tenant_id", s.TenantID.String()), zap.Error(err))
			}
			continue
		}
		recovered++
	}
	return recovered, nil
}

func (uc *OrganizationUseCase) recoverSaga(ctx context.Context, tenantID uuid.UUID) error {
	saga, err := uc.sagas.Claim(ctx, tenantID, uc.sagaLease)
	if err != nil {
		return err
	}
	if saga.State != domain.SagaRunning && saga.State != domain.SagaCompensating {
		uc.release(ctx, saga)
		return domain.ErrTenantBusy
	}
	tenant, err := uc.tenants.GetByID(ctx, tenantID)
	if err != nil {
		uc.release(ctx, saga)
		return fmt.Errorf("empresa de la saga: %w", err)
	}
	runCtx, cancel := uc.sagaContext(ctx)
	defer cancel()
	if saga.Operation == domain.SagaDelete {
		return uc.runDelete(runCtx, tenant, saga)
	}
	target, err := uc.targetFor(runCtx, tenant)
	if err != nil {
		uc.release(ctx, saga)
		return err
	}
	run := &createRun{tenant: tenant, target: target, saga: saga}
	if saga.State == domain.SagaRunning {
		if saga.Reached(domain.StepUserCreated) {
			// Lo que falta (asignar el rol y activar) no necesita la contrasena.
			_, err := uc.runCreate(runCtx, run)
			return err
		}
		// Sin la contrasena el alta no puede seguir: se deshace, incluido el paso que pudo
		// quedar a medias cuando se corto.
		saga.Step = domain.NextCreateStep(saga.Step)
		if saga.LastError == "" {
			saga.LastError = "alta interrumpida: la instancia que la ejecutaba no termino"
		}
	}
	return uc.compensateCreate(runCtx, run)
}
