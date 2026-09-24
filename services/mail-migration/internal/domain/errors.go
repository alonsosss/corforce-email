package domain

import "errors"

var (
	ErrNotFound           = errors.New("trabajo de migración no encontrado")
	ErrMailboxNotFound    = errors.New("el buzón no existe en la empresa")
	ErrMailboxInactive    = errors.New("el buzón no está activo")
	ErrJobAlreadyActive   = errors.New("el buzón ya tiene una migración pendiente o en curso")
	ErrTenantLimitReached = errors.New("la empresa alcanzó su límite de migraciones activas")
	ErrTenantRateLimited  = errors.New("la empresa alcanzó el máximo de migraciones que puede lanzar en un día")
	ErrSourceAuthCooldown = errors.New("el servidor de origen rechazó varias veces las credenciales de esa cuenta: espere antes de reintentar")
	ErrNotConfigured      = errors.New("la migración de buzones no está disponible: falta la clave del ejecutor")
	ErrNotCancellable     = errors.New("el trabajo ya terminó y no se puede cancelar")
	ErrLeaseLost          = errors.New("el trabajo ya no está reclamado por este ejecutor")
	ErrTenantUnknown      = errors.New("empresa desconocida")
	ErrInvalidMailbox     = errors.New("identificador de buzón no válido")

	ErrInvalidHost      = errors.New("servidor de origen no válido")
	ErrHostNotAllowed   = errors.New("el servidor de origen no es una dirección pública de Internet")
	ErrHostUnresolvable = errors.New("el servidor de origen no resuelve a ninguna dirección")
	ErrInvalidPort      = errors.New("puerto de origen no permitido")
	ErrInvalidTLS       = errors.New("modo TLS de origen no permitido")
	ErrInvalidUsername  = errors.New("usuario de origen no válido")
	ErrInvalidPassword  = errors.New("contraseña de origen no válida")
	ErrInvalidOutcome   = errors.New("resultado del trabajo no válido")
	ErrInvalidProgress  = errors.New("progreso no válido")
	ErrInvalidPhase     = errors.New("fase no válida")
	ErrInvalidRunner    = errors.New("identificador de ejecutor no válido")
)

// FieldError senala el campo de la peticion que fallo, para que el cliente lo explique.
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

func fieldErr(field string, err error) error { return &FieldError{Field: field, Err: err} }

// FieldOf devuelve el campo de un error de validacion, o vacio.
func FieldOf(err error) string {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}
