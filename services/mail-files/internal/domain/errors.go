package domain

import "errors"

var (
	// ErrNotFound: el fichero no existe o no es del buzon que lo pide.
	ErrNotFound = errors.New("el fichero no existe")
	// ErrLinkInvalid es la misma respuesta publica para una firma alterada, un enlace caducado,
	// revocado o agotado, una empresa que no existe o un objeto que ya no esta: el visitante no
	// puede distinguir un caso de otro.
	ErrLinkInvalid = errors.New("el enlace no es valido o ya no esta vigente")
	// ErrTooLarge: el fichero supera el tope servido.
	ErrTooLarge = errors.New("el fichero supera el tamano maximo")
	// ErrEmpty: un fichero sin contenido no se comparte.
	ErrEmpty = errors.New("el fichero esta vacio")
	// ErrIncomplete: la subida se corto antes de terminar.
	ErrIncomplete = errors.New("la subida no se completo")
	// ErrInfected: ClamAV encontro una firma. El fichero no se guarda.
	ErrInfected = errors.New("el fichero contiene un virus y no se ha guardado")
	// ErrScanUnavailable: sin veredicto limpio de ClamAV no se guarda nada (falla cerrado).
	ErrScanUnavailable = errors.New("el analisis antivirus no esta disponible")
	// ErrMailboxQuota y ErrTenantQuota: el fichero no cabe en el espacio del buzon o de la empresa.
	ErrMailboxQuota = errors.New("el fichero no cabe en el espacio de ficheros compartidos del buzon")
	ErrTenantQuota  = errors.New("el fichero no cabe en el espacio de ficheros compartidos de la empresa")
	// ErrTooManyFiles: el buzon ya tiene el maximo de enlaces vigentes.
	ErrTooManyFiles = errors.New("el buzon ya tiene el maximo de enlaces vigentes")
	// ErrBusy: se atienden ya todas las subidas que el servicio admite a la vez.
	ErrBusy = errors.New("hay demasiadas subidas en curso; intentalo en unos segundos")
	// ErrStorageDisabled: el servicio no tiene almacen de objetos configurado.
	ErrStorageDisabled = errors.New("el envio de ficheros grandes no esta disponible")
	// ErrNotRevocable: el enlace ya no esta vigente.
	ErrNotRevocable = errors.New("el enlace ya no esta vigente")
	// ErrTenantUnknown: la empresa no figura en el registro.
	ErrTenantUnknown = errors.New("empresa desconocida")
	// ErrUnavailable: una dependencia (base, almacen) no respondio.
	ErrUnavailable = errors.New("servicio no disponible")
)

// ValidationError es un dato de la peticion que no se admite; Field nombra el campo.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func NewValidationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
