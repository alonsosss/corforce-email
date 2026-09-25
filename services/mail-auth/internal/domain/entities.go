package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrJobCredentialRejected es el unico motivo de un rechazo de una credencial de trabajo de migracion:
	// quien pregunta no distingue un token mal formado de uno de un trabajo cerrado.
	ErrJobCredentialRejected = errors.New("credencial de trabajo rechazada")
)

// Protocol es la familia de acceso que gobierna un flag *_access del buzon y de la
// contrasena de aplicacion. Dovecot envia el nombre del servicio que atiende la
// conexion; varios servicios comparten flag (submission y lmtp se autorizan con smtp).
type Protocol string

const (
	ProtocolIMAP  Protocol = "imap"
	ProtocolPOP3  Protocol = "pop3"
	ProtocolSMTP  Protocol = "smtp"
	ProtocolSieve Protocol = "sieve"
	// ProtocolDAV es mail-dav (CardDAV): HTTP Basic con la contrasena principal o una de
	// aplicacion, sin sesion, de modo que cada peticion vuelve a pasar por aqui.
	ProtocolDAV Protocol = "dav"
	// ProtocolWebmail es el webmail de la plataforma. El buzon no tiene flag propio: el
	// webmail lee por IMAP y envia por SMTP con la credencial maestra de Dovecot, que no
	// vuelve a pasar por aqui, asi que exige los DOS flags. Si bastara imap_access, un
	// buzon sin smtp_access enviaria desde el webmail saltandose su flag.
	ProtocolWebmail Protocol = "webmail"
)

// AcceptsAppPasswords indica si el protocolo admite contrasenas de aplicacion. Son
// credenciales acotadas a un cliente (movil, escritorio); el webmail abre una sesion de
// navegador que lee y envia, y solo entra con la contrasena principal.
func (p Protocol) AcceptsAppPasswords() bool { return p != ProtocolWebmail }

// AuthenticatesEachRequest indica si el protocolo verifica la credencial en cada peticion en vez de abrir una
// sesion (HTTP Basic de mail-dav): un solo cliente produce decenas de verificaciones por minuto, y dejar
// un registro de inicio por cada una llenaria sasl_logins de filas que no dicen nada nuevo.
func (p Protocol) AuthenticatesEachRequest() bool { return p == ProtocolDAV }

// serviceProtocols traduce el campo service de passwd-verify.lua a su flag. Un servicio
// que no figure aqui se deniega: es preferible negar un protocolo nuevo a autorizarlo
// con el flag equivocado.
var serviceProtocols = map[string]Protocol{
	"imap":        ProtocolIMAP,
	"pop3":        ProtocolPOP3,
	"smtp":        ProtocolSMTP,
	"submission":  ProtocolSMTP,
	"lmtp":        ProtocolSMTP,
	"sieve":       ProtocolSieve,
	"managesieve": ProtocolSieve,
	"dav":         ProtocolDAV,
	"webmail":     ProtocolWebmail,
}

// ServiceMigration es el servicio con el que migration-verify.lua pregunta si la contrasena que recibe
// Dovecot es la credencial de destino de un trabajo de migracion (mail-migration). No es un protocolo del
// buzon ni tiene flag *_access: la credencial existe solo mientras dura el trabajo y abre solo el buzon
// destino, y no pasa por la contrasena principal ni por las de aplicacion.
const ServiceMigration = "migration"

// IsJobCredentialService indica si la peticion es una credencial de trabajo de migracion.
func IsJobCredentialService(service string) bool {
	return strings.EqualFold(strings.TrimSpace(service), ServiceMigration)
}

// JobCredential es lo que mail-migration sabe de una credencial de trabajo vigente: el buzon al que abre
// y el trabajo al que pertenece.
type JobCredential struct {
	TenantID  uuid.UUID
	MailboxID uuid.UUID
	JobID     uuid.UUID
}

// ProtocolFromService resuelve el flag de un servicio de Dovecot (sin distinguir
// mayusculas). ok es false para un servicio desconocido.
func ProtocolFromService(service string) (Protocol, bool) {
	p, ok := serviceProtocols[strings.ToLower(strings.TrimSpace(service))]
	return p, ok
}

// Estados de mail.mailboxes.active. El tri-estado es deliberado: un buzon dado de baja
// sigue recibiendo (2) mientras se decide que hacer con su correo, pero no inicia sesion.
const (
	MailboxInactive    int16 = 0
	MailboxActive      int16 = 1
	MailboxReceiveOnly int16 = 2
)

