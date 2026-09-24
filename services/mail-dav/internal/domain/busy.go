package domain

import (
	"sort"
	"time"
)

const (
	// MaxBusyWindow y MaxBusyMailboxes son los topes de una consulta de ocupacion: los mismos que aplican las
	// funciones mail_dav.busy_intervals y mail_dav.resolve_busy_mailboxes (migracion 05_scheduling.sql).
	MaxBusyWindow    = 62 * 24 * time.Hour
	MaxBusyMailboxes = 50
)

// Interval es un tramo de tiempo [Start, End).
type Interval struct {
	Start time.Time
	End   time.Time
}

// BusyPlan acota lo que se materializa de la ocupacion de un evento al guardarlo: sus apariciones en [From, To)
// y como mucho MaxIntervals tramos. La disponibilidad del equipo se sirve de esos tramos, sin leer el evento.
type BusyPlan struct {
	From         time.Time
	To           time.Time
	MaxIntervals int
}

func (p BusyPlan) enabled() bool { return p.To.After(p.From) && p.MaxIntervals > 0 }

// Busy devuelve los tramos que el objeto ocupa en [p.From, p.To) (las apariciones que no son Free) y hasta donde
// quedan materializados: nil si no tiene apariciones despues de p.To (lastEnd es el fin de la ultima, nil si no
// se conoce), p.To si las tiene, o el inicio del primer tramo que no cupo en p.MaxIntervals.
func (o CalendarObject) Busy(p BusyPlan, lastEnd *time.Time, b *Budget) ([]Interval, *time.Time) {
	if !p.enabled() {
		return nil, nil
	}
	from, to := p.From, p.To
	var out []Interval
	for _, occ := range o.Occurrences(TimeRange{Start: &from, End: &to}, b) {
		if occ.Free {
			continue
		}
		out = append(out, Interval{Start: occ.Start, End: occ.End})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	if len(out) > p.MaxIntervals {
		until := out[p.MaxIntervals].Start
		return out[:p.MaxIntervals], &until
	}
	if lastEnd != nil && !lastEnd.After(to) {
		return out, nil
	}
	return out, &to
}

// NewEventAt es NewEvent con la ocupacion del evento materializada segun plan (ver CalendarObject.Busy). Con un
// plan vacio no se materializa nada y BusyPlanned queda falso.
func NewEventAt(resourceName, raw string, lim CalendarLimits, plan BusyPlan) (Event, error) {
	if !ValidEventResourceName(resourceName) {
		return Event{}, ErrInvalidName
	}
	obj, err := ParseCalendarObject(raw, lim)
	if err != nil {
		return Event{}, err
	}
	e := Event{
		ResourceName: resourceName,
		UID:          obj.UID,
		ICal:         raw,
		Size:         len(raw),
		ETag:         ETagOf(raw),
		Summary:      obj.Summary,
		FirstStart:   obj.FirstStart,
		LastEnd:      obj.LastEnd,
	}
	if plan.enabled() {
		e.Busy, e.BusyUntil = obj.Busy(plan, obj.LastEnd, NewBudget(lim.MaxRecurrenceWork))
		e.BusyPlanned = true
	}
	return e, nil
}

// MergeIntervals ordena y funde los tramos que se solapan o se tocan.
func MergeIntervals(in []Interval) []Interval {
	if len(in) == 0 {
		return []Interval{}
	}
	s := append([]Interval(nil), in...)
	sort.Slice(s, func(i, j int) bool { return s[i].Start.Before(s[j].Start) })
	out := []Interval{s[0]}
	for _, iv := range s[1:] {
		last := &out[len(out)-1]
		if !iv.Start.After(last.End) {
			if iv.End.After(last.End) {
				last.End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}
