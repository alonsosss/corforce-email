package domain

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// LocalDateTimeLayout es el formato de una hora de pared sin zona ("2026-10-01T09:00").
const LocalDateTimeLayout = "2006-01-02T15:04"

const (
	// maxUTCOffset es el mayor adelanto sobre UTC de una zona real (Pacific/Kiritimati,
	// +14:00): nadie llega antes que ahi a una hora de pared.
	maxUTCOffset = 14 * time.Hour
	// transitionWindow: entre dos cambios de hora de una misma zona pasan semanas, asi que
	// el desfase 48 h antes y 48 h despues de la hora pedida es el de antes y el de despues
	// de la transicion que la afecte.
	transitionWindow  = 48 * time.Hour
	maxTimezoneLength = 64
)

// zoneCache guarda las zonas ya cargadas: el reparto por tramos consulta la de cada
// contacto de cada pagina, y cargarla lee la base de zonas. Solo entran nombres validos, asi
// que no crece mas alla del catalogo IANA.
var zoneCache sync.Map

// timezoneRegex es la forma de un nombre IANA (la misma regla que contacts).
var timezoneRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+){0,2}$`)

// LocalDateTime es una hora de pared sin zona. Se guarda como un time.Time en UTC cuyos
// campos son los de la hora pedida.
type LocalDateTime struct {
	wall time.Time
}

func ParseLocalDateTime(s string) (LocalDateTime, error) {
	t, err := time.ParseInLocation(LocalDateTimeLayout, strings.TrimSpace(s), time.UTC)
	if err != nil {
		return LocalDateTime{}, NewValidationError("local_send_at: use el formato AAAA-MM-DDTHH:MM")
	}
	return LocalDateTime{wall: t}, nil
}

// LocalDateTimeFromWall reconstruye la hora de pared guardada (sus campos, no su zona).
func LocalDateTimeFromWall(t time.Time) LocalDateTime {
	return LocalDateTime{wall: time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)}
}

func (l LocalDateTime) IsZero() bool { return l.wall.IsZero() }

// Wall es la hora de pared como time.Time en UTC (para guardarla en una columna sin zona).
func (l LocalDateTime) Wall() time.Time { return l.wall }

func (l LocalDateTime) String() string { return l.wall.Format(LocalDateTimeLayout) }

// In es el instante en que la zona loc marca esta hora de pared. Los cambios de hora se
// resuelven siempre igual, sin depender de lo que haga time.Date:
//   - hora que no existe (el reloj salta hacia delante): se desplaza la duracion del salto,
//     con el desfase de antes del cambio (02:30 en un salto de 02:00 a 03:00 es 03:30);
//   - hora que existe dos veces (el reloj retrocede): la primera de las dos.
func (l LocalDateTime) In(loc *time.Location) time.Time {
	naive := l.wall
	_, before := naive.Add(-transitionWindow).In(loc).Zone()
	_, after := naive.Add(transitionWindow).In(loc).Zone()
	first := naive.Add(-time.Duration(before) * time.Second)
	second := naive.Add(-time.Duration(after) * time.Second)
	ok1 := l.matches(first, loc)
	ok2 := l.matches(second, loc)
	switch {
	case ok1 && ok2:
		if second.Before(first) {
			return second
		}
		return first
	case ok1:
		return first
	case ok2:
		return second
	}
	return first
}

func (l LocalDateTime) matches(t time.Time, loc *time.Location) bool {
	w := t.In(loc)
	return w.Year() == l.wall.Year() && w.Month() == l.wall.Month() && w.Day() == l.wall.Day() &&
		w.Hour() == l.wall.Hour() && w.Minute() == l.wall.Minute()
}

// Earliest es el primer instante en que alguna zona marca esta hora de pared.
func (l LocalDateTime) Earliest() time.Time { return l.wall.Add(-maxUTCOffset) }

// LoadTimezone acepta un nombre de zona IANA que el binario sepa cargar (incorpora
// time/tzdata). "Local" no es una zona: es la del servidor.
func LoadTimezone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" || len(name) > maxTimezoneLength || !timezoneRegex.MatchString(name) {
		return nil, NewValidationError("timezone: %q no es una zona IANA valida (America/Lima)", name)
	}
	if loc, ok := zoneCache.Load(name); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, NewValidationError("timezone: %q no es una zona IANA valida (America/Lima)", name)
	}
	zoneCache.Store(name, loc)
	return loc, nil
}

// TimezoneDelivery es el envio por zona horaria: cada contacto recibe la campana a la hora
// de pared LocalSendAt en su zona, o en FallbackTimezone si no tiene una valida.
type TimezoneDelivery struct {
	LocalSendAt      LocalDateTime
	FallbackTimezone string
}

// Target es el instante de envio para un contacto con esa zona. Una zona ausente o que
// no carga (dato antiguo, zona retirada de la base IANA) usa la de respaldo.
func (t *TimezoneDelivery) Target(contactTimezone string) time.Time {
	if loc, err := LoadTimezone(contactTimezone); err == nil {
		return t.LocalSendAt.In(loc)
	}
	return t.fallbackTarget()
}

func (t *TimezoneDelivery) fallbackTarget() time.Time {
	loc, err := LoadTimezone(t.FallbackTimezone)
	if err != nil {
		loc = time.UTC
	}
	return t.LocalSendAt.In(loc)
}
