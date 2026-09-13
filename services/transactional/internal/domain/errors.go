package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrSendingDomainNotVerified: el dominio del remitente no esta verificado para
	// envio en la proyeccion local.
	ErrSendingDomainNotVerified = errors.New("sending domain not verified")
	// ErrAttachmentsNotSupported: esta fase no admite adjuntos.
	ErrAttachmentsNotSupported = errors.New("attachments not supported")
	// ErrTemplateNotFound: templates no conoce la plantilla o la version pedida.
	ErrTemplateNotFound = errors.New("template not found")
	// ErrSuppressionUnavailable: no se pudo consultar la lista de supresion; sin esa
	// respuesta no se encola nada (la supresion se respeta antes de encolar).
	ErrSuppressionUnavailable = errors.New("suppression service unavailable")
	// ErrTemplatesUnavailable: templates no respondio.
	ErrTemplatesUnavailable = errors.New("templates service unavailable")
	// ErrTenantMismatch: el evento de SES pertenece a otra empresa que la de la ruta.
	ErrTenantMismatch = errors.New("event tenant does not match route tenant")
	// ErrInvalidSignature: el enlace o la notificacion no supera la verificacion.
	ErrInvalidSignature = errors.New("invalid signature")
)

// ValidationError agrupa fallos de la peticion que el cliente debe corregir (422).
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func NewValidationError(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}
