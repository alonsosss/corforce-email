package domain

import (
	"errors"
	"testing"
	"time"
)

func validPolicy() Policy {
	return Policy{
		Thresholds: baseThresholds(),
		WindowDays: 7,
		Defaults: map[Class]Limits{
			ClassTransactional: {Hourly: 2000, Daily: 20000},
			ClassMarketing:     {Hourly: 5000, Daily: 50000},
		},
	}
}

func TestPolicyValidate(t *testing.T) {
	if err := validPolicy().Validate(); err != nil {
		t.Fatalf("la politica de referencia debe ser valida: %v", err)
	}
	mutations := map[string]func(*Policy){
		"warn igual a block":         func(p *Policy) { p.Thresholds.BounceWarn = p.Thresholds.BounceBlock },
		"queja warn mayor que block": func(p *Policy) { p.Thresholds.ComplaintWarn = dec("0.001") },
		"fraccion cero":              func(p *Policy) { p.Thresholds.BounceWarn = dec("0") },
		"fraccion uno":               func(p *Policy) { p.Thresholds.BounceBlock = dec("1") },
		"fraccion negativa":          func(p *Policy) { p.Thresholds.ComplaintWarn = dec("-0.1") },
		"volumen minimo cero":        func(p *Policy) { p.Thresholds.MinVolume = 0 },
		"ventana vacia":              func(p *Policy) { p.WindowDays = 0 },
		"ventana excesiva":           func(p *Policy) { p.WindowDays = MaxWindowDays + 1 },
		"faltan limites de una clase": func(p *Policy) {
			delete(p.Defaults, ClassMarketing)
		},
		"hourly mayor que daily": func(p *Policy) { p.Defaults[ClassTransactional] = Limits{Hourly: 10, Daily: 5} },
		"limite cero":            func(p *Policy) { p.Defaults[ClassMarketing] = Limits{Hourly: 0, Daily: 5} },
		"limite excesivo":        func(p *Policy) { p.Defaults[ClassMarketing] = Limits{Hourly: 1, Daily: MaxLimit + 1} },
	}
	for name, mutate := range mutations {
		p := validPolicy()
		mutate(&p)
		if err := p.Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s: se esperaba ErrInvalidPolicy, hubo %v", name, err)
		}
	}
}

func TestValidateOverrideConLosValoresPorDefecto(t *testing.T) {
	p := validPolicy()
	v := func(n int64) *int64 { return &n }
	ok := []LimitOverride{
		{Class: ClassTransactional},
		{Class: ClassTransactional, Hourly: v(10)},
		{Class: ClassMarketing, Hourly: v(100), Daily: v(100)},
	}
	for _, o := range ok {
		if err := p.ValidateOverride(o); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	bad := []LimitOverride{
		{Class: ClassTransactional, Hourly: v(0)},
		{Class: ClassTransactional, Daily: v(-1)},
		{Class: ClassTransactional, Hourly: v(30000)},
		{Class: ClassMarketing, Hourly: v(10), Daily: v(5)},
		{Class: ClassMarketing, Daily: v(MaxLimit + 1)},
	}
	for _, o := range bad {
		if err := p.ValidateOverride(o); !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("%+v: se esperaba ErrInvalidLimit, hubo %v", o, err)
		}
	}
}

func TestLimitsFor(t *testing.T) {
	p := validPolicy()
	h := int64(7)
	if got := p.LimitsFor(ClassMarketing, nil); got != (Limits{Hourly: 5000, Daily: 50000}) {
		t.Fatalf("sin override: %+v", got)
	}
	if got := p.LimitsFor(ClassMarketing, &LimitOverride{Hourly: &h}); got != (Limits{Hourly: 7, Daily: 50000}) {
		t.Fatalf("override parcial: %+v", got)
	}
}

func TestWindowStartCuentaDiasUTCConHoyIncluido(t *testing.T) {
	p := Policy{WindowDays: 7}
	want := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	if got := p.WindowStart(time.Date(2026, 9, 12, 23, 59, 0, 0, time.UTC)); !got.Equal(want) {
		t.Fatalf("inicio de ventana: %s", got)
	}
	// 01:00 en UTC+3 es todavia el dia 12 en UTC.
	local := time.Date(2026, 9, 13, 1, 0, 0, 0, time.FixedZone("UTC+3", 3*3600))
	if got := p.WindowStart(local); !got.Equal(want) {
		t.Fatalf("la ventana se cuenta en UTC: %s", got)
	}
	if got := (Policy{WindowDays: 1}).WindowStart(time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("ventana de un dia: %s", got)
	}
}

func TestParseClassYEstado(t *testing.T) {
	if c, err := ClassOrDefault(""); err != nil || c != ClassTransactional {
		t.Fatalf("sin clase es transaccional: %s %v", c, err)
	}
	if _, err := ClassOrDefault("newsletter"); !errors.Is(err, ErrInvalidClass) {
		t.Fatalf("clase desconocida: %v", err)
	}
	if ClassMarketing.BillingResource() != "marketing_messages" || ClassTransactional.BillingResource() != "transactional_messages" {
		t.Fatal("recurso de billing")
	}
	if _, err := ParseState("blocked"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("estado desconocido: %v", err)
	}
	if s, err := ParseState("restricted"); err != nil || s != StateRestricted {
		t.Fatalf("estado: %s %v", s, err)
	}
}
