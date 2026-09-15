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
	// ErrPlatformOnly: el cortafuegos de la celda solo lo opera el superadmin.
	ErrPlatformOnly = errors.New("solo el operador de la plataforma administra el cortafuegos de la celda")
	// ErrAlreadyExists: la red ya figura en una lista del cortafuegos.
	ErrAlreadyExists = errors.New("ya existe")
	// ErrInvalidLink: un enlace del aviso de cuarentena no vale (firma, caducidad, mensaje
	// ya liberado o descartado, enlace usado). Es un solo error a proposito: quien prueba
	// enlaces no aprende cual fallo.
	ErrInvalidLink = errors.New("enlace no valido")
	// ErrLinkUsed: el mensaje ya se libero o descarto por un enlace.
	ErrLinkUsed = errors.New("enlace ya usado")
	// ErrDKIMDomainNotActive: la celda no sirve el dominio (no esta en su directorio o no esta
	// activo) y sus claves DKIM no entran en los motores.
	ErrDKIMDomainNotActive = errors.New("el dominio no esta activo en el directorio de la celda")
)

// ValidationError lleva el detalle de una entrada rechazada.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
func (e *ValidationError) Unwrap() error { return ErrValidation }

func newValidation(msg string) error { return &ValidationError{Msg: msg} }
