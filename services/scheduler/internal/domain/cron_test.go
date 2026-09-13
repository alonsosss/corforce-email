package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func utc(y int, m time.Month, d, h, min, s int) time.Time {
	return time.Date(y, m, d, h, min, s, 0, time.UTC)
}

func TestNextRun(t *testing.T) {
	// 2026-09-13 es domingo.
	base := utc(2026, 9, 13, 10, 17, 42)
	cases := []struct {
		name, expr string
		after      time.Time
		want       time.Time
	}{
		{"cada minuto", "* * * * *", base, utc(2026, 9, 13, 10, 18, 0)},
		{"minuto exacto no se repite", "* * * * *", utc(2026, 9, 13, 10, 18, 0), utc(2026, 9, 13, 10, 19, 0)},
		{"cada cinco minutos", "*/5 * * * *", base, utc(2026, 9, 13, 10, 20, 0)},
		{"minuto fijo de cada hora", "30 * * * *", base, utc(2026, 9, 13, 10, 30, 0)},
		{"hora ya pasada salta al dia siguiente", "0 9 * * *", base, utc(2026, 9, 14, 9, 0, 0)},
		{"lista de horas", "0 8,12,18 * * *", base, utc(2026, 9, 13, 12, 0, 0)},
		{"rango de horas de oficina", "0 9-17 * * 1-5", base, utc(2026, 9, 14, 9, 0, 0)},
		{"dia de la semana por nombre", "15 3 * * WED", base, utc(2026, 9, 16, 3, 15, 0)},
		{"domingo como 0", "0 12 * * 0", base, utc(2026, 9, 13, 12, 0, 0)},
		{"domingo por nombre", "0 12 * * SUN", base, utc(2026, 9, 13, 12, 0, 0)},
		{"dia del mes o de la semana (OR)", "0 0 1 * MON", base, utc(2026, 9, 14, 0, 0, 0)},
		{"dia 31 salta los meses cortos", "0 0 31 * *", utc(2026, 9, 1, 0, 0, 0), utc(2026, 10, 31, 0, 0, 0)},
		{"fin de mes de febrero no bisiesto", "0 0 28-31 2 *", utc(2027, 2, 27, 12, 0, 0), utc(2027, 2, 28, 0, 0, 0)},
		{"29 de febrero espera al bisiesto", "0 0 29 2 *", utc(2026, 3, 1, 0, 0, 0), utc(2028, 2, 29, 0, 0, 0)},
		{"cambio de ano", "59 23 31 12 *", utc(2026, 12, 31, 23, 59, 0), utc(2027, 12, 31, 23, 59, 0)},
		{"@hourly", "@hourly", base, utc(2026, 9, 13, 11, 0, 0)},
		{"@daily", "@daily", base, utc(2026, 9, 14, 0, 0, 0)},
		{"@weekly empieza el domingo", "@weekly", base, utc(2026, 9, 20, 0, 0, 0)},
		{"@monthly", "@monthly", utc(2026, 1, 31, 8, 0, 0), utc(2026, 2, 1, 0, 0, 0)},
		{"@every cuenta desde after", "@every 90m", base, base.Add(90 * time.Minute)},
		{"@every descarta los nanosegundos", "@every 1m", base.Add(123 * time.Millisecond), base.Add(time.Minute)},
		{"espacios de sobra", "  0   9 * * *  ", base, utc(2026, 9, 14, 9, 0, 0)},
	}
	for _, tc := range cases {
		got, err := NextRun(tc.expr, DefaultTimezone, tc.after)
		if err != nil {
			t.Errorf("%s (%q): %v", tc.name, tc.expr, err)
			continue
		}
		if !got.Equal(tc.want) || got.Location() != time.UTC {
			t.Errorf("%s (%q): %v, se esperaba %v", tc.name, tc.expr, got, tc.want)
		}
	}
}

func TestLaZonaPorDefectoEsUTC(t *testing.T) {
	lima := time.FixedZone("UTC-5", -5*3600)
	after := time.Date(2026, 9, 13, 20, 0, 0, 0, lima) // 2026-09-14 01:00 UTC
	got, err := NextRun("0 9 * * *", DefaultTimezone, after)
	if err != nil {
		t.Fatal(err)
	}
	if want := utc(2026, 9, 14, 9, 0, 0); !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("%v, se esperaba %v en UTC", got, want)
	}
}

