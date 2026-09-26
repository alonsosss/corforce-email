package domain

import "errors"

var (
	ErrUserNotFound       = errors.New("user not found")
	ErrUserAlreadyExists  = errors.New("user already exists")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountLocked      = errors.New("account is locked")
	ErrAccountInactive    = errors.New("account is inactive")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionExpired     = errors.New("session expired")
	ErrSessionRevoked     = errors.New("session revoked")
	ErrSessionIdle        = errors.New("session expired by inactivity")
	ErrTokenBlocked       = errors.New("token has been revoked")
	ErrPasswordPolicyFail = errors.New("password does not meet policy requirements")
	ErrPasswordReused     = errors.New("password was recently used")
	ErrPasswordBreached   = errors.New("password appears in known data breaches")
	ErrTenantNotFound     = errors.New("tenant not found")
	ErrInvalidMFACode     = errors.New("invalid or expired MFA code")
	ErrResetTokenInvalid  = errors.New("reset token is invalid, expired or already used")

	ErrInvalidSessionPolicy = errors.New("session policy values out of range")
	// ErrMFARequiredByPolicy: la empresa exige verificacion en dos pasos; no se desactiva.
	ErrMFARequiredByPolicy = errors.New("the tenant requires two-step verification")
	// ErrMFAAlreadyEnabled: el alta obligatoria no rehace un segundo factor que ya existe.
	ErrMFAAlreadyEnabled = errors.New("two-step verification is already enabled")

	// ErrFirstUserConflict: la empresa ya tiene cuentas, o la del reintento no coincide con
	// la que se creo (otro correo, otra contrasena u otra empresa).
	ErrFirstUserConflict = errors.New("tenant already has a different first user")
	// ErrTenantActive: las cuentas de una empresa activa no se retiran en bloque.
	ErrTenantActive = errors.New("tenant is active")
)
