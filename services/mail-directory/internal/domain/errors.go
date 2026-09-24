package domain

import "errors"

var (
	ErrNotFound = errors.New("recurso no encontrado")
	// ErrAlreadyExists cubre las restricciones UNIQUE de la celda: un dominio, una
	// direccion o un destino ya registrados, por esta empresa o por otra.
	ErrAlreadyExists = errors.New("ya existe un registro con ese valor")

	ErrInvalidDomainName  = errors.New("nombre de dominio no válido")
	ErrInvalidLocalPart   = errors.New("parte local no válida: solo minúsculas, dígitos y . _ + -")
	ErrInvalidEmail       = errors.New("dirección de correo no válida")
	ErrInvalidAddress     = errors.New("la dirección debe ser un correo completo o @dominio")
	ErrInvalidGoto        = errors.New("goto debe ser una lista de correos separados por comas")
	ErrPasswordTooShort   = errors.New("la contraseña debe tener al menos 12 caracteres")
	ErrPasswordTooLong    = errors.New("la contraseña supera la longitud máxima")
	ErrInvalidActive      = errors.New("active debe ser 0, 1 o 2")
	ErrInvalidKind        = errors.New("kind no válido")
	ErrInvalidTLSPolicy   = errors.New("política TLS no válida")
	ErrInvalidBCCType     = errors.New("type debe ser sender o rcpt")
	ErrInvalidHostname    = errors.New("hostname no válido")
	ErrInvalidLimit       = errors.New("los límites y cuotas no admiten valores negativos")
	ErrSieveEmpty         = errors.New("el script sieve no puede estar vacío")
	ErrSieveTooLarge      = errors.New("el script sieve supera los 64 KiB")
	ErrValidityRequired   = errors.New("indique valid_until o permanent")
	ErrNothingToUpdate    = errors.New("la petición no trae ningún campo que actualizar")
	ErrNameRequired       = errors.New("name es obligatorio")
	ErrWildcardNeedsExtnl = errors.New("send_as * exige external = true")
	ErrSearchTooLong      = errors.New("search supera la longitud máxima")
	ErrTooManyIDs         = errors.New("la consulta supera el máximo de identificadores")
	ErrInvalidID          = errors.New("identificador de buzón no válido")

	// ErrDomainNotOwned: el dominio no pertenece a la empresa de la peticion (o no existe).
	ErrDomainNotOwned = errors.New("el dominio no pertenece a la empresa")
	// ErrMailboxNotOwned: el destino no es un buzon de la empresa.
	ErrMailboxNotOwned   = errors.New("el buzón no pertenece a la empresa")
	ErrRelayhostNotOwned = errors.New("el relayhost no pertenece a la empresa")
	// ErrDomainInUse: no se borra un dominio con buzones, aliases o dominios alias.
	ErrDomainInUse = errors.New("el dominio tiene buzones, aliases o dominios alias")
	// ErrDomainIsOwnDomain: un dominio propio no puede ser a la vez dominio alias.
	ErrDomainIsOwnDomain = errors.New("el dominio ya está registrado como dominio propio")
	// ErrActivationNotAllowed: activar un dominio es tarea del servicio que lo verifica.
	ErrActivationNotAllowed = errors.New("la activación la realiza la verificación del dominio")

	ErrMaxMailboxesReached = errors.New("el dominio alcanzó su máximo de buzones")
	ErrMaxAliasesReached   = errors.New("el dominio alcanzó su máximo de aliases")
	// ErrMaxAppPasswordsReached: el buzon ya tiene el maximo de contrasenas de aplicacion.
	ErrMaxAppPasswordsReached = errors.New("el buzón alcanzó su máximo de contraseñas de aplicación")
	ErrQuotaExceedsMax        = errors.New("la cuota supera el máximo por buzón del dominio")
	ErrDomainQuotaExceeded    = errors.New("la suma de cuotas supera la cuota del dominio")
	// ErrAddressTaken: la direccion ya la usa un buzon, un alias o un alias temporal.
	ErrAddressTaken = errors.New("la dirección ya está en uso")
	// ErrAddressRecentlyDeleted: un buzon con esa direccion se borro hace poco y Dovecot aun no retiro
	// su maildir del disco; crearlo ahora heredaria el correo del titular anterior.
	ErrAddressRecentlyDeleted = errors.New("la dirección se dio de baja hace poco y su buzón en disco está en retirada: reintente en unos minutos")

	// ErrPlatformOnly: las rutas de plataforma (sin empresa) solo las administra quien
	// opera la plataforma.
	ErrPlatformOnly = errors.New("solo el operador de la plataforma administra este recurso")
)

