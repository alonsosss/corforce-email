package domain

import "errors"

var (
	ErrTenantNotFound      = errors.New("tenant no encontrado")
	ErrTenantAlreadyExists = errors.New("ya existe un tenant con ese slug")
	ErrInvalidTenantStatus = errors.New("estado de tenant no valido")
	// ErrTenantStillActive: un tenant activo no se borra del registro. Primero se
	// suspende o se da de baja, para que el borrado nunca sea un accidente de un clic.
	ErrTenantStillActive = errors.New("el tenant sigue activo: suspendelo o dalo de baja antes de borrarlo")
	// ErrMigrationsLocked: otra instancia esta aplicando migraciones sobre esa base.
	// No es un fallo, es trabajo ya en curso.
	ErrMigrationsLocked = errors.New("migraciones bloqueadas por otra instancia")

	ErrCellNotFound    = errors.New("celda no encontrada")
	ErrCellCodeExists  = errors.New("ya existe una celda con ese codigo")
	ErrInvalidCellCode = errors.New("codigo de celda no valido")
	// ErrCellRequired: la peticion no indica celda y la plataforma no tiene una por
	// defecto configurada. Sin celda no hay donde crear la base.
	ErrCellRequired = errors.New("no se indico celda y no hay celda por defecto configurada")
	// ErrCellNotAssignable: la celda existe pero no admite tenants nuevos (esta en
	// drenado o cerrada).
	ErrCellNotAssignable  = errors.New("la celda no admite tenants nuevos")
	ErrInvalidCellStatus  = errors.New("estado de celda no valido")
	ErrNothingToUpdate    = errors.New("la peticion no trae ningun campo que actualizar")
	ErrAdminUserRequired  = errors.New("el alta exige el correo y la contrasena del primer administrador")
	ErrAdminPasswordShort = errors.New("la contrasena del primer administrador es demasiado corta")

	// ErrTenantBusy: otra peticion o instancia tiene en curso el alta o la baja de la empresa.
	ErrTenantBusy = errors.New("la empresa tiene un alta o una baja en curso")
	// ErrLeaseLost: la saga paso a otra instancia mientras esta la ejecutaba (su arriendo
	// vencio); esta deja de escribirla.
	ErrLeaseLost = errors.New("la saga de la empresa paso a otra instancia")
	// ErrSagaNotFound: la empresa no tiene saga (se dio de alta antes de que existieran).
	ErrSagaNotFound = errors.New("la empresa no tiene saga de alta ni de baja")
	// ErrDatabaseOccupied: ya existe una base con el nombre de la empresa y no la creo su
	// alta, como la que se conserva de una empresa borrada con el mismo slug.
	ErrDatabaseOccupied = errors.New("ya existe una base con ese nombre que no pertenece a esta alta")
	// ErrProvisioningMismatch: el reintento de un alta pendiente pide otra celda.
	ErrProvisioningMismatch = errors.New("el alta pendiente de esa empresa se pidio en otra celda")
	// ErrAdminRejected: identity rechazo al primer administrador (su contrasena no cumple la
	// politica o aparece en filtraciones). Lo lleva AdminRejectedError.
	ErrAdminRejected = errors.New("identity rechazo al primer administrador")
	// ErrAdminUserConflict: identity ya tiene otro primer usuario para la empresa.
	ErrAdminUserConflict = errors.New("la empresa ya tiene un primer administrador distinto")

	// ErrInvalidMailDomain: el nombre no es un dominio de correo (nombre DNS de dos etiquetas o mas).
	ErrInvalidMailDomain = errors.New("nombre de dominio de correo no valido")
	// ErrMailDomainNotFound: el dominio no esta activo en ninguna celda (no esta en el indice).
	ErrMailDomainNotFound = errors.New("el dominio de correo no esta activo en ninguna celda")
	// ErrMailDomainClaimed: el dominio ya esta activo para otra empresa. Un dominio solo se activa
	// en una celda y para una empresa.
	ErrMailDomainClaimed = errors.New("el dominio de correo ya esta activo en otra empresa")
	// ErrTenantBeingRemoved: la empresa tiene la baja en curso; no reclama dominios de correo.
	ErrTenantBeingRemoved = errors.New("la empresa se esta dando de baja: no reclama dominios de correo")
)

// AdminRejectedError lleva el codigo y el mensaje con los que identity rechazo al primer
// administrador, para responderlos tal cual a quien pidio el alta.
type AdminRejectedError struct {
	Code    string
	Message string
}

func (e *AdminRejectedError) Error() string { return ErrAdminRejected.Error() + ": " + e.Message }

func (e *AdminRejectedError) Is(target error) bool { return target == ErrAdminRejected }
