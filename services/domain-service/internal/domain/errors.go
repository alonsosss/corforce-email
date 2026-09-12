package domain

import "errors"

var (
	ErrDomainNotFound      = errors.New("dominio no encontrado")
	ErrDomainAlreadyExists = errors.New("el dominio ya esta dado de alta en esta empresa")
	ErrInvalidDomainName   = errors.New("nombre de dominio no valido")
	ErrPlatformDomain      = errors.New("el dominio pertenece a la plataforma y no puede darse de alta")
	ErrInvalidPurpose      = errors.New("purpose debe ser corporate, sending o both")
	ErrInvalidDMARCPolicy  = errors.New("dmarc_policy debe ser none, quarantine o reject")
	ErrNothingToUpdate     = errors.New("no hay nada que actualizar")
	// ErrDomainHasMailboxes lo devuelve mail-directory cuando el dominio todavia tiene
	// buzones: no se puede dar de baja hasta retirarlos.
	ErrDomainHasMailboxes = errors.New("el dominio tiene buzones activos en el directorio")
	// ErrIntegrationUnavailable: mail-directory o mail-security no respondieron y la
	// operacion no puede dejar la fila en un estado que ellos no reflejan.
	ErrIntegrationUnavailable = errors.New("un servicio de correo no esta disponible; reintente")
)
