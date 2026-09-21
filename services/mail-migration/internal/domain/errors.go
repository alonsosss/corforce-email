package domain

import "errors"

var (
	ErrNotFound           = errors.New("trabajo de migracion no encontrado")
	ErrMailboxNotFound    = errors.New("el buzon no existe en la empresa")
	ErrMailboxInactive    = errors.New("el buzon no esta activo")
	ErrJobAlreadyActive   = errors.New("el buzon ya tiene una migracion pendiente o en curso")
	ErrTenantLimitReached = errors.New("la empresa alcanzo su limite de migraciones activas")
	ErrTenantRateLimited  = errors.New("la empresa alcanzo el maximo de migraciones que puede lanzar en un dia")
	ErrSourceAuthCooldown = errors.New("el servidor de origen rechazo varias veces las credenciales de esa cuenta: espere antes de reintentar")
	ErrNotConfigured      = errors.New("la migracion de buzones no esta disponible: falta la clave del ejecutor")
	ErrNotCancellable     = errors.New("el trabajo ya termino y no se puede cancelar")
	ErrLeaseLost          = errors.New("el trabajo ya no esta reclamado por este ejecutor")
	ErrTenantUnknown      = errors.New("empresa desconocida")
	ErrInvalidMailbox     = errors.New("identificador de buzon no valido")

	ErrInvalidHost      = errors.New("servidor de origen no valido")
	ErrHostNotAllowed   = errors.New("el servidor de origen no es una direccion publica de Internet")
	ErrHostUnresolvable = errors.New("el servidor de origen no resuelve a ninguna direccion")
	ErrInvalidPort      = errors.New("puerto de origen no permitido")
	ErrInvalidTLS       = errors.New("modo TLS de origen no permitido")
	ErrInvalidUsername  = errors.New("usuario de origen no valido")
	ErrInvalidPassword  = errors.New("contrasena de origen no valida")
	ErrInvalidOutcome   = errors.New("resultado del trabajo no valido")
	ErrInvalidProgress  = errors.New("progreso no valido")
	ErrInvalidPhase     = errors.New("fase no valida")
	ErrInvalidRunner    = errors.New("identificador de ejecutor no valido")
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
