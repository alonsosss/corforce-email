package domain

import (
	"time"

	"github.com/google/uuid"
)

// El alta y la baja de una empresa son sagas: pasos que llaman al dueno de cada dato (la
// base de la empresa, access-control, identity, el directorio de correo de su celda), todos
// idempotentes, con el paso alcanzado guardado en organization.tenant_sagas para retomarlos tras
// una caida o deshacerlos si fallan.

// Operaciones de una saga.
const (
	SagaCreate = "create"
	SagaDelete = "delete"
)

// Estados de una saga. running avanza; compensating deshace un alta fallida; failed es un
// alta ya deshecha que espera un reintento o su borrado; completed cierra el alta. Una baja
// no se deshace: sus pasos borran y son idempotentes, asi que se retoma hasta terminar, y
// al terminar la saga desaparece con la empresa.
const (
	SagaRunning      = "running"
	SagaCompensating = "compensating"
	SagaFailed       = "failed"
	SagaCompleted    = "completed"
)

// Pasos del alta, en orden.
const (
	StepRegistered       = "registered"
	StepDatabaseCreated  = "database_created"
	StepDatabaseMigrated = "database_migrated"
	StepRoleSeeded       = "role_seeded"
	StepUserCreated      = "user_created"
	StepRoleAssigned     = "role_assigned"
	StepActivated        = "activated"
)

// Pasos de la baja, en orden. Los dos de correo van detras de database_dropped para que una baja
// guardada en ese paso por una version anterior los haga al retomarse.
const (
	StepDeletionStarted     = "deletion_started"
	StepRolesRemoved        = "roles_removed"
	StepUsersRemoved        = "users_removed"
	StepDatabaseDropped     = "database_dropped"
	StepMailRetired         = "mail_retired"
	StepMailDomainsReleased = "mail_domains_released"
)

var (
	createSteps = []string{StepRegistered, StepDatabaseCreated, StepDatabaseMigrated, StepRoleSeeded,
		StepUserCreated, StepRoleAssigned, StepActivated}
	deleteSteps = []string{StepDeletionStarted, StepRolesRemoved, StepUsersRemoved, StepDatabaseDropped,
		StepMailRetired, StepMailDomainsReleased}
)

// TenantSaga es el estado persistido del alta o la baja de una empresa.
//
// Step es el ultimo paso terminado mientras la saga avanza, y el paso mas alto cuyo efecto
// puede quedar mientras compensa. La contrasena del primer administrador no esta aqui: no
// se guarda nunca.
type TenantSaga struct {
	TenantID  uuid.UUID
	Operation string
	State     string
	Step      string
	// AdminUserID es el id del primer administrador, elegido al empezar el alta: repetir el
	// paso con el mismo id es reintentarlo en identity, no pedir otra cuenta.
	AdminUserID uuid.UUID
	// RoleID es el rol del sistema sembrado; uuid.Nil hasta sembrarlo.
	RoleID uuid.UUID
	// DropDatabase: la baja retira tambien la base, porque la creo un alta que no llego a
	// completarse. La base de una empresa que llego a operar se conserva.
	DropDatabase bool
	Attempts     int
	LastError    string
	LeaseToken   uuid.UUID
	LeaseUntil   *time.Time
}

// Reached indica si la saga ya paso por step en el orden de su operacion.
func (s *TenantSaga) Reached(step string) bool {
	order := createSteps
	if s.Operation == SagaDelete {
		order = deleteSteps
	}
	at, target := stepIndex(order, s.Step), stepIndex(order, step)
	return at >= 0 && target >= 0 && at >= target
}

// Pending indica si la empresa tiene un alta sin completar o una baja en curso: mientras
// tanto su estado no se cambia a mano.
func (s *TenantSaga) Pending() bool {
	return s.Operation != SagaCreate || s.State != SagaCompleted
}

// NextCreateStep devuelve el paso del alta que sigue a step: el que pudo quedar a medias si
// la ejecucion se corto despues de terminar step.
func NextCreateStep(step string) string {
	i := stepIndex(createSteps, step)
	if i < 0 || i+1 >= len(createSteps) {
		return step
	}
	return createSteps[i+1]
}

// PreviousCreateStep devuelve el paso anterior a step: donde queda el alta tras deshacerlo.
func PreviousCreateStep(step string) string {
	if i := stepIndex(createSteps, step); i > 0 {
		return createSteps[i-1]
	}
	return StepRegistered
}

func stepIndex(order []string, step string) int {
	for i, s := range order {
		if s == step {
			return i
		}
	}
	return -1
}
