package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Cambios de hora de 2026 usados en las pruebas (todas con fechas fijas):
//
//	America/New_York  8 de marzo  07:00Z  02:00 EST -> 03:00 EDT (no existe 02:00-02:59)
//	                  1 de nov.   06:00Z  02:00 EDT -> 01:00 EST (01:00-01:59 se repite)
//	Europe/Berlin     29 de marzo 01:00Z  02:00 CET -> 03:00 CEST
//	                  25 de oct.  01:00Z  03:00 CEST -> 02:00 CET (02:00-02:59 se repite)
//	America/Lima     sin horario de verano (UTC-5)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadTimezone(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestLoadTimezone(t *testing.T) {
	for _, name := range []string{"UTC", "America/Lima", "Europe/Berlin", "Etc/GMT+5", "America/Argentina/Buenos_Aires"} {
		loc, err := LoadTimezone(name)
		if err != nil || loc.String() != name {
			t.Errorf("%q: %v", name, err)
		}
	}
	invalid := map[string]string{
		"vacia":                    "",
		"la del servidor":          "Local",
		"desplazamiento":           "+05:00",
		"desplazamiento negativo":  "-05:00",
		"desplazamiento con UTC":   "UTC+05:00",
		"abreviatura inexistente":  "GMT+5",
		"en minusculas":            "america/lima",
		"lugar inexistente":        "America/Nowhere",
		"con espacios":             " America/Lima",
		"espacio al final":         "America/Lima ",
		"ruta relativa":            "../etc/passwd",
		"ruta absoluta":            "/etc/localtime",
		"segmento vacio":           "America//Lima",
		"mas larga que la columna": "America/" + strings.Repeat("a", MaxTimezoneLength),
	}
	for name, tz := range invalid {
		if _, err := LoadTimezone(tz); !errors.Is(err, ErrInvalidTimezone) {
			t.Errorf("%s (%q): %v", name, tz, err)
		}
	}
}

// Sin base de zonas (una imagen scratch sin time/tzdata) esto falla; el binario lo
// comprueba al arrancar y el Dockerfile al construir la imagen.
func TestLaBaseDeZonasEstaDisponible(t *testing.T) {
	if err := CheckTZDatabase(); err != nil {
		t.Fatal(err)
	}
}

// transitionWindows son ventanas de varios dias alrededor de un cambio de hora, en zonas de
// ambos hemisferios y de ambos signos, con saltos de una hora, de media hora (Lord Howe), a
// medianoche (Santiago) y de un dia entero (Apia se salto el 30 de diciembre de 2011).
var transitionWindows = []struct {
	zone     string
	from, to time.Time
}{
	{"America/New_York", utc(2026, 3, 6, 0, 0, 0), utc(2026, 3, 10, 0, 0, 0)},
	{"America/New_York", utc(2026, 10, 30, 0, 0, 0), utc(2026, 11, 3, 0, 0, 0)},
	{"Europe/Berlin", utc(2026, 3, 27, 0, 0, 0), utc(2026, 3, 31, 0, 0, 0)},
	{"Europe/Berlin", utc(2026, 10, 23, 0, 0, 0), utc(2026, 10, 27, 0, 0, 0)},
	{"Australia/Sydney", utc(2026, 4, 2, 0, 0, 0), utc(2026, 4, 7, 0, 0, 0)},
	{"Australia/Sydney", utc(2026, 10, 1, 0, 0, 0), utc(2026, 10, 6, 0, 0, 0)},
	{"Australia/Lord_Howe", utc(2026, 4, 2, 0, 0, 0), utc(2026, 4, 7, 0, 0, 0)},
	{"Australia/Lord_Howe", utc(2026, 10, 1, 0, 0, 0), utc(2026, 10, 6, 0, 0, 0)},
	{"America/Santiago", utc(2026, 4, 2, 0, 0, 0), utc(2026, 4, 8, 0, 0, 0)},
	{"America/Santiago", utc(2026, 9, 3, 0, 0, 0), utc(2026, 9, 9, 0, 0, 0)},
	{"Pacific/Apia", utc(2011, 12, 27, 0, 0, 0), utc(2012, 1, 2, 0, 0, 0)},
}

