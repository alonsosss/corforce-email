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
	// ErrDomainClaimedElsewhere: el indice global de dominios de organization tiene el dominio
	// activo para otra empresa. No se activa en el directorio de la celda de esta.
	ErrDomainClaimedElsewhere = errors.New("el dominio ya esta activo en otra empresa de la plataforma")
	// ErrTenantBeingRemoved: la empresa tiene la baja en curso o ya esta dada de baja en su celda.
	// organization no le deja reclamar dominios y mail-directory no le activa ninguno.
	ErrTenantBeingRemoved = errors.New("la empresa se esta dando de baja: sus dominios no se activan")
)