// Mailbox es la proyeccion de mail.mailboxes que necesita la verificacion: identidad,
// hash y politica de acceso por protocolo. No lleva cuota ni rutas de entrega.
type Mailbox struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	Username            string
	DisplayName         string
	PasswordHash        string
	Active              int16
	ForcePasswordUpdate bool
	// MFAEnabled: el buzon tiene la verificacion en dos pasos activa (mail-directory). Con ella la
	// contrasena principal solo abre el webmail, que pide el segundo paso; los programas de correo
	// entran con contrasenas de aplicacion.
	MFAEnabled bool
	Access     ProtocolAccess
}

// ProtocolAccess son los cinco flags *_access, comunes al buzon y a la contrasena de
// aplicacion.
type ProtocolAccess struct {
	IMAP  bool
	POP3  bool
	SMTP  bool
	Sieve bool
	DAV   bool
}

// Allows indica si el flag del protocolo esta encendido.
func (a ProtocolAccess) Allows(p Protocol) bool {
	switch p {
	case ProtocolIMAP:
		return a.IMAP
	case ProtocolPOP3:
		return a.POP3
	case ProtocolSMTP:
		return a.SMTP
	case ProtocolSieve:
		return a.Sieve
	case ProtocolDAV:
		return a.DAV
	case ProtocolWebmail:
		return a.IMAP && a.SMTP
	}
	return false
}

// CanLogin es la unica lectura valida de active para autenticar: solo el 1 entra.
func (m Mailbox) CanLogin() bool { return m.Active == MailboxActive }

// AppPassword es una contrasena de aplicacion activa del buzon, ya filtrada por el
// protocolo de la peticion.
type AppPassword struct {
	ID           uuid.UUID
	Name         string
	PasswordHash string
}

// Login es una fila de mail.sasl_logins: quien entro, por que servicio, con que
// credencial y desde donde.
type Login struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Username      string
	Service       string
	AppPasswordID *uuid.UUID
	RemoteIP      string
	LoggedAt      time.Time
}

// VerifyRequest es lo que manda passwd-verify.lua, ya validado por el adaptador HTTP.
type VerifyRequest struct {
	Username string
	Password string
	RemoteIP string
	Service  string
}

// Result es el desenlace de una verificacion. Solo ResultOK autoriza; el resto se
// distingue para las metricas y el log, nunca para el cliente, que recibe el mismo 401.
type Result string

const (
	ResultOK             Result = "ok"
	ResultBadPassword    Result = "bad_password"
	ResultInactive       Result = "inactive"
	ResultNoAccess       Result = "no_access"
	ResultThrottled      Result = "throttled"
	ResultUnknownService Result = "unknown_service"
	// ResultForbiddenNetwork es una credencial de trabajo usada desde fuera de la red del ejecutor.
	ResultForbiddenNetwork Result = "forbidden_network"
	// ResultJobCredentialsDisabled es una credencial de trabajo con la funcion sin configurar en mail-auth.
	ResultJobCredentialsDisabled Result = "job_credentials_disabled"
	// ResultMFAAppPasswordRequired es la contrasena principal, correcta, de un buzon con verificacion en
	// dos pasos por un protocolo que no es el webmail: ahi solo entran contrasenas de aplicacion.
	ResultMFAAppPasswordRequired Result = "mfa_app_password_required"
	// ResultError es un fallo interno (base o freno). Dovecot lo ve como contrasena
	// incorrecta; aqui se cuenta aparte para que una caida de la base no parezca una
	// ola de contrasenas erroneas.
	ResultError Result = "error"
)

// Authorized indica si el resultado abre la sesion.
func (r Result) Authorized() bool { return r == ResultOK }

// Verification es el desenlace con lo que se sabe del buzon, que solo se rellena cuando la
// credencial abre la sesion: el nombre visible (el webmail lo usa como nombre del remitente) y
// la identidad canonica (mail-dav necesita la empresa para elegir su base y el buzon para
// acotar sus datos, y no puede deducir ninguna de las dos del nombre; el webmail las guarda en
// su sesion para llamar a mail-dav en nombre del buzon).
type Verification struct {
	Result      Result
	DisplayName string
	Username    string
	TenantID    uuid.UUID
	MailboxID   uuid.UUID
	// MFARequired: la sesion del webmail no se abre hasta superar el segundo paso.
	MFARequired bool
}
