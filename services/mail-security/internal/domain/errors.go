package domain

import "errors"

var (
	ErrNotFound = errors.New("not found")
	// ErrValidation envuelve un mensaje legible para el cliente (422).
	ErrValidation = errors.New("validation")
	// ErrObjectNotOwned: el buzon o dominio no pertenece a la empresa de la peticion.
	ErrObjectNotOwned = errors.New("el objeto no pertenece a la empresa")
	// ErrExpansionTooDeep: la expansion de aliases supero el maximo de saltos.
	ErrExpansionTooDeep = errors.New("alias expansion too deep")
	// ErrNotConfigured: falta una integracion (contrasena del controller de Rspamd).
	ErrNotConfigured = errors.New("integracion no configurada")
	// ErrRedisUnavailable: Redis no responde; los motores lo distinguen (504).
	ErrRedisUnavailable = errors.New("redis unavailable")
)

// ValidationError lleva el detalle de una entrada rechazada.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
func (e *ValidationError) Unwrap() error { return ErrValidation }

func newValidation(msg string) error { return &ValidationError{Msg: msg} }
