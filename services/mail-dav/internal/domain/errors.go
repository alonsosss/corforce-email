package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound           = errors.New("no existe")
	ErrAlreadyExists      = errors.New("ya existe")
	ErrPreconditionFailed = errors.New("la precondicion de la peticion no se cumple")
	ErrAddressbookLimit   = errors.New("el buzon alcanzo su numero maximo de libretas")
	ErrContactLimit       = errors.New("el buzon alcanzo su numero maximo de contactos")
	ErrInvalidSyncToken   = errors.New("el token de sincronizacion no es valido")
	ErrInvalidCredentials = errors.New("credenciales invalidas")
	ErrUnavailable        = errors.New("un servicio del que depende mail-dav no responde")
	ErrInvalidName        = errors.New("nombre de recurso no valido")
	ErrInvalidMailbox     = errors.New("identificador de buzon o de empresa no valido")
	// ErrTenantUnknown es la empresa que ya no figura en el registro: un fallo definitivo, no una base que no responde.
	ErrTenantUnknown = errors.New("empresa desconocida")
)

// UIDConflictError: otro contacto de la libreta ya usa el UID del que se guarda.
type UIDConflictError struct{ Resource string }

func (e *UIDConflictError) Error() string {
	return fmt.Sprintf("el UID ya lo usa el contacto %q", e.Resource)
}

// VCardError: el cuerpo de un PUT no es un vCard aceptable. Reason va al cliente, asi que nunca
// repite contenido del vCard.
type VCardError struct {
	Reason string
	// TooLarge distingue el exceso de tamano (413) del resto (403 valid-address-data).
	TooLarge bool
}

func (e *VCardError) Error() string { return "vCard no valido: " + e.Reason }
