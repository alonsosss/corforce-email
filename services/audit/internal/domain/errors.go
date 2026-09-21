package domain

import "errors"

var (
	ErrLogNotFound           = errors.New("audit log not found")
	ErrSecurityEventNotFound = errors.New("security event not found")
	ErrInvalidSeverity       = errors.New("invalid severity")
	ErrInvalidEventType      = errors.New("invalid event type")
	ErrLogAlreadyRecorded    = errors.New("audit log already recorded")
	// ErrVerificationBusy: la empresa ya tiene un recorrido de su cadena en curso o el servicio
	// alcanzo su tope de recorridos simultaneos.
	ErrVerificationBusy = errors.New("chain verification already running")
)
