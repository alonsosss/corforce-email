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

	// ErrDomainNotOwned: el dominio no pertenece a la empresa de la peticion (o no existe).
	ErrDomainNotOwned = errors.New("el dominio no pertenece a la empresa")
	// ErrMailboxNotOwned: el destino no es un buzon de la empresa.
	ErrMailboxNotOwned = errors.New("el buzon no pertenece a la empresa")
	ErrRelayhostNotOwned = errors.New("el relayhost no pertenece a la empresa")
	// ErrDomainInUse: no se borra un dominio con buzones, aliases o dominios alias.
	ErrDomainInUse = errors.New("el dominio tiene buzones, aliases o dominios alias")
	// ErrDomainIsOwnDomain: un dominio propio no puede ser a la vez dominio alias.
	ErrDomainIsOwnDomain = errors.New("el dominio ya esta registrado como dominio propio")
	// ErrActivationNotAllowed: activar un dominio es tarea del servicio que lo verifica.
	ErrActivationNotAllowed = errors.New("la activacion la realiza la verificacion del dominio")

	ErrMaxMailboxesReached = errors.New("el dominio alcanzo su maximo de buzones")
	ErrMaxAliasesReached   = errors.New("el dominio alcanzo su maximo de aliases")
	ErrQuotaExceedsMax     = errors.New("la cuota supera el maximo por buzon del dominio")
	ErrDomainQuotaExceeded = errors.New("la suma de cuotas supera la cuota del dominio")
	// ErrAddressTaken: la direccion ya la usa un buzon, un alias o un alias temporal.
	ErrAddressTaken = errors.New("la direccion ya esta en uso")

	// ErrPlatformOnly: las rutas de plataforma (sin empresa) solo las administra quien
	// opera la plataforma.
	ErrPlatformOnly = errors.New("solo el operador de la plataforma administra este recurso")
)
