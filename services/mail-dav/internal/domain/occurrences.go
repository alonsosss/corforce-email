package domain

import (
	"sort"
	"strings"
	"time"
)

// Occurrence es una aparicion de un evento dentro de un rango: la de la serie (con el texto del VEVENT
// principal) o la de una sobrescritura (con el suyo). Recurring la marca como parte de una serie y RecurrenceID
// es el instante que la identifica dentro de ella (su inicio original, el RECURRENCE-ID de RFC 5545). Free es la
// aparicion que no ocupa el tiempo de su dueno: transparente, cancelada o de dia completo sin TRANSP:OPAQUE.
type Occurrence struct {
	Start        time.Time
	End          time.Time
	AllDay       bool
	Title        string
	Location     string
	Recurring    bool
	RecurrenceID time.Time
	Free         bool
}

// free dice si un componente no ocupa tiempo: TRANSP:TRANSPARENT, STATUS:CANCELLED o, sin TRANSP explicito, un
// dia completo (como lo tratan los clientes de calendario habituales).
func (c eventComponent) free() bool {
	transp := strings.ToUpper(c.text("TRANSP"))
	if transp == "TRANSPARENT" || strings.EqualFold(c.text("STATUS"), "CANCELLED") {
		return true
	}
	return c.start.date && transp != "OPAQUE"
}

// Occurrences expande el objeto dentro del rango [r.Start, r.End), que debe traer los dos extremos: la serie
// con sus RDATE, sin las fechas de EXDATE ni las que sustituye una sobrescritura, y cada sobrescritura en su
// propia hora. El trabajo se descuenta de b (Each). Lo que no se puede expandir con exactitud (una regla que
// el servidor no sabe expandir, un RDATE con periodos, un RECURRENCE-ID con RANGE o el presupuesto agotado)
// aparece en su primera ocurrencia, si cae en el rango, con Recurring verdadero; lo expandido hasta entonces
// se conserva. El resultado va ordenado por inicio.
func (o CalendarObject) Occurrences(r TimeRange, b *Budget) []Occurrence {
	if r.Start == nil || r.End == nil {
		return nil
	}
	var out []Occurrence
	overridden := map[int64]bool{}
	for _, c := range o.events {
		if c.recurrenceID != nil {
			overridden[o.zones.instant(*c.recurrenceID).Unix()] = true
		}
	}
	emit := func(c eventComponent, start, rid time.Time, recurring bool) {
		out = append(out, Occurrence{
			Start: start, End: start.Add(c.dur), AllDay: c.start.date,
			Title: c.text("SUMMARY"), Location: c.text("LOCATION"), Recurring: recurring,
			RecurrenceID: rid, Free: c.free(),
		})
	}
	for _, c := range o.events {
		start := o.zones.instant(c.start)
		if c.recurrenceID != nil {
			if r.overlaps(start, c.dur) {
				emit(c, start, o.zones.instant(*c.recurrenceID), true)
			}
			continue
		}
		recurring := c.rrule != nil || len(c.rdates) > 0 || c.approx || len(overridden) > 0
		skipped := make(map[int64]bool, len(overridden)+len(c.exdates))
		for k := range overridden {
			skipped[k] = true
		}
		for _, d := range c.exdates {
			skipped[o.zones.instant(d).Unix()] = true
		}
		seen := map[int64]bool{}
		add := func(inst time.Time) {
			key := inst.Unix()
			if seen[key] || skipped[key] || !r.overlaps(inst, c.dur) {
				return
			}
			seen[key] = true
			emit(c, inst, inst, recurring)
		}
		// DTSTART es siempre la primera aparicion (RFC 5545, 3.8.5.3), y la unica que se da por segura de lo que
		// no se sabe expandir.
		add(start)
		if c.approx || (c.rrule != nil && c.rrule.Unsupported) {
			continue
		}
		for _, d := range c.rdates {
			add(o.zones.instant(d))
		}
		if c.rrule == nil {
			continue
		}
		toInstant := func(w time.Time) time.Time { return o.zones.toInstant(c.start.tzid, w) }
		c.rrule.Each(c.start, toInstant, b, r.Start.Add(-c.dur-ahead), func(w time.Time) bool {
			inst := toInstant(w)
			if !inst.Before(*r.End) {
				return false
			}
			add(inst)
			return true
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}
