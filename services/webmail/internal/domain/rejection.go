package domain

import "errors"

// RejectionKind es la clase de un rechazo de un servicio dueno del dato. El adaptador HTTP la
// traduce a su codigo de estado; el dominio no sabe de HTTP.
type RejectionKind int

const (
	RejectBadRequest RejectionKind = iota + 1
	RejectValidation
	RejectNotFound
	RejectPrecondition
	RejectTooLarge
	RejectQuota
	RejectRateLimited
	RejectUnavailable
)

// ErrResourceNotFound es un contacto o un evento que no existe (o no es del buzon).
var ErrResourceNotFound = errors.New("recurso no encontrado")

// ServiceRejection es un rechazo de mail-dav que el webmail entrega al usuario tal cual: su codigo,
// su mensaje y sus detalles (details.field, details.limit, details.max_days). ETag acompana a una
// precondicion fallida (la version vigente) y RetryAfter a un cupo o una saturacion.
type ServiceRejection struct {
	Kind       RejectionKind
	Code       string
	Message    string
	Details    map[string]string
	ETag       string
	RetryAfter string
}

func (e *ServiceRejection) Error() string { return e.Code + ": " + e.Message }

// Is deja comprobar la clase con los errores del dominio que ya la nombran.
func (e *ServiceRejection) Is(target error) bool {
	switch target {
	case ErrPreconditionFailed:
		return e.Kind == RejectPrecondition
	case ErrResourceNotFound:
		return e.Kind == RejectNotFound
	case ErrUnavailable:
		return e.Kind == RejectUnavailable
	}
	return false
}
