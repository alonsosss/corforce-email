package domain

import "errors"

var (
	ErrEntryNotFound      = errors.New("exclusion no encontrada")
	ErrEntryAlreadyExists = errors.New("la direccion ya esta excluida en esta empresa")
	ErrInvalidEmail       = errors.New("direccion de correo no valida")
	ErrInvalidReason      = errors.New("reason debe ser hard_bounce, complaint, unsubscribe, manual o invalid")
	ErrManualOnly         = errors.New("por el API publico solo se registran exclusiones manuales")
	ErrExpiryInPast       = errors.New("expires_at debe ser una fecha futura")
	ErrTooManyEmails      = errors.New("la lista de direcciones supera el maximo permitido")
	ErrNoEmails           = errors.New("emails no puede estar vacio")
	// ErrUnsubscribeProtected: la baja la pidio la persona. Ningun operador la retira por
	// API; solo un nuevo consentimiento explicito registrado por contacts la levanta.
	ErrUnsubscribeProtected = errors.New("una baja solicitada por la persona no se puede retirar por el API")
)
