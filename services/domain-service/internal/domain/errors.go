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
	// ErrDKIMRotationInProgress: la clave anterior sigue en gracia. Retirarla para rotar otra vez
	// romperia el DKIM del correo que aun esta en cola firmado con ella; si esta comprometida, se
	// revoca.
	ErrDKIMRotationInProgress = errors.New("hay una clave DKIM anterior en su periodo de gracia; espere a que se retire o revoque las claves si estan comprometidas")
	// ErrDKIMSelectorNotCurrent: la revocacion nombra un selector que ya no es el actual del
	// dominio ni el que revoco la ultima revocacion.
	ErrDKIMSelectorNotCurrent = errors.New("el selector indicado no es la clave DKIM actual del dominio; recargue el dominio")
	// ErrDKIMKeysChanged: otra operacion cambio las claves del dominio mientras esta decidia.
	ErrDKIMKeysChanged = errors.New("las claves DKIM del dominio cambiaron durante la operacion; reintente")
	// ErrInvalidRevocationReason: el motivo de una revocacion es obligatorio y texto plano.
	ErrInvalidRevocationReason = errors.New("reason es obligatorio, sin caracteres de control y de 500 caracteres como mucho")
)
