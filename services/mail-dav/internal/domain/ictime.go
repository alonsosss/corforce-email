package domain

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dtValue es un DATE o DATE-TIME de iCalendar tal como lo escribio el cliente. wall es el reloj de pared
// (en UTC solo como contenedor): el instante real depende de la zona, que resuelve zoneSet.
type dtValue struct {
	wall time.Time
	utc  bool
	date bool
	tzid string
}

const (
	layoutDate     = "20060102"
	layoutDateTime = "20060102T150405"
)

// parseDateTime lee un DATE (VALUE=DATE) o un DATE-TIME, flotante, en UTC (sufijo Z) o con TZID.
func parseDateTime(value string, params map[string]string) (dtValue, error) {
	value = strings.TrimSpace(value)
	kind := strings.ToUpper(params["VALUE"])
	if kind != "" && kind != "DATE" && kind != "DATE-TIME" {
		return dtValue{}, errors.New("VALUE no admitido para una fecha")
	}
	if len(value) == len(layoutDate) {
		if kind == "DATE-TIME" {
			return dtValue{}, errors.New("fecha sin hora con VALUE=DATE-TIME")
		}
		t, err := time.Parse(layoutDate, value)
		if err != nil {
			return dtValue{}, errors.New("fecha no valida")
		}
		return dtValue{wall: t, date: true}, nil
	}
	if kind == "DATE" {
		return dtValue{}, errors.New("fecha con hora con VALUE=DATE")
	}
	utc := strings.HasSuffix(value, "Z")
	t, err := time.Parse(layoutDateTime, strings.TrimSuffix(value, "Z"))
	if err != nil {
		return dtValue{}, errors.New("fecha y hora no validas")
	}
	tzid := params["TZID"]
	if utc && tzid != "" {
		return dtValue{}, errors.New("una hora en UTC no lleva TZID")
	}
	if len(tzid) > maxUIDLength {
		return dtValue{}, errors.New("TZID demasiado largo")
	}
	return dtValue{wall: t, utc: utc, tzid: tzid}, nil
}

// maxDurationDays acota una DURATION: un evento de mas de cien anos no es un evento.
const maxDurationDays = 366 * 100

// parseDuration lee una DURATION de RFC 5545 ([+-]P[nW][nD][T[nH][nM][nS]]).
func parseDuration(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	sign := time.Duration(1)
	switch {
	case strings.HasPrefix(v, "-"):
		sign, v = -1, v[1:]
	case strings.HasPrefix(v, "+"):
		v = v[1:]
	}
	if !strings.HasPrefix(v, "P") || len(v) < 2 {
		return 0, errors.New("DURATION no valida")
	}
	v = v[1:]
	var total time.Duration
	inTime, seen, timePart := false, false, false
	for len(v) > 0 {
		if v[0] == 'T' {
			if inTime {
				return 0, errors.New("DURATION no valida")
			}
			inTime, v = true, v[1:]
			continue
		}
		i := 0
		for i < len(v) && v[i] >= '0' && v[i] <= '9' {
			i++
		}
		if i == 0 || i > 9 || i == len(v) {
			return 0, errors.New("DURATION no valida")
		}
		n, _ := strconv.Atoi(v[:i])
		var unit time.Duration
		switch {
		case v[i] == 'W' && !inTime:
			unit = 7 * 24 * time.Hour
		case v[i] == 'D' && !inTime:
			unit = 24 * time.Hour
		case v[i] == 'H' && inTime:
			unit = time.Hour
		case v[i] == 'M' && inTime:
			unit = time.Minute
		case v[i] == 'S' && inTime:
			unit = time.Second
		default:
			return 0, errors.New("DURATION no valida")
		}
		total += time.Duration(n) * unit
		if total > maxDurationDays*24*time.Hour {
			return 0, errors.New("DURATION excesiva")
		}
		seen, timePart = true, timePart || inTime
		v = v[i+1:]
	}
	if !seen || (inTime && !timePart) {
		return 0, errors.New("DURATION no valida")
	}
	return sign * total, nil
}

var (
	tzidNameRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)
	utcOffsetRe  = regexp.MustCompile(`^[+-][0-9]{4}([0-9]{2})?$`)
	locationsIdx sync.Map
)

// maxZoneWork acota la expansion de las reglas de un VTIMEZONE al convertir una hora.
const maxZoneWork = 5000

