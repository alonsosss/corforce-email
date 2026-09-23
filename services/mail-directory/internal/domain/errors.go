package domain

import "errors"

var (
	ErrNotFound = errors.New("recurso no encontrado")
	// ErrAlreadyExists cubre las restricciones UNIQUE de la celda: un dominio, una
	// direccion o un destino ya registrados, por esta empresa o por otra.
	ErrAlreadyExists = errors.New("ya existe un registro con ese valor")

	ErrInvalidDomainName  = errors.New("nombre de dominio no valido")
	ErrInvalidLocalPart   = errors.New("parte local no valida: solo minusculas, digitos y . _ + -")
	ErrInvalidEmail       = errors.New("direccion de correo no valida")
	ErrInvalidAddress     = errors.New("la direccion debe ser un correo completo o @dominio")
	ErrInvalidGoto        = errors.New("goto debe ser una lista de correos separados por comas")
	ErrPasswordTooShort   = errors.New("la contrasena debe tener al menos 12 caracteres")
	ErrPasswordTooLong    = errors.New("la contrasena supera la longitud maxima")
	ErrInvalidActive      = errors.New("active debe ser 0, 1 o 2")
	ErrInvalidKind        = errors.New("kind no valido")
	ErrInvalidTLSPolicy   = errors.New("politica TLS no valida")
	ErrInvalidBCCType     = errors.New("type debe ser sender o rcpt")
	ErrInvalidHostname    = errors.New("hostname no valido")
	ErrInvalidLimit       = errors.New("los limites y cuotas no admiten valores negativos")
	ErrSieveEmpty         = errors.New("el script sieve no puede estar vacio")
	ErrSieveTooLarge      = errors.New("el script sieve supera los 64 KiB")
	ErrValidityRequired   = errors.New("indique valid_until o permanent")
	ErrNothingToUpdate    = errors.New("la peticion no trae ningun campo que actualizar")
	ErrNameRequired       = errors.New("name es obligatorio")
	ErrWildcardNeedsExtnl = errors.New("send_as * exige external = true")
	ErrSearchTooLong      = errors.New("search supera la longitud maxima")
	ErrTooManyIDs         = errors.New("la consulta supera el maximo de identificadores")
	ErrInvalidID          = errors.New("identificador de buzon no valido")

	// ErrDomainNotOwned: el dominio no pertenece a la empresa de la peticion (o no existe).
	ErrDomainNotOwned = errors.New("el dominio no pertenece a la empresa")
	// ErrMailboxNotOwned: el destino no es un buzon de la empresa.
	ErrMailboxNotOwned   = errors.New("el buzon no pertenece a la empresa")
	ErrRelayhostNotOwned = errors.New("el relayhost no pertenece a la empresa")
	// ErrDomainInUse: no se borra un dominio con buzones, aliases o dominios alias.
	ErrDomainInUse = errors.New("el dominio tiene buzones, aliases o dominios alias")
	// ErrDomainIsOwnDomain: un dominio propio no puede ser a la vez dominio alias.
	ErrDomainIsOwnDomain = errors.New("el dominio ya esta registrado como dominio propio")
	// ErrActivationNotAllowed: activar un dominio es tarea del servicio que lo verifica.
	ErrActivationNotAllowed = errors.New("la activacion la realiza la verificacion del dominio")

	ErrMaxMailboxesReached = errors.New("el dominio alcanzo su maximo de buzones")
	ErrMaxAliasesReached   = errors.New("el dominio alcanzo su maximo de aliases")
	// ErrMaxAppPasswordsReached: el buzon ya tiene el maximo de contrasenas de aplicacion.
	ErrMaxAppPasswordsReached = errors.New("el buzon alcanzo su maximo de contrasenas de aplicacion")
	ErrQuotaExceedsMax        = errors.New("la cuota supera el maximo por buzon del dominio")
	ErrDomainQuotaExceeded    = errors.New("la suma de cuotas supera la cuota del dominio")
	// ErrAddressTaken: la direccion ya la usa un buzon, un alias o un alias temporal.
	ErrAddressTaken = errors.New("la direccion ya esta en uso")
	// ErrAddressRecentlyDeleted: un buzon con esa direccion se borro hace poco y Dovecot aun no retiro
	// su maildir del disco; crearlo ahora heredaria el correo del titular anterior.
	ErrAddressRecentlyDeleted = errors.New("la direccion se dio de baja hace poco y su buzon en disco esta en retirada: reintente en unos minutos")

	// ErrPlatformOnly: las rutas de plataforma (sin empresa) solo las administra quien
	// opera la plataforma.
	ErrPlatformOnly = errors.New("solo el operador de la plataforma administra este recurso")
)

var (
	ErrVacationMessageRequired = errors.New("la respuesta automatica necesita un mensaje")
	ErrVacationMessageInvalid  = errors.New("el mensaje de la respuesta automatica tiene caracteres no validos o supera los 8192")
	ErrVacationSubjectInvalid  = errors.New("el asunto de la respuesta automatica tiene caracteres no validos o supera los 200")
	ErrVacationInterval        = errors.New("interval_days debe estar entre 1 y 30")
	ErrVacationWindow          = errors.New("ends_on no puede ser anterior a starts_on")
	ErrVacationDate            = errors.New("las fechas de la respuesta automatica se dan como AAAA-MM-DD")
)

var (
	ErrInvalidMTASTSMode = errors.New("mode debe ser none, testing o enforce")
	// ErrMTASTSTransition: se entra a enforce y se sale de el por testing.
	ErrMTASTSTransition = errors.New("la politica MTA-STS pasa por testing para entrar o salir de enforce")
	// ErrMTASTSDomainNotActive: enforce solo se admite en un dominio verificado y activo.
	ErrMTASTSDomainNotActive = errors.New("enforce exige un dominio verificado y activo")
	// ErrMTASTSMXMismatch: los MX publicados del dominio no son solo los de la plataforma; con
	// enforce los remitentes no entregarian el correo.
	ErrMTASTSMXMismatch = errors.New("los MX publicados del dominio no son los de la plataforma")
	// ErrMTASTSDNSUnavailable: no se pudo comprobar el DNS del dominio; no es un fallo del dominio.
	ErrMTASTSDNSUnavailable = errors.New("no se pudo consultar el DNS del dominio; intentalo de nuevo")
)

var (
	// ErrPlanMailboxesExceeded: el plan contratado por la empresa no admite otro buzon. El
	// limite es del plan (billing), no del dominio: ErrMaxMailboxesReached es el del dominio.
	ErrPlanMailboxesExceeded = errors.New("el plan contratado no admite mas buzones")
	// ErrPlanStorageExceeded: la cuota que se pide dejaria el espacio asignado de la empresa
	// por encima del que incluye su plan.
	ErrPlanStorageExceeded = errors.New("el espacio asignado superaria el que incluye el plan contratado")
)

// ErrSubscriptionInactive: la empresa tiene plan pero su suscripcion no esta vigente. No
// crece (ni buzones ni espacio) mientras siga asi; conserva lo que ya tiene. Es el mismo
// criterio con el que billing deniega el envio a una empresa dada de baja (ADR 0010).
var ErrSubscriptionInactive = errors.New("la suscripcion de la empresa no esta vigente")