func TestExpresionesInvalidas(t *testing.T) {
	cases := map[string]string{
		"vacia":                    "",
		"solo espacios":            "   ",
		"cuatro campos":            "0 9 * *",
		"seis campos (segundos)":   "0 0 9 * * *",
		"minuto fuera de rango":    "60 * * * *",
		"hora fuera de rango":      "0 24 * * *",
		"dia del mes cero":         "0 0 0 * *",
		"mes trece":                "0 0 1 13 *",
		"dia de la semana ocho":    "0 0 * * 8",
		"domingo como 7":           "0 12 * * 7",
		"texto":                    "cada lunes",
		"paso cero":                "*/0 * * * *",
		"30 de febrero nunca":      "0 0 30 2 *",
		"31 de abril nunca":        "0 0 31 4 *",
		"descriptor desconocido":   "@fortnightly",
		"descriptor no admitido":   "@yearly",
		"descriptor en mayusculas": "@DAILY",
		"@every sin duracion":      "@every",
		"@every ilegible":          "@every pronto",
		"@every menor al minimo":   "@every 59s",
		"@every cero":              "@every 0s",
		"@every negativo":          "@every -5m",
		"zona horaria":             "CRON_TZ=America/Lima 0 9 * * *",
		"zona horaria corta":       "TZ=UTC 0 9 * * *",
		"demasiado larga":          "0 " + strings.Repeat("1,", 60) + "2 * * *",
	}
	now := utc(2026, 9, 13, 10, 0, 0)
	for name, expr := range cases {
		if _, err := NextRun(expr, DefaultTimezone, now); !errors.Is(err, ErrInvalidCron) {
			t.Errorf("%s (%q): %v", name, expr, err)
		}
	}
	if _, err := ParseCron("@every 1m", DefaultTimezone); err != nil {
		t.Errorf("@every del minimo exacto: %v", err)
	}
	if _, err := (CronSpec{}).Next(now); !errors.Is(err, ErrInvalidCron) {
		t.Errorf("una expresion sin analizar: %v", err)
	}
}

func mustParse(t *testing.T, expr string) CronSpec {
	t.Helper()
	return mustParseIn(t, expr, DefaultTimezone)
}

func mustParseIn(t *testing.T, expr, timezone string) CronSpec {
	t.Helper()
	spec, err := ParseCron(expr, timezone)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestSiguienteTrasDespachoSinDeriva(t *testing.T) {
	cases := []struct {
		name, expr     string
		scheduled, now time.Time
		want           time.Time
	}{
		{"calendario despachado con retraso", "*/5 * * * *",
			utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 13, 10, 0, 29), utc(2026, 9, 13, 10, 5, 0)},
		{"@every sigue la rejilla, no la hora real", "@every 5m",
			utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 13, 10, 0, 29), utc(2026, 9, 13, 10, 5, 0)},
		{"calendario con ocurrencias saltadas", "0 * * * *",
			utc(2026, 9, 13, 3, 0, 0), utc(2026, 9, 13, 10, 17, 0), utc(2026, 9, 13, 11, 0, 0)},
		{"diario tras dias caido", "0 3 * * *",
			utc(2026, 9, 10, 3, 0, 0), utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 14, 3, 0, 0)},
		{"@every con ocurrencias saltadas", "@every 10m",
			utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 13, 10, 47, 3), utc(2026, 9, 13, 10, 50, 0)},
		{"@every con la siguiente justo en now", "@every 10m",
			utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 13, 10, 20, 0), utc(2026, 9, 13, 10, 30, 0)},
	}
	for _, tc := range cases {
		got, err := mustParse(t, tc.expr).NextAfterDispatch(tc.scheduled, tc.now)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("%s: %v, se esperaba %v", tc.name, got, tc.want)
		}
		if !got.After(tc.now) {
			t.Errorf("%s: la siguiente (%v) debe ser futura respecto de %v", tc.name, got, tc.now)
		}
	}
}

