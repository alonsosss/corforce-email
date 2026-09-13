package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidEvent marca un evento cuyo contenido no se puede contabilizar y no se
	// podra al reintentar: el consumidor lo descarta en vez de reentregarlo.
	ErrInvalidEvent = errors.New("evento no contabilizable")
	// ErrCampaignNotFound: la campana no aparece ni en los eventos de campaigns ni en
	// los agregados de envio de la empresa.
	ErrCampaignNotFound = errors.New("campana sin datos de envio")
)

// ValidationError es un parametro de consulta fuera de contrato.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// IsValidation indica si el error es de un parametro de consulta.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func invalidEvent(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidEvent, fmt.Sprintf(format, args...))
}
