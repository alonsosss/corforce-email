package domain

import (
	"errors"
	"regexp"
	"strings"
)

// Revocacion en los motores: Dovecot guarda cada autenticacion correcta en su cache
// (auth_cache_ttl) y no vuelve a preguntar a mail-auth mientras dura, y una sesion IMAP o POP3
// abierta sigue hasta que el cliente se va. Con cada evento de buzon del directorio este servicio
// vacia la entrada del buzon y, si ya no puede entrar o su credencial cambio, cierra sus sesiones.

// MailboxChange es lo que el directorio anuncia de un buzon.
type MailboxChange string

const (
	MailboxUpdated            MailboxChange = "updated"
	MailboxDeleted            MailboxChange = "deleted"
	MailboxCredentialsChanged MailboxChange = "credentials_changed"
)

// MailboxChangeOf traduce el subject de un evento de buzon; ok es false para los que no revocan
// nada (mail.mailbox.created: un buzon nuevo no tiene credencial vieja que retirar).
func MailboxChangeOf(subject string) (MailboxChange, bool) {
	switch subject {
	case SubjectMailboxUpdated:
		return MailboxUpdated, true
	case SubjectMailboxDeleted:
		return MailboxDeleted, true
	case SubjectMailboxCredentialsChanged:
		return MailboxCredentialsChanged, true
	}
	return "", false
}

// SessionAction es lo que se hace en Dovecot con un buzon.
type SessionAction string

const (
	// SessionFlush vacia su cache de autenticacion: la siguiente autenticacion vuelve a mail-auth,
	// que decide con el estado actual. No toca sus sesiones abiertas.
	SessionFlush SessionAction = "flush"
	// SessionKick vacia su cache y ademas cierra sus sesiones abiertas.
	SessionKick SessionAction = "kick"
)

// SessionActions son todas las acciones, para que sus series nazcan a cero.
func SessionActions() []SessionAction { return []SessionAction{SessionFlush, SessionKick} }

// mailboxLoginActive: solo active = 1 inicia sesion (mail-auth); 2 solo recibe y 0 esta apagado.
const mailboxLoginActive int16 = 1

// SessionActionFor decide con el estado real del buzon en el directorio (current, nil si ya no
// esta), no con lo que diga el evento:
//   - borrado o credencial cambiada: cierra sus sesiones. Una sesion no dice con que credencial
//     entro, y el cliente con la credencial vigente vuelve a entrar solo.
//   - actualizado y ya sin inicio de sesion (apagado, solo recepcion o borrado despues): igual.
//   - actualizado y activo (cuota, nombre visible, TLS, relayhost...): solo vacia la cache, sin
//     echar a nadie. Asi un protocolo retirado (imap_access y los demas) deja de valer en la
//     siguiente autenticacion, que mail-auth vuelve a comprobar.
func SessionActionFor(change MailboxChange, current *Mailbox) SessionAction {
	if change == MailboxDeleted || change == MailboxCredentialsChanged {
		return SessionKick
	}
	if current == nil || current.Active != mailboxLoginActive {
		return SessionKick
	}
	return SessionFlush
}

// sessionLocalPart es la parte local que mail-directory admite en un buzon.
var sessionLocalPart = regexp.MustCompile(`^[a-z0-9._+-]{1,64}$`)

// NormalizeSessionUser devuelve el nombre del buzon tal como lo guarda Dovecot (en minusculas,
// auth_username_format) y rechaza cualquier otra forma: la mascara de doveadm kick admite
// comodines (* y ?), y un nombre con ellos echaria a otros buzones.
func NormalizeSessionUser(raw string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(raw))
	local, domainName, ok := SplitAddress(u)
	if !ok || !sessionLocalPart.MatchString(local) || ValidateDomainName(domainName) != nil {
		return "", newValidation("username no es un buzon valido")
	}
	return u, nil
}

// SessionRevocationFailure es el motivo por el que una revocacion no se aplico.
type SessionRevocationFailure string

const (
	// RevocationUnreachable: Dovecot no responde (red, plazo, error 5xx).
	RevocationUnreachable SessionRevocationFailure = "unreachable"
	// RevocationRejected: Dovecot rechaza la llamada (clave, orden no permitida o certificado).
	RevocationRejected SessionRevocationFailure = "rejected"
	// RevocationCommand: la orden fallo dentro de Dovecot o su respuesta no se entiende.
	RevocationCommand SessionRevocationFailure = "command"
	// RevocationDirectory: no se pudo leer el estado del buzon en la base de la celda.
	RevocationDirectory SessionRevocationFailure = "directory"
)

// SessionRevocationFailures son todos los motivos, para que sus series nazcan a cero.
func SessionRevocationFailures() []SessionRevocationFailure {
	return []SessionRevocationFailure{RevocationUnreachable, RevocationRejected, RevocationCommand, RevocationDirectory}
}

// RevocationFailureOf clasifica el error de los motores.
func RevocationFailureOf(err error) SessionRevocationFailure {
	switch {
	case errors.Is(err, ErrEngineUnreachable):
		return RevocationUnreachable
	case errors.Is(err, ErrEngineRejected):
		return RevocationRejected
	default:
		return RevocationCommand
	}
}