// La referencia se calcula por fuerza bruta, minuto a minuto, sin la logica que prueba: el
// primer instante cuya hora de pared es la dada o posterior.
func TestPrimerInstanteDeUnaHoraDePared(t *testing.T) {
	for _, w := range transitionWindows {
		loc := mustLoad(t, w.zone)
		if _, a := w.from.In(loc).Zone(); func() bool { _, b := w.to.In(loc).Zone(); return a == b }() {
			t.Fatalf("%s: la ventana %v-%v no contiene un cambio de hora", w.zone, w.from, w.to)
		}
		var instants, walls []time.Time
		for at := w.from; !at.After(w.to); at = at.Add(time.Minute) {
			instants = append(instants, at)
			walls = append(walls, wallClock(at, loc))
		}
		i := 0
		for wall := walls[0].Add(time.Minute); !wall.After(walls[len(walls)-1]); wall = wall.Add(time.Minute) {
			for walls[i].Before(wall) {
				i++
			}
			if got := firstInstantAtWall(wall, loc); !got.Equal(instants[i]) || got.Location() != time.UTC {
				t.Fatalf("%s: hora de pared %s -> %v, se esperaba %v", w.zone, wall.Format("2006-01-02 15:04"), got, instants[i])
			}
		}
	}
}

func TestCronEnLaZonaDelTrabajo(t *testing.T) {
	cases := []struct {
		name, expr, zone string
		after, want      time.Time
	}{
		{"diario a las 8 en Lima", "0 8 * * *", "America/Lima", utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 13, 13, 0, 0)},
		{"@daily es la medianoche de Lima", "@daily", "America/Lima", utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 14, 5, 0, 0)},
		// 2026-09-14 00:30 UTC es lunes en UTC y todavia domingo 19:30 en Lima.
		{"el dia de la semana es el de la zona", "0 20 * * SUN", "America/Lima", utc(2026, 9, 14, 0, 30, 0), utc(2026, 9, 14, 1, 0, 0)},
		{"Berlin en verano", "0 8 * * *", "Europe/Berlin", utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 14, 6, 0, 0)},
		{"Berlin en invierno", "0 8 * * *", "Europe/Berlin", utc(2026, 12, 13, 10, 0, 0), utc(2026, 12, 14, 7, 0, 0)},
	}
	for _, tc := range cases {
		got, err := NextRun(tc.expr, tc.zone, tc.after)
		if err != nil || !got.Equal(tc.want) || got.Location() != time.UTC {
			t.Errorf("%s: %v (%v), se esperaba %v", tc.name, got, err, tc.want)
		}
	}
}

// sequence devuelve las ejecuciones de spec en (from, to].
func sequence(t *testing.T, spec CronSpec, from, to time.Time) []time.Time {
	t.Helper()
	var out []time.Time
	for at := from; ; {
		next, err := spec.Next(at)
		if err != nil {
			t.Fatal(err)
		}
		if next.After(to) {
			return out
		}
		out = append(out, next)
		at = next
	}
}

