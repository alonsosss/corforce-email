package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound           = errors.New("no existe")
	ErrAlreadyExists      = errors.New("ya existe")
	ErrPreconditionFailed = errors.New("la precondición de la petición no se cumple")
	ErrAddressbookLimit   = errors.New("el buzón alcanzó su número máximo de libretas")
	ErrContactLimit       = errors.New("el buzón alcanzó su número máximo de contactos")
	ErrCalendarLimit      = errors.New("el buzón alcanzó su número máximo de calendarios")
	ErrEventLimit         = errors.New("el buzón alcanzó su número máximo de eventos")
	ErrStorageLimit       = errors.New("el buzón alcanzó su espacio máximo")
	ErrResultTooLarge     = errors.New("el resultado supera el tamaño máximo de una respuesta")
	ErrInvalidSyncToken   = errors.New("el token de sincronización no es válido")
	ErrInvalidCredentials = errors.New("credenciales inválidas")
	ErrUnavailable        = errors.New("un servicio del que depende mail-dav no responde")
	ErrInvalidName        = errors.New("nombre de recurso no válido")
	ErrInvalidMailbox     = errors.New("identificador de buzón o de empresa no válido")
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

func (e *VCardError) Error() string { return "vCard no válido: " + e.Reason }

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

func (e *ICalError) Error() string { return "iCalendar no válido: " + e.Reason }

// FieldError: un campo de la API estructurada (contactos y eventos en JSON) no es valido. Field es la ruta del
// campo tal como la envio el cliente (emails[1].value, recurrence.until) y Reason va al cliente, asi que nunca
// repite el valor.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Reason }

func fieldError(field, reason string) *FieldError { return &FieldError{Field: field, Reason: reason} }

var (
	// ErrBookingLimit: la pagina de citas alcanzo su tope diario de reservas, o el visitante el suyo.
	ErrBookingLimit = errors.New("se alcanzó el máximo de reservas por día")
	// ErrSlotUnavailable: el hueco pedido ya no esta libre o no es un hueco de la pagina.
	ErrSlotUnavailable = errors.New("el hueco ya no está disponible")
)
