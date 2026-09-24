package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/mailcell"
)

// Token de sesion: "<celda>.<secreto>", con la celda de la instancia que abrio la sesion y un
// secreto de 256 bits en base64url sin relleno. La celda deja al gateway llevar cada peticion a
// la instancia de su celda sin guardar nada; no autoriza nada, porque la sesion solo existe en
// el almacen de esa celda y cada instancia rechaza, sin buscarlo, el token de otra.
const sessionTokenSeparator = "."

var sessionSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ValidCellCode dice si code puede ser la celda de un token de sesion.
func ValidCellCode(code string) bool { return mailcell.ValidCode(code) }

// NewSessionToken compone el token de la celda con su secreto.
func NewSessionToken(cell, secret string) string {
	return cell + sessionTokenSeparator + secret
}

// ParseSessionToken separa la celda del token; ok es false si el token no tiene la forma de uno.
func ParseSessionToken(token string) (cell string, ok bool) {
	cell, secret, found := strings.Cut(token, sessionTokenSeparator)
	return cell, found && ValidCellCode(cell) && sessionSecretPattern.MatchString(secret)
}

// Identity es lo que la verificacion del buzon devuelve al abrir el webmail. TenantID y MailboxID
// son la empresa y el buzon (UUID) que mail-auth devuelve para service webmail; vacios si una
// version anterior de mail-auth no los envia.
type Identity struct {
	Username    string
	DisplayName string
	TenantID    string
	MailboxID   string
}

// Session es una sesion de webmail.
//
// CreatedAt es el instante en que EMPEZO la verificacion de la credencial, no el de su
// alta en el almacen: si la contrasena cambia mientras mail-auth responde, la revocacion
// que llega con ese cambio ya alcanza a esta sesion (ver RevokedBy).
type Session struct {
	Username    string
	DisplayName string
	TenantID    string
	MailboxID   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Mailbox es la empresa y el buzon de la sesion, que exigen la libreta personal y el calendario
// (mail-dav). Una sesion abierta antes de que mail-auth los devolviera no los tiene: ok es false y
// el usuario debe volver a entrar.
func (s Session) Mailbox() (MailboxRef, bool) {
	if !ValidUUID(s.TenantID) || !ValidUUID(s.MailboxID) {
		return MailboxRef{}, false
	}
	return MailboxRef{TenantID: s.TenantID, MailboxID: s.MailboxID, Address: s.Username}, true
}

// RevokedBy indica si una revocacion del buzon en revokedAt alcanza a la sesion: toda
// sesion cuya verificacion empezo antes o en ese instante queda invalidada.
func (s Session) RevokedBy(revokedAt time.Time) bool {
	return !revokedAt.IsZero() && !s.CreatedAt.After(revokedAt)
}

// SessionPolicy acota la vida de una sesion: Idle sin actividad y Max desde el inicio.
type SessionPolicy struct {
	Idle time.Duration
	Max  time.Duration
}

var errInvalidSessionPolicy = errors.New("política de sesión inválida: inactividad y vida máxima deben ser positivas y la inactividad no puede superar la vida máxima")

// Validate rechaza una politica que dejaria sesiones sin limite o incoherentes.
func (p SessionPolicy) Validate() error {
	if p.Idle <= 0 || p.Max <= 0 || p.Idle > p.Max {
		return errInvalidSessionPolicy
	}
	return nil
}

// Open crea la sesion de una identidad recien verificada.
func (p SessionPolicy) Open(id Identity, verificationStartedAt time.Time) Session {
	return Session{
		Username:    id.Username,
		DisplayName: id.DisplayName,
		TenantID:    id.TenantID,
		MailboxID:   id.MailboxID,
		CreatedAt:   verificationStartedAt,
		ExpiresAt:   verificationStartedAt.Add(p.Max),
	}
}

// Remaining es cuanto puede vivir la sesion sin nueva actividad: la inactividad, recortada
// por la vida maxima. ok es false cuando la sesion ya caduco.
func (p SessionPolicy) Remaining(s Session, now time.Time) (time.Duration, bool) {
	left := s.ExpiresAt.Sub(now)
	if left <= 0 {
		return 0, false
	}
	return min(p.Idle, left), true
}

// usernamePattern es el nombre de un buzon propio tal como lo crea mail-directory
// (parte local [a-z0-9._+-], dominio DNS en minusculas).
var usernamePattern = regexp.MustCompile(`^[a-z0-9._+-]{1,64}@[a-z0-9.-]{1,253}$`)

// NormalizeUsername deja el nombre de buzon en minusculas y sin espacios y rechaza lo que
// no puede ser un buzon de la celda. En particular rechaza '*', el separador de usuario
// maestro de Dovecot: un nombre que lo llevara cambiaria a quien se autentica el webmail.
func NormalizeUsername(raw string) (string, bool) {
	u := strings.ToLower(strings.TrimSpace(raw))
	if len(u) > 254 || !usernamePattern.MatchString(u) {
		return "", false
	}
	return u, true
}
