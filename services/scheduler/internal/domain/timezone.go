package domain

import (
	"fmt"
	"regexp"
	"time"
)

// DefaultTimezone es la zona de un trabajo que no declara la suya, y la de todos los
// trabajos anteriores a que existiera la columna: con ella nada cambia de hora.
const DefaultTimezone = "UTC"

// MaxTimezoneLength es el ancho de job_definitions.timezone.
const MaxTimezoneLength = 64

// La forma de un nombre IANA (Area/Lugar, Etc/GMT+5, UTC). Deja fuera los desplazamientos
// sueltos ("+05:00", "-5") antes de consultar la base: no son zonas, no saben de horario
// de verano.
var timezoneRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+)*$`)

// TimezonePattern es la forma que debe tener el nombre de una zona, para publicarla.
func TimezonePattern() string { return timezoneRe.String() }

// tzDatabaseProbe es UTC con su nombre de la base de zonas. Go resuelve "UTC" sin leerla;
// "Etc/UTC" solo carga si la base esta disponible.
const tzDatabaseProbe = "Etc/UTC"

// LoadTimezone valida el nombre de zona de un trabajo y la carga de la base de zonas.
// "Local" no es una zona: es la del servidor, que no dice nada de la empresa.
func LoadTimezone(name string) (*time.Location, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: timezone must not be empty", ErrInvalidTimezone)
	}
	if len(name) > MaxTimezoneLength || name == "Local" || !timezoneRe.MatchString(name) {
		return nil, fmt.Errorf("%w: %q is not an IANA time zone name (Area/Location)", ErrInvalidTimezone, truncateName(name))
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not in the time zone database", ErrInvalidTimezone, name)
	}
	return loc, nil
}

// CheckTZDatabase comprueba que el proceso puede cargar zonas. Sin base de zonas toda zona
// distinta de UTC fallaria al validarla y, peor, al despachar: el trabajo se desactivaria.
func CheckTZDatabase() error {
	if _, err := time.LoadLocation(tzDatabaseProbe); err != nil {
		return fmt.Errorf("time zone database unavailable: %w", err)
	}
	return nil
}

func truncateName(s string) string {
	if len(s) <= MaxTimezoneLength {
		return s
	}
	return s[:MaxTimezoneLength] + "..."
}

// wallClock es la hora de pared de t en loc, expresada como una hora UTC con los mismos
// campos. Sobre ella el parser evalua la expresion sin saltos ni repeticiones.
func wallClock(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

// firstInstantAtWall es el primer instante en que el reloj de pared de loc marca wall o
// una hora posterior:
//
//   - una hora que existe una vez es ese instante;
//   - una hora repetida (se atrasa el reloj) es su primera pasada;
//   - una hora que no existe (se adelanta el reloj) es el instante del salto.
//
// time.Date no sirve tal cual: ante una hora repetida o inexistente elige segun el signo
// del desplazamiento de la zona.
func firstInstantAtWall(wall time.Time, loc *time.Location) time.Time {
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), loc)
	got := wallClock(t, loc)
	switch {
	case got.Before(wall):
		// Go tomo el desplazamiento de antes del salto: el salto es el final de ese tramo.
		_, end := t.ZoneBounds()
		return end.UTC()
	case got.After(wall):
		start, _ := t.ZoneBounds()
		return start.UTC()
	}
	start, _ := t.ZoneBounds()
	if start.IsZero() {
		return t.UTC()
	}
	// Si el tramo anterior tambien marca wall, t es la segunda pasada de una hora repetida.
	_, prevOffset := start.Add(-time.Nanosecond).Zone()
	earlier := wall.Add(-time.Duration(prevOffset) * time.Second)
	if earlier.Before(start) && wallClock(earlier, loc).Equal(wall) {
		return earlier.UTC()
	}
	return t.UTC()
}
