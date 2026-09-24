package domain

import "errors"

var (
	ErrEntryNotFound      = errors.New("exclusión no encontrada")
	ErrEntryAlreadyExists = errors.New("la dirección ya tiene una exclusión vigente por esa causa en esta empresa")
	ErrInvalidEmail       = errors.New("dirección de correo no válida")
	ErrInvalidReason      = errors.New("reason debe ser hard_bounce, complaint, unsubscribe, manual o invalid")
	ErrManualOnly         = errors.New("por el API público solo se registran exclusiones manuales")
	ErrExpiryInPast       = errors.New("expires_at debe ser una fecha futura")
	ErrTooManyEmails      = errors.New("la lista de direcciones supera el máximo permitido")
	ErrNoEmails           = errors.New("emails no puede estar vacío")
	// ErrUnsubscribeProtected: la baja la pidio la persona. Ningun operador la retira por
	// API; solo un nuevo consentimiento explicito registrado por contacts la levanta.
	ErrUnsubscribeProtected = errors.New("una baja solicitada por la persona no se puede retirar por el API")
)
