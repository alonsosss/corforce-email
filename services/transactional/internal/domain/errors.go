package domain

import (
	"errors"
	"fmt"
	"time"
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
	// ErrReputationUnavailable: reputation no respondio a la autorizacion previa. El
	// marketing falla cerrado con este error; el transaccional nunca lo devuelve.
	ErrReputationUnavailable = errors.New("reputation service unavailable")
	// ErrTemplateNotMarketing: la plantilla renderizada no lleva el enlace de baja del
	// mensaje, obligatorio en toda plantilla de marketing.
	ErrTemplateNotMarketing = errors.New("template is not a marketing template")
	// ErrIdempotencyKeyReused: la clave ya identifica una peticion de la otra clase.
	ErrIdempotencyKeyReused = errors.New("idempotency key already used by a request of another class")
	// ErrLaneNotConfigured: la clase del mensaje no tiene proveedor ni limitador propios.
	// Nunca se cae al carril de otra clase: el mensaje espera en la cola.
	ErrLaneNotConfigured = errors.New("sending lane not configured")
)

// Motivos con los que reputation deniega un envio. Los del derecho mensual los decide
// billing (limit_reached, no_subscription...) y son un conjunto abierto; cuando billing no
// da uno, reputation usa plan_denied o plan_limit_exceeded.
const (
	DenySuspended            = "suspended"
	DenyReputationRestricted = "reputation_restricted"
	DenyRateLimited          = "rate_limited"
	DenyPlanDenied           = "plan_denied"
	DenyPlanLimitExceeded    = "plan_limit_exceeded"
)

// SendingDeniedError es una denegacion explicita de reputation. Se respeta siempre, en
// las dos clases: nada se crea ni se encola. Las cuatro categorias son excluyentes.
type SendingDeniedError struct {
	Class  string
	Reason string
	// RetryAfter es la espera que da reputation con rate_limited cuando la cantidad cabe al
	// abrir la siguiente ventana; nil cuando esperar no sirve.
	RetryAfter *time.Duration
}

func (e *SendingDeniedError) Error() string {
	return fmt.Sprintf("sending denied for %s: %s", e.Class, e.Reason)
}

// RateLimited: la empresa supero la tasa de su clase y puede reintentar pasado RetryAfter.
func (e *SendingDeniedError) RateLimited() bool {
	return e.Reason == DenyRateLimited && e.RetryAfter != nil
}

// ExceedsRateWindow: la cantidad pedida no cabe en la ventana de tasa aunque se espere;
// hay que partir el envio.
func (e *SendingDeniedError) ExceedsRateWindow() bool {
	return e.Reason == DenyRateLimited && e.RetryAfter == nil
}

// Restricted: la clase esta suspendida o restringida por su reputacion.
func (e *SendingDeniedError) Restricted() bool {
	return e.Reason == DenySuspended || e.Reason == DenyReputationRestricted
}

// PlanLimit: cualquier otro motivo es del derecho mensual del plan (billing, a traves de
// reputation).
func (e *SendingDeniedError) PlanLimit() bool {
	return e.Reason != DenyRateLimited && !e.Restricted()
}

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
