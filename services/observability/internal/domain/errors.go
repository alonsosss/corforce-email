package domain

import "errors"

var (
	// ErrValidation envuelve un mensaje legible para el cliente (422).
	ErrValidation = errors.New("validation")
	// ErrNotConfigured: sin LOKI_URL el visor queda desactivado y el servicio arranca igual.
	ErrNotConfigured = errors.New("integración no configurada")
	// ErrPlatformOnly: los registros mezclan a todas las empresas y solo los lee el superadmin.
	ErrPlatformOnly = errors.New("solo el operador de la plataforma consulta los registros")
	// ErrStoreUnavailable: Loki no responde (red, plazo, 5xx).
	ErrStoreUnavailable = errors.New("el almacén de registros no responde")
	// ErrStoreRejected: Loki rechazo la consulta o su respuesta no se entiende o excede el tope.
	ErrStoreRejected = errors.New("el almacén de registros rechazó la consulta")
)

// ValidationError lleva el detalle de una entrada rechazada.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
func (e *ValidationError) Unwrap() error { return ErrValidation }

func newValidation(msg string) error { return &ValidationError{Msg: msg} }
