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
	ErrCalendarLimit      = errors.New("el buzon alcanzo su numero maximo de calendarios")
	ErrEventLimit         = errors.New("el buzon alcanzo su numero maximo de eventos")
	ErrStorageLimit       = errors.New("el buzon alcanzo su espacio maximo")
	ErrResultTooLarge     = errors.New("el resultado supera el tamano maximo de una respuesta")
	ErrInvalidSyncToken   = errors.New("el token de sincronizacion no es valido")
	ErrInvalidCredentials = errors.New("credenciales invalidas")
	ErrUnavailable        = errors.New("un servicio del que depende mail-dav no responde")
	ErrInvalidName        = errors.New("nombre de recurso no valido")
	ErrInvalidMailbox     = errors.New("identificador de buzon o de empresa no valido")
	// ErrTenantUnknown es la empresa que ya no figura en el registro: un fallo definitivo, no una base que no responde.
	ErrTenantUnknown = errors.New("empresa desconocida")
)

// UIDConflictError: otro contacto o evento de la coleccion ya usa el UID del que se guarda.
type UIDConflictError struct{ Resource string }

func (e *UIDConflictError) Error() string {
	return fmt.Sprintf("el UID ya lo usa el recurso %q", e.Resource)
}

// VCardError: el cuerpo de un PUT no es un vCard aceptable. Reason va al cliente, asi que nunca
// repite contenido del vCard.
type VCardError struct {
	Reason string
	// TooLarge distingue el exceso de tamano (413) del resto (403 valid-address-data).
	TooLarge bool
}

func (e *VCardError) Error() string { return "vCard no valido: " + e.Reason }

// ICalErrorKind distingue la precondicion de CalDAV (RFC 4791, 5.3.2.1) que incumple un PUT.
type ICalErrorKind int

const (
	// ICalInvalid: no es un iCalendar valido (valid-calendar-data).
	ICalInvalid ICalErrorKind = iota
	// ICalObject: es iCalendar, pero no un objeto de calendario admisible (valid-calendar-object-resource).
	ICalObject
	// ICalComponent: trae un componente que la coleccion no admite (supported-calendar-component).
	ICalComponent
	// ICalTooLarge: supera el tamano maximo (max-resource-size).
	ICalTooLarge
)

// ICalError: el cuerpo de un PUT no es un objeto de calendario aceptable. Reason va al cliente, asi que
// nunca repite contenido del objeto.
type ICalError struct {
	Kind   ICalErrorKind
	Reason string
}

func (e *ICalError) Error() string { return "iCalendar no valido: " + e.Reason }
