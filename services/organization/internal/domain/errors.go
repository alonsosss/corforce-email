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
)
