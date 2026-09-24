package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// vtimezoneYears es cuantos anios de transiciones describe un VTIMEZONE generado, desde el anterior al del
	// evento. Una regla anual que las describe todas se escribe como RRULE y vale para siempre; si no la hay (la
	// zona cambio de reglas en ese tramo), las transiciones se enumeran y la ultima queda vigente.
	vtimezoneYears = 12
	// maxZoneTransitions acota lo que se recorre de la base de zonas al generar un VTIMEZONE.
	maxZoneTransitions = 4 * vtimezoneYears
)

// zoneTransition es un cambio de desfase de una zona: el instante, los desfases de antes y despues (segundos)
// y si el nuevo es horario de verano.
type zoneTransition struct {
	at       time.Time
	from, to int
	name     string
	dst      bool
}

// wall es la hora de pared de la transicion con el desfase anterior, que es como la escribe DTSTART de una
// observancia (RFC 5545, 3.6.5).
func (t zoneTransition) wall() time.Time {
	return t.at.Add(time.Duration(t.from) * time.Second).UTC()
}

// zoneTransitions recorre los cambios de desfase de loc en [from, to) con time.Time.ZoneBounds, que lee la base
// de zonas embebida (time/tzdata).
func zoneTransitions(loc *time.Location, from, to time.Time) []zoneTransition {
	var out []zoneTransition
	t := from.In(loc)
	for len(out) < maxZoneTransitions {
		_, end := t.ZoneBounds()
		if end.IsZero() || !end.Before(to) {
			break
		}
		_, before := t.Zone()
		name, after := end.In(loc).Zone()
		if before != after {
			out = append(out, zoneTransition{at: end.UTC(), from: before, to: after, name: name, dst: end.In(loc).IsDST()})
		}
		t = end.In(loc)
	}
	return out
}

// yearlyRule es la regla FREQ=YEARLY;BYMONTH;BYDAY que produce la transicion t si la describe un dia de la
// semana con ordinal: -1 para la ultima semana del mes y 1 a 4 para las demas.
func yearlyRule(w time.Time, last bool) string {
	n := strconv.Itoa((w.Day()-1)/7 + 1)
	if last {
		n = "-1"
	}
	return fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%s%s", int(w.Month()), n, weekdayCodes[w.Weekday()])
}

// ruleFits dice si la regla anual (ultima semana o ordinal) reproduce la fecha de pared w.
func ruleFits(w time.Time, last bool, ordinal int, month time.Month, day time.Weekday) bool {
	if w.Month() != month || w.Weekday() != day {
		return false
	}
	if last {
		return w.Day()+7 > time.Date(w.Year(), w.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	}
	return (w.Day()-1)/7+1 == ordinal
}

func formatUTCOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	out := fmt.Sprintf("%s%02d%02d", sign, seconds/3600, seconds/60%60)
	if s := seconds % 60; s != 0 {
		out += fmt.Sprintf("%02d", s)
	}
	return out
}

// observanceLines escribe una observancia STANDARD o DAYLIGHT.
func observanceComp(dst bool, from, to int, name string, onset time.Time, rule string, rdates []time.Time) *rawComp {
	kind := "STANDARD"
	if dst {
		kind = "DAYLIGHT"
	}
	c := &rawComp{name: kind}
	add := func(text string) { c.props = append(c.props, generated(text)) }
	add("DTSTART:" + onset.Format(layoutDateTime))
	add("TZOFFSETFROM:" + formatUTCOffset(from))
	add("TZOFFSETTO:" + formatUTCOffset(to))
	if name = strings.TrimSpace(name); name != "" {
		add("TZNAME:" + escapeText(name))
	}
	if rule != "" {
		add("RRULE:" + rule)
	}
	if len(rdates) > 0 {
		parts := make([]string, len(rdates))
		for i, d := range rdates {
			parts[i] = d.Format(layoutDateTime)
		}
		add("RDATE:" + strings.Join(parts, ","))
	}
	return c
}

