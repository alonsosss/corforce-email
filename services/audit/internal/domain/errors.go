package domain

import "errors"

var (
	ErrLogNotFound           = errors.New("audit log not found")
	ErrSecurityEventNotFound = errors.New("security event not found")
	ErrInvalidSeverity       = errors.New("invalid severity")
	ErrInvalidEventType      = errors.New("invalid event type")
)
