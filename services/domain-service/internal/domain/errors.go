package domain

import "errors"

var (
	// ErrTenantSchemaNotReady: la base de la empresa aun no tiene todas las migraciones del servicio (se esta
	// aprovisionando o falta aplicarlas). El barrido la salta en esa pasada y la reintenta en la siguiente.
	ErrTenantSchemaNotReady = errors.New("el esquema de la empresa aún no está migrado")
	ErrDomainNotFound       = errors.New("dominio no encontrado")
	ErrDomainAlreadyExists  = errors.New("el dominio ya está dado de alta en esta empresa")
	ErrInvalidDomainName    = errors.New("nombre de dominio no válido")
	ErrPlatformDomain       = errors.New("el dominio pertenece a la plataforma y no puede darse de alta")
	ErrPublicSuffixDomain   = errors.New("el nombre es un sufijo público (como com.pe o co.uk) y no un dominio registrable; indique el dominio de la empresa")
	// ErrInvalidPlatformHostname: MAIL_HOSTNAME no es un nombre DNS valido o es un sufijo publico.
	ErrInvalidPlatformHostname = errors.New("el hostname de la plataforma no es válido o es un sufijo público")
	// ErrInvalidReportAddress: la direccion de informes de un registro TXT no es una sola direccion valida.
	ErrInvalidReportAddress = errors.New("la dirección de informes debe ser un único correo válido")
	ErrInvalidPurpose       = errors.New("purpose debe ser corporate, sending o both")
	ErrInvalidDMARCPolicy   = errors.New("dmarc_policy debe ser none, quarantine o reject")
	ErrNothingToUpdate      = errors.New("no hay nada que actualizar")
	// ErrDomainHasMailboxes lo devuelve mail-directory cuando el dominio todavia tiene
	// buzones: no se puede dar de baja hasta retirarlos.
	ErrDomainHasMailboxes = errors.New("el dominio tiene buzones activos en el directorio")
	// ErrIntegrationUnavailable: mail-directory o mail-security no respondieron y la
	// operacion no puede dejar la fila en un estado que ellos no reflejan.
	ErrIntegrationUnavailable = errors.New("un servicio de correo no está disponible; reintente")
	// ErrDomainClaimedElsewhere: el indice global de dominios de organization tiene el dominio
	// activo para otra empresa. No se activa en el directorio de la celda de esta.
	ErrDomainClaimedElsewhere = errors.New("el dominio ya está activo en otra empresa de la plataforma")
	// ErrTenantBeingRemoved: la empresa tiene la baja en curso o ya esta dada de baja en su celda.
	// organization no le deja reclamar dominios y mail-directory no le activa ninguno.
	ErrTenantBeingRemoved = errors.New("la empresa se está dando de baja: sus dominios no se activan")
	// ErrDKIMRotationInProgress: la clave anterior sigue en gracia. Retirarla para rotar otra vez
	// romperia el DKIM del correo que aun esta en cola firmado con ella; si esta comprometida, se
	// revoca.
	ErrDKIMRotationInProgress = errors.New("hay una clave DKIM anterior en su periodo de gracia; espere a que se retire o revoque las claves si están comprometidas")
	// ErrDKIMSelectorNotCurrent: la revocacion nombra un selector que ya no es el actual del
	// dominio ni el que revoco la ultima revocacion.
	ErrDKIMSelectorNotCurrent = errors.New("el selector indicado no es la clave DKIM actual del dominio; recargue el dominio")
	// ErrDKIMKeysChanged: otra operacion cambio las claves del dominio mientras esta decidia.
	ErrDKIMKeysChanged = errors.New("las claves DKIM del dominio cambiaron durante la operación; reintente")
	// ErrSESIdentityNotFound y ErrSESIdentityExists: Amazon SES no tiene, o ya tiene, la identidad.
	ErrSESIdentityNotFound = errors.New("amazon ses no tiene la identidad del dominio")
	ErrSESIdentityExists   = errors.New("amazon ses ya tiene la identidad del dominio")
	// ErrSESIdentityOwnedElsewhere: la identidad de SES del dominio lleva la etiqueta de otra empresa.
	ErrSESIdentityOwnedElsewhere = errors.New("la identidad de amazon ses del dominio es de otra empresa de la plataforma")
	// ErrInvalidRevocationReason: el motivo de una revocacion es obligatorio y texto plano.
	ErrInvalidRevocationReason = errors.New("reason es obligatorio, sin caracteres de control y de 500 caracteres como mucho")
)

// Publicacion automatica del DNS en el proveedor de la empresa. Ningun error lleva el token ni el
// mensaje del proveedor, que puede repetir datos de la peticion.
var (
	ErrUnsupportedDNSProvider  = errors.New("proveedor DNS no admitido")
	ErrInvalidDNSMode          = errors.New("dns_mode debe ser manual o el nombre de un proveedor DNS admitido")
	ErrInvalidDNSProviderToken = errors.New("el token de API no tiene un formato válido")
	ErrInvalidRecordKind       = errors.New("replace solo admite tipos de registro del dominio")
	// ErrDNSProviderNotConnected: la empresa no tiene conectado el proveedor.
	ErrDNSProviderNotConnected = errors.New("la empresa no tiene conectado este proveedor DNS")
	// ErrDNSModeManual: el dominio publica su DNS a mano; se publica solo en modo automatico.
	ErrDNSModeManual = errors.New("el dominio publica su DNS a mano; cambie a publicación automática antes de publicar")
	// ErrDNSProviderTokenInvalid: el proveedor no acepta el token (inexistente, caducado, revocado
	// o no activo).
	ErrDNSProviderTokenInvalid = errors.New("el proveedor DNS no acepta el token: no existe, caducó o no está activo")
	// ErrDNSProviderPermissionDenied: el token es valido pero no tiene permiso para lo pedido.
	ErrDNSProviderPermissionDenied = errors.New("el token no tiene permiso para leer las zonas o editar sus registros DNS")
	// ErrDNSProviderNoZones: el token no ve ninguna zona; no sirve para publicar nada.
	ErrDNSProviderNoZones = errors.New("el token no ve ninguna zona DNS")
	// ErrDNSZoneNotFound: ninguna zona visible es la del dominio ni una de la que sea subdominio.
	ErrDNSZoneNotFound = errors.New("el token no ve la zona DNS del dominio")
	// ErrDNSProviderRateLimited: el proveedor limita las peticiones; se reintenta mas tarde.
	ErrDNSProviderRateLimited = errors.New("el proveedor DNS limita las peticiones; reintente en unos minutos")
	// ErrDNSProviderUnavailable: el proveedor no respondio o respondio con un error suyo.
	ErrDNSProviderUnavailable = errors.New("el proveedor DNS no está disponible; reintente")
	// ErrDNSProviderRejected: el proveedor rechazo la peticion por su contenido.
	ErrDNSProviderRejected = errors.New("el proveedor DNS rechazó el registro")
	// ErrDNSRecordExists: el proveedor ya tiene un registro identico (otra publicacion llego antes).
	ErrDNSRecordExists = errors.New("el proveedor DNS ya tiene ese registro")
	// ErrDNSRecordOutsideZone: un registro no pertenece a la zona elegida. No debe ocurrir: se
	// comprueba antes de cada escritura para no escribir jamas fuera de la zona del dominio.
	ErrDNSRecordOutsideZone = errors.New("el registro no pertenece a la zona del dominio")
)
