package domain

import (
	"testing"
	"time"
)

var failuresNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time {
	v := failuresNow.Add(d)
	return &v
}

// La ventana se cuenta desde lo ultimo que paso: el ultimo fallo contado o el final del bloqueo.
// Justo en el limite todavia cuenta; sin ninguna fecha (fallos anteriores a la columna) tambien.
func TestLosFallosSeOlvidanTrasLaVentana(t *testing.T) {
	w := FailedLoginWindow
	cases := []struct {
		name string
		f    LoginFailures
		want bool
	}{
		{"sin fechas", LoginFailures{Attempts: 4}, false},
		{"ultimo fallo en el limite", LoginFailures{Attempts: 4, LastFailedAt: at(-w)}, false},
		{"ultimo fallo pasado el limite", LoginFailures{Attempts: 4, LastFailedAt: at(-w - time.Nanosecond)}, true},
		{"el bloqueo termino despues del ultimo fallo", LoginFailures{Attempts: 5, LastFailedAt: at(-w - time.Hour), LockedUntil: at(-w + time.Minute)}, false},
		{"el bloqueo termino antes del ultimo fallo", LoginFailures{Attempts: 5, LastFailedAt: at(-w - time.Second), LockedUntil: at(-2 * w)}, true},
		{"solo con bloqueo", LoginFailures{Attempts: 5, LockedUntil: at(-w - time.Second)}, true},
	}
	for _, c := range cases {
		if got := c.f.Forgotten(failuresNow); got != c.want {
			t.Errorf("%s: olvidado=%v, se esperaba %v", c.name, got, c.want)
		}
		wantNext := c.f.Attempts + 1
		if c.want {
			wantNext = 1
		}
		if got := c.f.AttemptsAfterFailure(failuresNow); got != wantNext {
			t.Errorf("%s: siguiente %d, se esperaba %d", c.name, got, wantNext)
		}
	}
}

// Un fallo cuenta si no hay bloqueo vigente y bloquea al llegar al umbral; durante el bloqueo no
// cuenta ni mueve ninguna fecha, y al vencer el siguiente fallo vuelve a bloquear.
func TestUnFalloBloqueaAlUmbralYNoCuentaDuranteElBloqueo(t *testing.T) {
	const lockout = 30 * time.Minute
	var f LoginFailures
	steps := []struct {
		at         time.Duration
		attempts   int
		wasLocked  bool
		lockedTill *time.Time
	}{
		{0, 1, false, nil},
		{time.Second, 2, false, nil},
		{2 * time.Second, 3, false, at(2*time.Second + lockout)},
		{3 * time.Second, 3, true, at(2*time.Second + lockout)},
		{2*time.Second + lockout, 4, false, at(2*time.Second + 2*lockout)},
	}
	for i, s := range steps {
		next, wasLocked := f.RecordFailure(failuresNow.Add(s.at), 3, lockout)
		if wasLocked != s.wasLocked || next.Attempts != s.attempts ||
			(next.LockedUntil == nil) != (s.lockedTill == nil) || (s.lockedTill != nil && !next.LockedUntil.Equal(*s.lockedTill)) {
			t.Fatalf("paso %d: %+v bloqueado=%v, se esperaba %d intentos, bloqueado=%v hasta %v", i, next, wasLocked, s.attempts, s.wasLocked, s.lockedTill)
		}
		if s.wasLocked && next.LastFailedAt != f.LastFailedAt {
			t.Fatalf("paso %d: un intento bloqueado movio el ultimo fallo", i)
		}
		f = next
	}
	// Con plazo cero el bloqueo nunca esta vigente: cada fallo cuenta.
	var z LoginFailures
	for i := 1; i <= 3; i++ {
		var wasLocked bool
		z, wasLocked = z.RecordFailure(failuresNow, 1, 0)
		if wasLocked || z.Attempts != i {
			t.Fatalf("plazo cero, fallo %d: %+v bloqueado=%v", i, z, wasLocked)
		}
	}
}
