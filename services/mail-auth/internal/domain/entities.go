package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("not found")

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
	Access              ProtocolAccess
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
// acotar sus datos, y no puede deducir ninguna de las dos del nombre).
type Verification struct {
	Result      Result
	DisplayName string
	Username    string
	TenantID    uuid.UUID
	MailboxID   uuid.UUID
}