func TestHoraQueSeSaltaAlAdelantarElReloj(t *testing.T) {
	cases := []struct {
		name, expr, zone string
		from, to         time.Time
		want             []time.Time
	}{
		{"02:30 se lanza en el salto y al dia siguiente a su hora", "30 2 * * *", "America/New_York",
			utc(2026, 3, 8, 5, 0, 0), utc(2026, 3, 9, 12, 0, 0),
			[]time.Time{utc(2026, 3, 8, 7, 0, 0), utc(2026, 3, 9, 6, 30, 0)}},
		{"una hora que existe no se mueve", "0 3 * * *", "America/New_York",
			utc(2026, 3, 8, 5, 0, 0), utc(2026, 3, 8, 12, 0, 0),
			[]time.Time{utc(2026, 3, 8, 7, 0, 0)}},
		{"02:00 y 03:00 coinciden en el salto: una sola ejecucion", "0 2,3 * * *", "America/New_York",
			utc(2026, 3, 8, 5, 0, 0), utc(2026, 3, 9, 12, 0, 0),
			[]time.Time{utc(2026, 3, 8, 7, 0, 0), utc(2026, 3, 9, 6, 0, 0), utc(2026, 3, 9, 7, 0, 0)}},
		{"cada media hora sigue cada media hora", "*/30 * * * *", "America/New_York",
			utc(2026, 3, 8, 6, 15, 0), utc(2026, 3, 8, 7, 45, 0),
			[]time.Time{utc(2026, 3, 8, 6, 30, 0), utc(2026, 3, 8, 7, 0, 0), utc(2026, 3, 8, 7, 30, 0)}},
		{"Berlin: 02:30 se lanza a las 03:00 CEST", "30 2 * * *", "Europe/Berlin",
			utc(2026, 3, 28, 23, 0, 0), utc(2026, 3, 30, 12, 0, 0),
			[]time.Time{utc(2026, 3, 29, 1, 0, 0), utc(2026, 3, 30, 0, 30, 0)}},
	}
	for _, tc := range cases {
		got := sequence(t, mustParseIn(t, tc.expr, tc.zone), tc.from, tc.to)
		if !equalTimes(got, tc.want) {
			t.Errorf("%s:\n  %v\n  se esperaba %v", tc.name, got, tc.want)
		}
	}
}

func TestHoraQueSeRepiteAlAtrasarElReloj(t *testing.T) {
	cases := []struct {
		name, expr, zone string
		from, to         time.Time
		want             []time.Time
	}{
		{"01:30 solo en la primera pasada", "30 1 * * *", "America/New_York",
			utc(2026, 11, 1, 4, 0, 0), utc(2026, 11, 2, 12, 0, 0),
			[]time.Time{utc(2026, 11, 1, 5, 30, 0), utc(2026, 11, 2, 6, 30, 0)}},
		{"vista desde la segunda pasada, la de hoy ya se lanzo", "30 1 * * *", "America/New_York",
			utc(2026, 11, 1, 6, 10, 0), utc(2026, 11, 2, 12, 0, 0),
			[]time.Time{utc(2026, 11, 2, 6, 30, 0)}},
		{"cada hora no repite la 01:00", "0 * * * *", "America/New_York",
			utc(2026, 11, 1, 4, 30, 0), utc(2026, 11, 1, 8, 30, 0),
			[]time.Time{utc(2026, 11, 1, 5, 0, 0), utc(2026, 11, 1, 7, 0, 0), utc(2026, 11, 1, 8, 0, 0)}},
		{"Berlin: 02:30 CEST y no 02:30 CET", "30 2 * * *", "Europe/Berlin",
			utc(2026, 10, 24, 22, 0, 0), utc(2026, 10, 26, 12, 0, 0),
			[]time.Time{utc(2026, 10, 25, 0, 30, 0), utc(2026, 10, 26, 1, 30, 0)}},
	}
	for _, tc := range cases {
		got := sequence(t, mustParseIn(t, tc.expr, tc.zone), tc.from, tc.to)
		if !equalTimes(got, tc.want) {
			t.Errorf("%s:\n  %v\n  se esperaba %v", tc.name, got, tc.want)
		}
	}
}

