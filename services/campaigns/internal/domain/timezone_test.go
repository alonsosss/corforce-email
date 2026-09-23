package domain

import (
	"errors"
	"testing"
	"time"
)

func mustLocal(t *testing.T, s string) LocalDateTime {
	t.Helper()
	l, err := ParseLocalDateTime(s)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadTimezone(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestParseLocalDateTime(t *testing.T) {
	l := mustLocal(t, " 2026-10-01T09:30 ")
	if l.String() != "2026-10-01T09:30" || l.Wall() != time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC) {
		t.Fatalf("hora de pared: %s %v", l, l.Wall())
	}
	for _, bad := range []string{"", "2026-10-01", "2026-10-01T09:30:00", "2026-10-01T09:30Z", "2026-13-01T09:00", "2026-02-30T09:00", "09:00"} {
		if _, err := ParseLocalDateTime(bad); !errors.Is(err, ErrInvalidCampaign) {
			t.Errorf("%q: se esperaba error de validacion, err = %v", bad, err)
		}
	}
	if got := LocalDateTimeFromWall(time.Date(2026, 3, 8, 2, 30, 59, 7, time.FixedZone("x", 3600))); got.String() != "2026-03-08T02:30" {
		t.Fatalf("la hora guardada se lee por sus campos: %s", got)
	}
}

func TestLoadTimezone(t *testing.T) {
	for _, ok := range []string{"America/Lima", "Europe/Madrid", "UTC", "Asia/Kathmandu", "America/Argentina/Buenos_Aires", "Etc/GMT+5"} {
		if _, err := LoadTimezone(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus", "../../etc/passwd", "/etc/localtime", "America//Lima", "a/b/c/d"} {
		if _, err := LoadTimezone(bad); !errors.Is(err, ErrInvalidCampaign) {
			t.Errorf("%q: se esperaba error de validacion, err = %v", bad, err)
		}
	}
	a := mustZone(t, "Europe/Madrid")
	b := mustZone(t, "Europe/Madrid")
	if a != b {
		t.Fatal("las zonas cargadas se reutilizan")
	}
}

// Horas normales, zonas con desfases de media hora y tres cuartos, y los extremos.
func TestLocalDateTimeIn(t *testing.T) {
	cases := []struct {
		zone, local, want string
	}{
		{"America/Lima", "2026-10-01T09:00", "2026-10-01T14:00:00Z"},
		{"Europe/Madrid", "2026-10-01T09:00", "2026-10-01T07:00:00Z"},
		{"Europe/Madrid", "2026-12-01T09:00", "2026-12-01T08:00:00Z"},
		{"Asia/Kolkata", "2026-10-01T09:00", "2026-10-01T03:30:00Z"},
		{"Asia/Kathmandu", "2026-10-01T09:00", "2026-10-01T03:15:00Z"},
		{"Pacific/Kiritimati", "2026-10-01T09:00", "2026-09-30T19:00:00Z"},
		{"Pacific/Pago_Pago", "2026-10-01T09:00", "2026-10-01T20:00:00Z"},
		{"UTC", "2026-10-01T09:00", "2026-10-01T09:00:00Z"},
		// Hemisferio sur: Sydney esta en horario de verano en diciembre.
		{"Australia/Sydney", "2026-12-01T09:00", "2026-11-30T22:00:00Z"},
		{"Australia/Sydney", "2026-07-01T09:00", "2026-06-30T23:00:00Z"},
	}
	for _, tc := range cases {
		got := mustLocal(t, tc.local).In(mustZone(t, tc.zone)).UTC().Format(time.RFC3339)
		if got != tc.want {
			t.Errorf("%s en %s = %s, quiero %s", tc.local, tc.zone, got, tc.want)
		}
	}
}

// Cambios de hora: una hora que no existe se desplaza la duracion del salto; una que
// existe dos veces toma la primera.
func TestLocalDateTimeInAcrossDST(t *testing.T) {
	cases := []struct {
		name, zone, local, want string
	}{
		{"salto de primavera en Nueva York", "America/New_York", "2026-03-08T02:30", "2026-03-08T07:30:00Z"},
		{"justo antes del salto", "America/New_York", "2026-03-08T01:59", "2026-03-08T06:59:00Z"},
		{"justo despues del salto", "America/New_York", "2026-03-08T03:00", "2026-03-08T07:00:00Z"},
		{"hora repetida en otono en Nueva York", "America/New_York", "2026-11-01T01:30", "2026-11-01T05:30:00Z"},
		{"salto de primavera en Madrid", "Europe/Madrid", "2026-03-29T02:30", "2026-03-29T01:30:00Z"},
		{"hora repetida en otono en Madrid", "Europe/Madrid", "2026-10-25T02:30", "2026-10-25T00:30:00Z"},
		{"salto de primavera en Sydney", "Australia/Sydney", "2026-10-04T02:30", "2026-10-03T16:30:00Z"},
		{"hora repetida en Sydney", "Australia/Sydney", "2026-04-05T02:30", "2026-04-04T15:30:00Z"},
		// Lord Howe adelanta solo media hora.
		{"salto de media hora en Lord Howe", "Australia/Lord_Howe", "2026-10-04T02:15", "2026-10-03T15:45:00Z"},
		{"dia siguiente al cambio", "America/New_York", "2026-03-09T09:00", "2026-03-09T13:00:00Z"},
	}
	for _, tc := range cases {
		loc := mustZone(t, tc.zone)
		l := mustLocal(t, tc.local)
		got := l.In(loc)
		if got.UTC().Format(time.RFC3339) != tc.want {
			t.Errorf("%s: %s = %s, quiero %s", tc.name, tc.local, got.UTC().Format(time.RFC3339), tc.want)
		}
		if again := l.In(loc); !again.Equal(got) {
			t.Errorf("%s: el resultado no es determinista", tc.name)
		}
	}
}

func TestEarliestPrecedesEveryZone(t *testing.T) {
	l := mustLocal(t, "2026-10-01T09:00")
	for _, z := range []string{"Pacific/Kiritimati", "Pacific/Auckland", "Asia/Tokyo", "Europe/Madrid", "America/Lima", "Pacific/Pago_Pago"} {
		if l.In(mustZone(t, z)).Before(l.Earliest()) {
			t.Errorf("%s llega antes del primer instante", z)
		}
	}
	if !l.In(mustZone(t, "Pacific/Kiritimati")).Equal(l.Earliest()) {
		t.Fatal("Kiritimati marca la hora en el primer instante")
	}
}

func TestTimezoneDeliveryTarget(t *testing.T) {
	d := TimezoneDelivery{LocalSendAt: mustLocal(t, "2026-10-01T09:00"), FallbackTimezone: "America/New_York"}
	fallback := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"Europe/Madrid": time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC),
		"America/Lima":  time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC),
		"":              fallback,
		"Mars/Olympus":  fallback,
		"Local":         fallback,
	}
	for zone, want := range cases {
		if got := d.Target(zone); !got.Equal(want) {
			t.Errorf("%q: %s, quiero %s", zone, got.UTC(), want)
		}
	}
	broken := TimezoneDelivery{LocalSendAt: mustLocal(t, "2026-10-01T09:00"), FallbackTimezone: "Mars/Olympus"}
	if got := broken.Target(""); !got.Equal(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("una zona de respaldo ilegible cae en UTC: %s", got)
	}
}