// buildVTimezone genera el VTIMEZONE de una zona de la base IANA con sus transiciones reales alrededor de from:
// cada clase de transicion (entrada y salida del horario de verano) es una observancia con su regla anual si una
// regla la describe en todos los anios del tramo, o con sus fechas enumeradas si no. Una zona sin transiciones
// en el tramo es una sola observancia STANDARD con su desfase.
func buildVTimezone(loc *time.Location, from time.Time) *rawComp {
	tz := &rawComp{name: "VTIMEZONE"}
	tz.props = append(tz.props, generated("TZID:"+loc.String()))
	begin := time.Date(from.Year()-1, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := begin.AddDate(vtimezoneYears, 0, 0)
	transitions := zoneTransitions(loc, begin, end)
	if len(transitions) == 0 {
		name, offset := from.In(loc).Zone()
		tz.comps = append(tz.comps, observanceComp(false, offset, offset, name, time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC), "", nil))
		return tz
	}
	type class struct {
		dst      bool
		from, to int
		name     string
		items    []zoneTransition
	}
	var classes []*class
	for _, t := range transitions {
		var found *class
		for _, c := range classes {
			if c.dst == t.dst && c.from == t.from && c.to == t.to {
				found = c
				break
			}
		}
		if found == nil {
			found = &class{dst: t.dst, from: t.from, to: t.to, name: t.name}
			classes = append(classes, found)
		}
		found.items = append(found.items, t)
	}
	for _, c := range classes {
		first := c.items[0].wall()
		rule := ""
		// Una regla anual solo se escribe si hay una transicion por anio, sin huecos hasta el final del tramo, a la
		// misma hora de pared y en el mismo dia de la semana del mismo mes.
		consecutive := c.items[len(c.items)-1].wall().Year() >= end.Year()-2
		for i := 1; i < len(c.items) && consecutive; i++ {
			consecutive = c.items[i].wall().Year() == c.items[i-1].wall().Year()+1
		}
		if consecutive && len(c.items) > 1 {
			ordinal := (first.Day()-1)/7 + 1
			for _, last := range []bool{true, false} {
				fits := true
				for _, t := range c.items {
					w := t.wall()
					if w.Hour() != first.Hour() || w.Minute() != first.Minute() || w.Second() != first.Second() ||
						!ruleFits(w, last, ordinal, first.Month(), first.Weekday()) {
						fits = false
						break
					}
				}
				if fits {
					rule = yearlyRule(first, last)
					break
				}
			}
		}
		var rdates []time.Time
		if rule == "" {
			for _, t := range c.items[1:] {
				rdates = append(rdates, t.wall())
			}
		}
		tz.comps = append(tz.comps, observanceComp(c.dst, c.from, c.to, c.name, first, rule, rdates))
	}
	return tz
}

// wallIn es el reloj de pared de un instante en la zona de un DTSTART: la IANA, la del VTIMEZONE del objeto o, sin
// zona, el propio instante (UTC o flotante).
func (z zoneSet) wallIn(tzid string, inst time.Time) time.Time {
	if tzid == "" {
		return inst.UTC()
	}
	if loc := ianaLocation(tzid); loc != nil {
		w := inst.In(loc)
		return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.UTC)
	}
	if tz := z.custom[tzid]; tz != nil {
		guess := inst.Add(time.Duration(tz.offsetAt(inst)) * time.Second)
		return inst.Add(time.Duration(tz.offsetAt(guess)) * time.Second).UTC()
	}
	return inst.UTC()
}

// formatLike escribe prop con el instante inst en la misma forma que like (un DTSTART ya guardado): fecha, UTC, con
// su TZID o flotante. Asi un RECURRENCE-ID o un EXDATE casan con el DTSTART de la serie (RFC 5545, 3.8.4.4).
func (z zoneSet) formatLike(prop string, like dtValue, inst time.Time) string {
	switch {
	case like.date:
		return prop + ";VALUE=DATE:" + inst.UTC().Format(layoutDate)
	case like.utc:
		return prop + ":" + inst.UTC().Format(layoutStamp)
	case like.tzid != "":
		return prop + ";TZID=" + quoteParam(like.tzid) + ":" + z.wallIn(like.tzid, inst).Format(layoutDateTime)
	}
	return prop + ":" + inst.UTC().Format(layoutDateTime)
}