func equalTimes(a, b []time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

// Alrededor de cada cambio de hora, recorrer Next lanza cada hora de pared que casa una
// sola vez (ninguna hora de pared se repite) y no pierde ninguna.
func TestCadaHoraDeParedSeLanzaUnaSolaVez(t *testing.T) {
	exprs := []string{"* * * * *", "*/7 * * * *", "0 * * * *", "30 2 * * *", "30 1 * * *", "0 0 * * *", "15,45 0-3 * * *", "@hourly"}
	for _, w := range transitionWindows {
		loc := mustLoad(t, w.zone)
		for _, expr := range exprs {
			spec := mustParseIn(t, expr, w.zone)
			fired := sequence(t, spec, w.from, w.to)
			seen := map[time.Time]bool{}
			for i, at := range fired {
				seen[at] = true
				if i > 0 && !wallClock(at, loc).After(wallClock(fired[i-1], loc)) {
					t.Fatalf("%s %q: %v repite o retrocede la hora de pared de %v", w.zone, expr, at, fired[i-1])
				}
			}
			for wall := spec.schedule.Next(wallClock(w.from, loc)); ; wall = spec.schedule.Next(wall) {
				at := firstInstantAtWall(wall, loc)
				if at.After(w.to) {
					break
				}
				if !seen[at] {
					t.Fatalf("%s %q: la hora de pared %s no se lanza", w.zone, expr, wall.Format("2006-01-02 15:04"))
				}
			}
		}
	}
}

func TestSiguienteTrasDespachoEnLaZona(t *testing.T) {
	cases := []struct {
		name, expr, zone string
		scheduled, now   time.Time
		want             time.Time
	}{
		{"Lima sin deriva", "0 8 * * *", "America/Lima",
			utc(2026, 9, 13, 13, 0, 0), utc(2026, 9, 13, 13, 0, 29), utc(2026, 9, 14, 13, 0, 0)},
		{"Lima tras tres dias caido: una vez y la de hoy", "0 8 * * *", "America/Lima",
			utc(2026, 9, 10, 13, 0, 0), utc(2026, 9, 13, 10, 0, 0), utc(2026, 9, 13, 13, 0, 0)},
		{"la del salto despachada tarde", "30 2 * * *", "America/New_York",
			utc(2026, 3, 8, 7, 0, 0), utc(2026, 3, 8, 7, 0, 29), utc(2026, 3, 9, 6, 30, 0)},
		{"caido durante el salto: una sola vez", "30 2 * * *", "America/New_York",
			utc(2026, 3, 7, 7, 30, 0), utc(2026, 3, 8, 12, 0, 0), utc(2026, 3, 9, 6, 30, 0)},
		{"caido durante la hora repetida: no se lanza otra vez", "30 1 * * *", "America/New_York",
			utc(2026, 11, 1, 5, 30, 0), utc(2026, 11, 1, 6, 45, 0), utc(2026, 11, 2, 6, 30, 0)},
		{"@every cuenta tiempo transcurrido aunque se atrase el reloj", "@every 90m", "America/New_York",
			utc(2026, 11, 1, 5, 0, 0), utc(2026, 11, 1, 5, 0, 10), utc(2026, 11, 1, 6, 30, 0)},
		{"@every cuenta tiempo transcurrido aunque se adelante el reloj", "@every 90m", "America/New_York",
			utc(2026, 3, 8, 6, 0, 0), utc(2026, 3, 8, 6, 0, 10), utc(2026, 3, 8, 7, 30, 0)},
	}
	for _, tc := range cases {
		got, err := mustParseIn(t, tc.expr, tc.zone).NextAfterDispatch(tc.scheduled, tc.now)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%s: %v (%v), se esperaba %v", tc.name, got, err, tc.want)
		}
	}
}

func TestReconciliarEnLaZona(t *testing.T) {
	now := utc(2026, 9, 13, 10, 0, 0)
	spec := mustParseIn(t, "0 8 * * *", "America/Lima")
	cases := []struct {
		name         string
		stored, want time.Time
	}{
		{"pendiente a deshora pasa a las 8 de Lima", utc(2026, 9, 13, 10, 37, 12), utc(2026, 9, 13, 13, 0, 0)},
		{"pendiente correcto no cambia", utc(2026, 9, 13, 13, 0, 0), utc(2026, 9, 13, 13, 0, 0)},
		{"pendiente en la hora UTC de antes pasa a la de Lima", utc(2026, 9, 14, 8, 0, 0), utc(2026, 9, 13, 13, 0, 0)},
		{"vencido con una ocurrencia saltada se lanza una vez", utc(2026, 9, 12, 12, 0, 0), utc(2026, 9, 12, 13, 0, 0)},
	}
	for _, tc := range cases {
		got, err := spec.Reconcile(tc.stored, now)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%s: %v (%v), se esperaba %v", tc.name, got, err, tc.want)
			continue
		}
		if again, err := spec.Reconcile(got, now); err != nil || !again.Equal(got) {
			t.Errorf("%s: reconciliar dos veces cambia el resultado: %v -> %v", tc.name, got, again)
		}
	}
}
