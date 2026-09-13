package sweep

import (
	"testing"
	"time"
)

func TestNextRun(t *testing.T) {
	at := 4 * time.Hour
	lima := time.FixedZone("Lima", -5*3600)
	cases := []struct {
		name      string
		now, want time.Time
	}{
		{"antes de la hora: hoy", time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC), time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)},
		{"a la hora exacta: manana", time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC), time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC)},
		{"despues de la hora: manana", time.Date(2026, 9, 13, 17, 30, 0, 0, time.UTC), time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC)},
		{"la hora es UTC aunque el reloj no lo sea", time.Date(2026, 9, 13, 23, 0, 0, 0, lima), time.Date(2026, 9, 15, 4, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if got := NextRun(tc.now, at); !got.Equal(tc.want) {
			t.Errorf("%s: NextRun(%s) = %s, se esperaba %s", tc.name, tc.now, got, tc.want)
		}
	}
}
