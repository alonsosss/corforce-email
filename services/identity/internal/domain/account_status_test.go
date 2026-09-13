package domain

import (
	"errors"
	"testing"
	"time"
)

var statusNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func statusAt(d time.Duration) *time.Time {
	v := statusNow.Add(d)
	return &v
}

// El bloqueo por intentos es temporal: vencido, o sin fecha, la cuenta cuenta como active.
// inactive y pending no dependen de ninguna fecha, y un estado desconocido no abre sesion.
func TestElBloqueoEsTemporalYLoDemasNo(t *testing.T) {
	cases := []struct {
		name      string
		status    UserStatus
		until     *time.Time
		locked    bool
		effective UserStatus
		err       error
	}{
		{"activa", UserStatusActive, nil, false, UserStatusActive, nil},
		{"activa con la fecha de un bloqueo anterior", UserStatusActive, statusAt(time.Hour), false, UserStatusActive, nil},
		{"bloqueo vigente", UserStatusLocked, statusAt(time.Second), true, UserStatusLocked, ErrAccountLocked},
		{"bloqueo que vence justo ahora", UserStatusLocked, statusAt(0), false, UserStatusActive, nil},
		{"bloqueo vencido", UserStatusLocked, statusAt(-time.Minute), false, UserStatusActive, nil},
		{"bloqueada sin fecha", UserStatusLocked, nil, false, UserStatusActive, nil},
		{"inactiva", UserStatusInactive, nil, false, UserStatusInactive, ErrAccountInactive},
		{"inactiva con fecha de bloqueo", UserStatusInactive, statusAt(time.Hour), false, UserStatusInactive, ErrAccountInactive},
		{"pendiente", UserStatusPending, nil, false, UserStatusPending, ErrAccountInactive},
		{"estado desconocido", UserStatus("suspended"), nil, false, UserStatus("suspended"), ErrAccountInactive},
	}
	for _, c := range cases {
		u := &User{Status: c.status, LockedUntil: c.until}
		if got := u.LockActive(statusNow); got != c.locked {
			t.Errorf("%s: LockActive = %v, se esperaba %v", c.name, got, c.locked)
		}
		if got := u.EffectiveStatus(statusNow); got != c.effective {
			t.Errorf("%s: EffectiveStatus = %q, se esperaba %q", c.name, got, c.effective)
		}
		if err := u.SessionAllowed(statusNow); !errors.Is(err, c.err) {
			t.Errorf("%s: SessionAllowed = %v, se esperaba %v", c.name, err, c.err)
		}
	}
}

// La misma cuenta deja de estar bloqueada en el instante en que vence el plazo, sin que nada
// escriba en ella.
func TestElBloqueoVenceConElReloj(t *testing.T) {
	until := statusNow.Add(30 * time.Minute)
	u := &User{Status: UserStatusLocked, LockedUntil: &until}
	if err := u.SessionAllowed(until.Add(-time.Nanosecond)); !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("un instante antes del plazo: %v, se esperaba ErrAccountLocked", err)
	}
	if err := u.SessionAllowed(until); err != nil {
		t.Fatalf("al vencer el plazo: %v, se esperaba sesion permitida", err)
	}
	if u.Status != UserStatusLocked {
		t.Fatal("decidir el estado no cambia el estado guardado")
	}
}
