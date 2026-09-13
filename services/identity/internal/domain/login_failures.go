package domain

import "time"

// FailedLoginWindow es cuanto se recuerdan los inicios fallidos de un sujeto: pasado ese plazo
// desde el ultimo fallo contado y desde el final de su ultimo bloqueo, el siguiente fallo vuelve
// a contar desde uno. Rige igual para una cuenta que para un correo sin cuenta, y es el plazo tras
// el que se borra el contador de un correo sin cuenta: con reglas distintas, el bloqueo diria
// cual de los dos existe.
const FailedLoginWindow = 24 * time.Hour

// LoginFailures es el contador de inicios fallidos de un sujeto: una cuenta (sus columnas de
// identity.users) o un correo sin cuenta en el ambito en que se busco
// (identity.unknown_login_failures). La base aplica la misma regla en una sola sentencia; las
// pruebas de integracion comparan las dos con estos metodos.
type LoginFailures struct {
	Attempts     int
	LastFailedAt *time.Time
	LockedUntil  *time.Time
}

// LockActive dice si el bloqueo sigue vigente en now. El de una cuenta lo decide ademas su
// estado (User.LockActive).
func (f LoginFailures) LockActive(now time.Time) bool {
	return f.LockedUntil != nil && now.Before(*f.LockedUntil)
}

// Forgotten dice si el contador ya no cuenta en now. Sin ninguna de las dos fechas (una cuenta
// con fallos anteriores a la columna last_failed_login_at) se conserva hasta el siguiente fallo.
func (f LoginFailures) Forgotten(now time.Time) bool {
	ref := f.LastFailedAt
	if f.LockedUntil != nil && (ref == nil || f.LockedUntil.After(*ref)) {
		ref = f.LockedUntil
	}
	return ref != nil && now.Sub(*ref) > FailedLoginWindow
}

// AttemptsAfterFailure es el contador tras un fallo contado en now.
func (f LoginFailures) AttemptsAfterFailure(now time.Time) int {
	if f.Forgotten(now) {
		return 1
	}
	return f.Attempts + 1
}

// RecordFailure aplica un inicio fallido en now con el umbral y el plazo de la politica. Con el
// bloqueo vigente el intento no cuenta y wasLocked es true, igual que una cuenta bloqueada se
// rechaza sin mirar su contrasena. Si no, cuenta y, al llegar al umbral, bloquea hasta
// now + lockout: tambien tras vencer un bloqueo dentro de la ventana, como una cuenta.
func (f LoginFailures) RecordFailure(now time.Time, maxAttempts int, lockout time.Duration) (next LoginFailures, wasLocked bool) {
	if f.LockActive(now) {
		return f, true
	}
	at := now
	next = LoginFailures{Attempts: f.AttemptsAfterFailure(now), LastFailedAt: &at, LockedUntil: f.LockedUntil}
	if next.Attempts >= maxAttempts {
		until := now.Add(lockout)
		next.LockedUntil = &until
	}
	return next, false
}