// ianaLocation resuelve un TZID como nombre de la base de zonas horarias. El nombre es texto del cliente:
// solo se admite la forma de un nombre de zona (sin puntos ni barras iniciales), de modo que ninguna
// entrada llega al sistema de ficheros con otra forma. Solo se guardan los aciertos, asi que la cache esta
// acotada por el tamano de la base de zonas.
func ianaLocation(tzid string) *time.Location {
	if len(tzid) > 64 || tzid == "Local" || !tzidNameRe.MatchString(tzid) {
		return nil
	}
	if loc, ok := locationsIdx.Load(tzid); ok {
		return loc.(*time.Location)
	}
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil
	}
	locationsIdx.Store(tzid, loc)
	return loc
}

// zoneSet convierte relojes de pared en instantes. Un TZID se resuelve primero como zona de la base
// IANA y, si no lo es (un nombre propio de Outlook, por ejemplo), con el VTIMEZONE que envio el cliente
// en el mismo objeto. Sin ninguna de las dos, la hora se toma como UTC.
type zoneSet struct {
	custom map[string]*vtimezone
}

func (z zoneSet) toInstant(tzid string, wall time.Time) time.Time {
	if tzid == "" {
		return wall
	}
	if loc := ianaLocation(tzid); loc != nil {
		return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc).UTC()
	}
	if tz := z.custom[tzid]; tz != nil {
		return tz.toInstant(wall)
	}
	return wall
}

func (z zoneSet) instant(d dtValue) time.Time { return z.toInstant(d.tzid, d.wall) }

// observance es un tramo STANDARD o DAYLIGHT de un VTIMEZONE: desde cada inicio pasa a estar vigente su
// desfase (to, en segundos), que se sumaba al anterior (from).
type observance struct {
	onset  dtValue
	from   int
	to     int
	rule   *RRule
	rdates []time.Time
}

type vtimezone struct {
	observances []observance
}

// latestOnset es el inicio mas reciente de la observancia que no pasa del reloj de pared dado.
func (o observance) latestOnset(wall time.Time) (time.Time, bool) {
	var latest time.Time
	found := false
	consider := func(t time.Time) {
		if !t.After(wall) && (!found || t.After(latest)) {
			latest, found = t, true
		}
	}
	consider(o.onset.wall)
	for _, r := range o.rdates {
		consider(r)
	}
	if o.rule != nil {
		shift := func(w time.Time) time.Time { return w.Add(-time.Duration(o.from) * time.Second) }
		o.rule.Each(o.onset, shift, NewBudget(maxZoneWork), time.Time{}, func(w time.Time) bool {
			if w.After(wall) {
				return false
			}
			consider(w)
			return true
		})
	}
	return latest, found
}

// offsetAt es el desfase (segundos) vigente en un reloj de pared: el de la observancia cuyo inicio mas
// reciente precede a esa hora. Antes de todo inicio vale el desfase previo de la observancia mas antigua.
func (z *vtimezone) offsetAt(wall time.Time) int {
	var best time.Time
	offset, found := 0, false
	earliest := 0
	for i, o := range z.observances {
		if i == 0 || o.onset.wall.Before(z.observances[earliest].onset.wall) {
			earliest = i
		}
		if latest, ok := o.latestOnset(wall); ok && (!found || latest.After(best)) {
			best, offset, found = latest, o.to, true
		}
	}
	if !found {
		return z.observances[earliest].from
	}
	return offset
}

func (z *vtimezone) toInstant(wall time.Time) time.Time {
	return wall.Add(-time.Duration(z.offsetAt(wall)) * time.Second)
}

func parseUTCOffset(v string) (int, error) {
	v = strings.TrimSpace(v)
	if !utcOffsetRe.MatchString(v) {
		return 0, errors.New("desfase UTC no valido")
	}
	h, _ := strconv.Atoi(v[1:3])
	m, _ := strconv.Atoi(v[3:5])
	s := 0
	if len(v) == 7 {
		s, _ = strconv.Atoi(v[5:7])
	}
	if h > 23 || m > 59 || s > 59 {
		return 0, errors.New("desfase UTC no valido")
	}
	total := h*3600 + m*60 + s
	if v[0] == '-' {
		total = -total
	}
	return total, nil
}
