package domain

import "errors"

var (
	// ErrInvalidCredentials cubre cualquier rechazo del inicio de sesion: buzon
	// inexistente, contrasena incorrecta, buzon inactivo, sin acceso o frenado. Nunca se
	// distinguen ante el cliente.
	ErrInvalidCredentials = errors.New("usuario o contraseña incorrectos")
	// ErrSessionInvalid es una sesion inexistente, caducada o revocada.
	ErrSessionInvalid = errors.New("sesión inválida o caducada")
	// ErrUnavailable es un fallo de un servicio del que depende el webmail (mail-auth,
	// Dovecot, Postfix, Redis): el cliente puede reintentar.
	ErrUnavailable = errors.New("servicio de correo no disponible")

	ErrFolderNotFound  = errors.New("carpeta no encontrada")
	ErrMessageNotFound = errors.New("mensaje no encontrado")
	ErrPartNotFound    = errors.New("parte no encontrada")
	ErrTrashNotFound   = errors.New("el buzón no tiene papelera")
	ErrDraftsNotFound  = errors.New("el buzón no tiene carpeta de borradores")
	ErrQuotaExceeded   = errors.New("el buzón superó su cuota")

	ErrFolderProtected    = errors.New("la carpeta es del sistema y no se puede renombrar ni borrar")
	ErrFolderHasChildren  = errors.New("la carpeta tiene subcarpetas: borra primero las subcarpetas")
	ErrFolderExists       = errors.New("ya existe una carpeta con ese nombre")
	ErrFolderNotEmptiable = errors.New("solo se pueden vaciar la papelera y el spam")

	ErrScheduledNotFound   = errors.New("envío programado no encontrado")
	ErrScheduledNotPending = errors.New("el envío programado ya no está pendiente")
	ErrScheduledNotClaimed = errors.New("el envío programado no estaba reclamado")
	ErrScheduledLimit      = errors.New("el buzón alcanzó el máximo de envíos programados pendientes")

	// ErrPreconditionFailed es una edicion sobre una version que otro cliente ya cambio (If-Match).
	ErrPreconditionFailed = errors.New("el recurso cambió desde que se leyó")
	// ErrImportTooLarge es un fichero de contactos por encima del tope del webmail.
	ErrImportTooLarge = errors.New("el fichero supera el tamaño máximo de importación")

	ErrTooManyRecipients  = errors.New("demasiados destinatarios")
	ErrMessageTooLarge    = errors.New("el mensaje supera el tamaño máximo")
	ErrPartTooLarge       = errors.New("la parte supera el tamaño máximo de lectura")
	ErrSenderNotAllowed   = errors.New("el remitente no pertenece al buzón")
	ErrMessageRejected    = errors.New("el servidor de correo rechazó el mensaje")
	ErrAttachmentInfected = errors.New("un adjunto contiene malware")
	ErrScanUnavailable    = errors.New("no se pudo analizar un adjunto")

	// ErrSendInProgress es otra peticion con la misma clave de idempotencia que aun no
	// termino.
	ErrSendInProgress = errors.New("ese envío ya está en curso")
	// ErrComposeBusy es un envio o borrador que no cabe ahora: el servicio acota cuantos
	// mensajes compone a la vez para no quedarse sin memoria.
	ErrComposeBusy = errors.New("hay demasiados envíos en curso; vuelve a intentarlo en unos segundos")
	// ErrDeliveryUncertain es un envio cuya respuesta final de Postfix se perdio: el mensaje
	// pudo quedar en cola. Con la misma clave no se vuelve a intentar.
	ErrDeliveryUncertain = errors.New("no se pudo confirmar si el servidor de correo aceptó el mensaje")
	// ErrIdempotencyKeyReused es una clave de idempotencia que ya se uso con otro mensaje.
	ErrIdempotencyKeyReused = errors.New("la clave de idempotencia ya se usó con otro mensaje")
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