func TestReconciliarUnCalendario(t *testing.T) {
	now := utc(2026, 9, 13, 10, 0, 0)
	cases := []struct {
		name, expr string
		stored     time.Time
		want       time.Time
	}{
		{"pendiente a deshora pasa a la ocurrencia real", "0 3 * * *",
			utc(2026, 9, 13, 10, 37, 12), utc(2026, 9, 14, 3, 0, 0)},
		{"pendiente mas tarde que una ocurrencia intermedia", "*/10 * * * *",
			utc(2026, 9, 13, 10, 37, 12), utc(2026, 9, 13, 10, 10, 0)},
		{"pendiente correcto no cambia", "0 3 * * *",
			utc(2026, 9, 14, 3, 0, 0), utc(2026, 9, 14, 3, 0, 0)},
		{"vencido con una ocurrencia saltada se lanza una vez", "0 3 * * *",
			utc(2026, 9, 13, 2, 37, 12), utc(2026, 9, 13, 3, 0, 0)},
		{"vencido sin ocurrencia desde entonces pasa a la futura", "0 12 * * *",
			utc(2026, 9, 13, 9, 37, 12), utc(2026, 9, 13, 12, 0, 0)},
		{"vencido que es ocurrencia real no cambia", "0 3 * * *",
			utc(2026, 9, 13, 3, 0, 0), utc(2026, 9, 13, 3, 0, 0)},
		{"@every pendiente mas alla de un periodo se adelanta", "@every 5m",
			utc(2026, 9, 13, 10, 42, 0), utc(2026, 9, 13, 10, 5, 0)},
		{"@every pendiente dentro del periodo no cambia", "@every 5m",
			utc(2026, 9, 13, 10, 3, 0), utc(2026, 9, 13, 10, 3, 0)},
		{"@every vencido no cambia", "@every 5m",
			utc(2026, 9, 13, 9, 0, 0), utc(2026, 9, 13, 9, 0, 0)},
	}
	for _, tc := range cases {
		spec := mustParse(t, tc.expr)
		got, err := spec.Reconcile(tc.stored, now)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("%s: %v, se esperaba %v", tc.name, got, tc.want)
		}
		again, err := spec.Reconcile(got, now)
		if err != nil || !again.Equal(got) {
			t.Errorf("%s: reconciliar dos veces cambia el resultado: %v -> %v (%v)", tc.name, got, again, err)
		}
	}
}

func TestDefinicionCronValida(t *testing.T) {
	daily, bad, empty := "0 3 * * *", "0 25 * * *", ""
	ok := JobDefinition{Name: "Informe", Code: "informe", Handler: "reports.daily", JobType: JobTypeCron,
		CronExpression: &daily, Timezone: DefaultTimezone}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	lima := ok
	lima.Timezone = "America/Lima"
	if err := lima.Validate(); err != nil {
		t.Fatalf("un cron en America/Lima: %v", err)
	}
	for _, tz := range []string{"", "+05:00", "America/Nowhere"} {
		j := ok
		j.Timezone = tz
		if err := j.Validate(); !errors.Is(err, ErrInvalidTimezone) {
			t.Errorf("zona %q: %v", tz, err)
		}
	}
	for name, expr := range map[string]*string{"sin expresion": nil, "vacia": &empty, "hora 25": &bad} {
		j := ok
		j.CronExpression = expr
		if err := j.Validate(); !errors.Is(err, ErrInvalidCron) {
			t.Errorf("%s: %v", name, err)
		}
	}
	five := 5
	interval := JobDefinition{Name: "Informe", Code: "intervalo", Handler: "reports.daily", JobType: JobTypeInterval,
		IntervalMinutes: &five, CronExpression: &bad, Timezone: DefaultTimezone}
	if err := interval.Validate(); err != nil {
		t.Errorf("un trabajo de intervalo no depende de cron_expression: %v", err)
	}
	interval.Timezone = "Local"
	if err := interval.Validate(); !errors.Is(err, ErrInvalidTimezone) {
		t.Errorf("la zona se valida en todo trabajo, aunque solo la use cron: %v", err)
	}
}
