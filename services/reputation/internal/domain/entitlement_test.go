package domain

import (
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }

func TestEntitlementAdmits(t *testing.T) {
	cuota := Entitlement{Allowed: true, HardLimit: true, Limit: i64(100), Remaining: i64(10), Requested: 1}
	porCantidad := Entitlement{Allowed: false, HardLimit: true, Limit: i64(100), Remaining: i64(3), Reason: "quota_exceeded", Requested: 10}
	cases := []struct {
		name        string
		e           Entitlement
		n, consumed int64
		ok          bool
		reason      string
	}{
		{"sin limite", Entitlement{Allowed: true, Requested: 1}, 5000, 0, true, ""},
		{"cabe en el remanente", cuota, 10, 0, true, ""},
		{"no cabe tras lo ya autorizado", cuota, 5, 6, false, ReasonPlanLimitExceeded},
		{"limite blando deja pasar", Entitlement{Allowed: true, HardLimit: false, Remaining: i64(0), Requested: 1}, 10, 0, true, ""},
		{"denegado por el estado del plan", Entitlement{Allowed: false, HardLimit: true, Remaining: i64(50), Reason: "subscription_suspended", Requested: 1}, 1, 0, false, "subscription_suspended"},
		{"denegado sin motivo", Entitlement{Allowed: false, Requested: 1}, 1, 0, false, ReasonPlanDenied},
		{"denegado por cantidad admite una menor", porCantidad, 3, 0, true, ""},
		{"denegado por cantidad sigue denegando la misma", porCantidad, 10, 0, false, "quota_exceeded"},
	}
	for _, c := range cases {
		ok, reason := c.e.Admits(c.n, c.consumed)
		if ok != c.ok || reason != c.reason {
			t.Errorf("%s: ok=%v reason=%q; se esperaba ok=%v reason=%q", c.name, ok, reason, c.ok, c.reason)
		}
	}
}

func TestEntitlementView(t *testing.T) {
	m := Entitlement{Limit: i64(100), Used: 90, Remaining: i64(10)}.View(15)
	if m.Used != 105 || m.Remaining == nil || *m.Remaining != 0 || *m.Limit != 100 {
		t.Fatalf("el remanente no baja de cero: %+v", m)
	}
	if v := (Entitlement{Used: 3}).View(2); v.Limit != nil || v.Remaining != nil || v.Used != 5 {
		t.Fatalf("sin limite: %+v", v)
	}
}

func TestRetryAfter(t *testing.T) {
	at := func(h, m, s, ns int) time.Time { return time.Date(2026, 9, 12, h, m, s, ns, time.UTC) }
	cases := []struct {
		name string
		now  time.Time
		w    Window
		want int64
	}{
		{"media hora", at(10, 30, 0, 0), WindowHour, 1800},
		{"fraccion de segundo se redondea hacia arriba", at(10, 59, 59, 500_000_000), WindowHour, 1},
		{"en el cambio de hora espera la siguiente", at(11, 0, 0, 0), WindowHour, 3600},
		{"ultima hora del dia", at(23, 0, 0, 0), WindowDay, 3600},
		{"dia completo", at(0, 0, 0, 0), WindowDay, 86400},
		{"sin ventana agotada", at(10, 0, 0, 0), WindowNone, 0},
		{"hora local se lleva a UTC", time.Date(2026, 9, 12, 10, 30, 0, 0, time.FixedZone("UTC+3", 3*3600)), WindowHour, 1800},
	}
	for _, c := range cases {
		if got := RetryAfter(c.now, c.w); got != c.want {
			t.Errorf("%s: %d; se esperaba %d", c.name, got, c.want)
		}
	}
}

func TestLimitsFits(t *testing.T) {
	l := Limits{Hourly: 10, Daily: 100}
	if !l.Fits(10, WindowHour) || l.Fits(11, WindowHour) || !l.Fits(100, WindowDay) || l.Fits(101, WindowDay) {
		t.Fatal("Fits compara con el limite de la ventana agotada")
	}
}
