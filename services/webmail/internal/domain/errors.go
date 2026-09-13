package domain

import "errors"

var (
	// ErrInvalidCredentials cubre cualquier rechazo del inicio de sesion: buzon
	// inexistente, contrasena incorrecta, buzon inactivo, sin acceso o frenado. Nunca se
	// distinguen ante el cliente.
	ErrInvalidCredentials = errors.New("usuario o contrasena incorrectos")
	// ErrSessionInvalid es una sesion inexistente, caducada o revocada.
	ErrSessionInvalid = errors.New("sesion invalida o caducada")
	// ErrUnavailable es un fallo de un servicio del que depende el webmail (mail-auth,
	// Dovecot, Postfix, Redis): el cliente puede reintentar.
	ErrUnavailable = errors.New("servicio de correo no disponible")

	ErrFolderNotFound  = errors.New("carpeta no encontrada")
	ErrMessageNotFound = errors.New("mensaje no encontrado")
	ErrPartNotFound    = errors.New("parte no encontrada")
	ErrTrashNotFound   = errors.New("el buzon no tiene papelera")
	ErrDraftsNotFound  = errors.New("el buzon no tiene carpeta de borradores")
	ErrQuotaExceeded   = errors.New("el buzon supero su cuota")

	ErrTooManyRecipients  = errors.New("demasiados destinatarios")
	ErrMessageTooLarge    = errors.New("el mensaje supera el tamano maximo")
	ErrPartTooLarge       = errors.New("la parte supera el tamano maximo de lectura")
	ErrSenderNotAllowed   = errors.New("el remitente no pertenece al buzon")
	ErrMessageRejected    = errors.New("el servidor de correo rechazo el mensaje")
	ErrAttachmentInfected = errors.New("un adjunto contiene malware")
	ErrScanUnavailable    = errors.New("no se pudo analizar un adjunto")
)

// ValidationError es una entrada del cliente que no cumple una regla.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }

// NewValidationError construye un ValidationError desde fuera del dominio (reglas que
// dependen de dos campos de una misma peticion).
func NewValidationError(field, reason string) error { return invalid(field, reason) }

// RecipientRejectedError es un destinatario que el servidor de envio rechazo.
type RecipientRejectedError struct {
	Address string
}

func (e *RecipientRejectedError) Error() string {
	return "destinatario rechazado por el servidor de correo: " + e.Address
}
