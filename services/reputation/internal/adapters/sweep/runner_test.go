package sweep

import (
	"testing"
	"time"
)

func TestNextRun(t *testing.T) {
	at := 5 * time.Minute
	cases := []struct {
		now, want time.Time
	}{
		{time.Date(2026, 9, 12, 0, 1, 0, 0, time.UTC), time.Date(2026, 9, 12, 0, 5, 0, 0, time.UTC)},
		{time.Date(2026, 9, 12, 0, 5, 0, 0, time.UTC), time.Date(2026, 9, 13, 0, 5, 0, 0, time.UTC)},
		{time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC), time.Date(2026, 9, 13, 0, 5, 0, 0, time.UTC)},
		{time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 5, 0, 0, time.UTC)},
		// 02:00 en UTC+3 son las 23:00 UTC del dia anterior.
		{time.Date(2026, 9, 13, 2, 0, 0, 0, time.FixedZone("UTC+3", 3*3600)), time.Date(2026, 9, 13, 0, 5, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := NextRun(c.now, at); !got.Equal(c.want) {
			t.Errorf("NextRun(%s) = %s; se esperaba %s", c.now, got, c.want)
		}
	}
}
