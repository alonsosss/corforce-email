package domain

import (
	"testing"
	"time"
)

var clock = time.Date(2026, 9, 13, 15, 30, 0, 0, time.UTC)

func date(s string) time.Time {
	t, err := time.ParseInLocation(dateLayout, s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRangoPorDefecto(t *testing.T) {
	r, err := ParseRange("", "", clock)
	if err != nil {
		t.Fatal(err)
	}
	if !r.From.Equal(date("2026-08-15")) || !r.To.Equal(date("2026-09-13")) || r.Days() != DefaultRangeDays {
		t.Fatalf("ultimos 30 dias hasta hoy: %s..%s (%d)", FormatDate(r.From), FormatDate(r.To), r.Days())
	}
	r, err = ParseRange("", "2026-01-31", clock)
	if err != nil || !r.From.Equal(date("2026-01-02")) {
		t.Fatalf("solo to: %+v %v", r, err)
	}
	r, err = ParseRange("2026-09-01", "", clock)
	if err != nil || !r.To.Equal(date("2026-09-13")) || r.Days() != 13 {
		t.Fatalf("solo from: %+v %v", r, err)
	}
}

func TestValidacionDeRangos(t *testing.T) {
	if r, err := ParseRange("2024-01-01", "2024-12-31", clock); err != nil || r.Days() != MaxRangeDays {
		t.Fatalf("un ano bisiesto completo cabe: %d %v", r.Days(), err)
	}
	cases := []struct{ from, to, field string }{
		{"2026-09-10", "2026-09-01", "from"},
		{"2025-01-01", "2026-01-02", "to"},
		{"2026-9-1", "", "from"},
		{"", "2026-02-30", "to"},
		{"hoy", "", "from"},
	}
	for _, c := range cases {
		_, err := ParseRange(c.from, c.to, clock)
		ve, ok := err.(*ValidationError)
		if !ok || ve.Field != c.field {
			t.Errorf("%q..%q: se esperaba error en %s, hubo %v", c.from, c.to, c.field, err)
		}
	}
	if _, err := ParseRange("2026-09-13", "2026-09-13", clock); err != nil {
		t.Fatalf("un solo dia: %v", err)
	}
}

func TestRangoDeCampana(t *testing.T) {
	first, last := date("2026-09-01"), date("2026-09-05")
	if r := CampaignRange(CampaignSummary{FirstDay: &first, LastDay: &last}, clock); r.Days() != 5 || !r.From.Equal(first) {
		t.Fatalf("dias con envios: %+v", r)
	}
	started := time.Date(2026, 9, 10, 22, 0, 0, 0, time.UTC)
	if r := CampaignRange(CampaignSummary{StartedAt: &started}, clock); !r.From.Equal(date("2026-09-10")) || !r.To.Equal(date("2026-09-13")) {
		t.Fatalf("sin envios, desde el inicio hasta hoy: %+v", r)
	}
	if r := CampaignRange(CampaignSummary{}, clock); r.Days() != 1 || !r.To.Equal(date("2026-09-13")) {
		t.Fatalf("sin nada, hoy: %+v", r)
	}
	old := date("2020-01-01")
	if r := CampaignRange(CampaignSummary{FirstDay: &old, LastDay: &last}, clock); r.Days() != MaxRangeDays || !r.To.Equal(last) {
		t.Fatalf("se recorta a los ultimos %d dias: %+v", MaxRangeDays, r)
	}
}

func TestLimiteDeDominios(t *testing.T) {
	if v, err := ParseDomainLimit(""); err != nil || v != DefaultDomainLimit {
		t.Fatalf("por defecto: %d %v", v, err)
	}
	for _, ok := range []string{"1", "100"} {
		if _, err := ParseDomainLimit(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"0", "101", "-1", "diez"} {
		if _, err := ParseDomainLimit(bad); !IsValidation(err) {
			t.Errorf("%s: se esperaba error de validacion, hubo %v", bad, err)
		}
	}
}

func TestFiltroDeClase(t *testing.T) {
	if c, err := ParseClassFilter(""); err != nil || c != "" {
		t.Fatalf("vacio = todas: %q %v", c, err)
	}
	if _, err := ParseClassFilter("corporate"); !IsValidation(err) {
		t.Fatalf("clase desconocida: %v", err)
	}
}
