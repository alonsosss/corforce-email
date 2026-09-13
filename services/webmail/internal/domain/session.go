package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Identity es lo que la verificacion del buzon devuelve al abrir el webmail.
type Identity struct {
	Username    string
	DisplayName string
}

// Session es una sesion de webmail.
//
// CreatedAt es el instante en que EMPEZO la verificacion de la credencial, no el de su
// alta en el almacen: si la contrasena cambia mientras mail-auth responde, la revocacion
// que llega con ese cambio ya alcanza a esta sesion (ver RevokedBy).
type Session struct {
	Username    string
	DisplayName string
	CreatedAt   time.Time
	ExpiresAt   time.Time
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

var errInvalidSessionPolicy = errors.New("politica de sesion invalida: inactividad y vida maxima deben ser positivas y la inactividad no puede superar la vida maxima")

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