var (
	ErrVacationMessageRequired = errors.New("la respuesta automática necesita un mensaje")
	ErrVacationMessageInvalid  = errors.New("el mensaje de la respuesta automática tiene caracteres no válidos o supera los 8192")
	ErrVacationSubjectInvalid  = errors.New("el asunto de la respuesta automática tiene caracteres no válidos o supera los 200")
	ErrVacationInterval        = errors.New("interval_days debe estar entre 1 y 30")
	ErrVacationWindow          = errors.New("ends_on no puede ser anterior a starts_on")
	ErrVacationDate            = errors.New("las fechas de la respuesta automática se dan como AAAA-MM-DD")
)

var (
	ErrInvalidMTASTSMode = errors.New("mode debe ser none, testing o enforce")
	// ErrMTASTSTransition: se entra a enforce y se sale de el por testing.
	ErrMTASTSTransition = errors.New("la política MTA-STS pasa por testing para entrar o salir de enforce")
	// ErrMTASTSDomainNotActive: enforce solo se admite en un dominio verificado y activo.
	ErrMTASTSDomainNotActive = errors.New("enforce exige un dominio verificado y activo")
	// ErrMTASTSMXMismatch: los MX publicados del dominio no son solo los de la plataforma; con
	// enforce los remitentes no entregarian el correo.
	ErrMTASTSMXMismatch = errors.New("los MX publicados del dominio no son los de la plataforma")
	// ErrMTASTSDNSUnavailable: no se pudo comprobar el DNS del dominio; no es un fallo del dominio.
	ErrMTASTSDNSUnavailable = errors.New("no se pudo consultar el DNS del dominio; inténtalo de nuevo")
)

var (
	// ErrPlanMailboxesExceeded: el plan contratado por la empresa no admite otro buzon. El
	// limite es del plan (billing), no del dominio: ErrMaxMailboxesReached es el del dominio.
	ErrPlanMailboxesExceeded = errors.New("el plan contratado no admite más buzones")
	// ErrPlanStorageExceeded: la cuota que se pide dejaria el espacio asignado de la empresa
	// por encima del que incluye su plan.
	ErrPlanStorageExceeded = errors.New("el espacio asignado superaría el que incluye el plan contratado")
)

// ErrSubscriptionInactive: la empresa tiene plan pero su suscripcion no esta vigente. No
// crece (ni buzones ni espacio) mientras siga asi; conserva lo que ya tiene. Es el mismo
// criterio con el que billing deniega el envio a una empresa dada de baja (ADR 0010).
var ErrSubscriptionInactive = errors.New("la suscripción de la empresa no está vigente")

// FieldError es un fallo de entrada ligado a un campo del cuerpo (rules[3].conditions[0].value,
// forwarding.addresses[1]): se responde 422 con details.field para que la interfaz lo senale.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Reason }

func fieldErr(field, reason string) *FieldError { return &FieldError{Field: field, Reason: reason} }

var (
	// ErrScheduledSendNotPending: la fila ya no se cambia ni se cancela (se esta enviando o ya salio).
	ErrScheduledSendNotPending = errors.New("el envío programado ya no está pendiente")
	// ErrScheduledSendNotClaimed: se cierra una fila que no esta reclamada (status sending).
	ErrScheduledSendNotClaimed = errors.New("el envío programado no está en curso")
	// ErrScheduledSendLimit: el buzon alcanzo el maximo de envios programados pendientes.
	ErrScheduledSendLimit = errors.New("el buzón alcanzó su máximo de envíos programados pendientes")
)

var (
	// ErrReminderNotPending: el recordatorio ya no se cambia ni se cancela (se esta procesando o ya termino).
	ErrReminderNotPending = errors.New("el recordatorio ya no está pendiente")
	// ErrReminderNotClaimed: se cierra una fila que no esta reclamada (status running).
	ErrReminderNotClaimed = errors.New("el recordatorio no está en curso")
	// ErrReminderLimit: el buzon alcanzo el maximo de recordatorios activos.
	ErrReminderLimit = errors.New("el buzón alcanzó su máximo de recordatorios activos")
	// ErrQuickReplyLimit: el buzon alcanzo el maximo de respuestas rapidas.
	ErrQuickReplyLimit = errors.New("el buzón alcanzó su máximo de respuestas rápidas")
)
