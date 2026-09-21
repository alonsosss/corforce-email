package domain

import (
	"errors"
	"time"
)

// index calcula FirstStart y LastEnd: el intervalo que contiene todas las apariciones del objeto. Con una
// recurrencia sin fin, o que no se sabe expandir, LastEnd queda nulo (sin cota conocida).
func (o *CalendarObject) index(b *Budget) {
	var first, last time.Time
	seen, unbounded := false, false
	span := func(start time.Time, dur time.Duration) {
		end := start.Add(dur)
		if !seen || start.Before(first) {
			first = start
		}
		if !seen || end.After(last) {
			last = end
		}
		seen = true
	}
	for _, c := range o.events {
		span(o.zones.instant(c.start), c.dur)
		unbounded = unbounded || c.approx
		for _, d := range c.rdates {
			span(o.zones.instant(d), c.dur)
		}
		if c.rrule == nil {
			continue
		}
		toInstant := func(w time.Time) time.Time { return o.zones.toInstant(c.start.tzid, w) }
		switch {
		case c.rrule.Until != nil:
			u := c.rrule.Until
			bound := u.wall
			if u.date {
				bound = bound.Add(24 * time.Hour)
			}
			if !u.utc {
				bound = toInstant(bound)
			}
			if end := bound.Add(c.dur); end.After(last) {
				last = end
			}
		case c.rrule.Count > 0:
			var lastStart time.Time
			got := false
			res := c.rrule.Each(c.start, toInstant, b, time.Time{}, func(w time.Time) bool {
				lastStart, got = toInstant(w), true
				return true
			})
			if res == ExpandIncomplete {
				unbounded = true
			} else if got {
				span(lastStart, c.dur)
			}
		default:
			unbounded = true
		}
	}
	o.FirstStart = first
	if !unbounded {
		o.LastEnd = &last
	}
}

// TimeRange es el time-range de RFC 4791 (9.9): un intervalo [Start, End) en UTC con extremos opcionales.
type TimeRange struct {
	Start *time.Time
	End   *time.Time
}

var ErrInvalidFilter = errors.New("filtro de consulta no valido")

func parseUTCInstant(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	d, err := parseDateTime(v, nil)
	if err != nil || !d.utc {
		return nil, ErrInvalidFilter
	}
	return &d.wall, nil
}

// ParseTimeRange lee los atributos start y end de un time-range: fechas y horas en UTC. Al menos uno, y si
// van los dos, el fin es posterior al inicio.
func ParseTimeRange(start, end string) (TimeRange, error) {
	s, err := parseUTCInstant(start)
	if err != nil {
		return TimeRange{}, err
	}
	e, err := parseUTCInstant(end)
	if err != nil {
		return TimeRange{}, err
	}
	if (s == nil && e == nil) || (s != nil && e != nil && !e.After(*s)) {
		return TimeRange{}, ErrInvalidFilter
	}
	return TimeRange{Start: s, End: e}, nil
}

// overlaps aplica la tabla de RFC 4791 (9.9) a una aparicion: el evento sin duracion solo cuenta si su
// inicio cae dentro del rango; el resto, si su intervalo lo solapa.
func (r TimeRange) overlaps(start time.Time, dur time.Duration) bool {
	if dur == 0 {
		return (r.Start == nil || !start.Before(*r.Start)) && (r.End == nil || start.Before(*r.End))
	}
	return (r.Start == nil || start.Add(dur).After(*r.Start)) && (r.End == nil || start.Before(*r.End))
}

// ahead es lo que se retrocede desde el inicio del rango al saltar periodos de una regla: mas que
// cualquier diferencia entre un reloj de pared y UTC.
const ahead = 48 * time.Hour

// Overlaps dice si alguna aparicion del objeto (su recurrencia expandida hasta donde alcanza el
// presupuesto, con las excepciones y sobrescrituras) solapa el rango. Lo que no se puede decidir con
// exactitud (una regla que no se sabe expandir, un presupuesto agotado, un RDATE con periodos) cuenta como
// solape: es mejor devolver un evento de mas, que el cliente expande y descarta, que omitir uno.
func (o CalendarObject) Overlaps(r TimeRange, b *Budget) bool {
	overridden := map[int64]bool{}
	for _, c := range o.events {
		if c.recurrenceID != nil {
			overridden[o.zones.instant(*c.recurrenceID).Unix()] = true
		}
	}
	for _, c := range o.events {
		if c.approx {
			return true
		}
		start := o.zones.instant(c.start)
		if c.recurrenceID != nil {
			if r.overlaps(start, c.dur) {
				return true
			}
			continue
		}
		skipped := make(map[int64]bool, len(overridden)+len(c.exdates))
		for k := range overridden {
			skipped[k] = true
		}
		for _, d := range c.exdates {
			skipped[o.zones.instant(d).Unix()] = true
		}
		hit := func(inst time.Time) bool { return !skipped[inst.Unix()] && r.overlaps(inst, c.dur) }
		if hit(start) {
			return true
		}
		for _, d := range c.rdates {
			if hit(o.zones.instant(d)) {
				return true
			}
		}
		if c.rrule == nil {
			continue
		}
		toInstant := func(w time.Time) time.Time { return o.zones.toInstant(c.start.tzid, w) }
		var from time.Time
		if r.Start != nil {
			from = r.Start.Add(-c.dur - ahead)
		}
		found := false
		res := c.rrule.Each(c.start, toInstant, b, from, func(w time.Time) bool {
			inst := toInstant(w)
			if r.End != nil && !inst.Before(*r.End) {
				return false
			}
			if inst.Equal(start) {
				return true
			}
			found = hit(inst)
			return !found
		})
		if found || res == ExpandIncomplete {
			return true
		}
	}
	return false
}

// CompFilter es un comp-filter de calendar-query sobre un componente de VCALENDAR: no debe existir
// (IsNotDefined) o debe existir uno que cumpla el rango y todos los prop-filter.
type CompFilter struct {
	Name         string
	IsNotDefined bool
	Range        *TimeRange
	Props        []PropFilter
}

// CalendarFilter es el filtro de un calendar-query: todos sus comp-filter (los de dentro de VCALENDAR)
// deben cumplirse. Sin ninguno admite todo.
type CalendarFilter struct {
	Comps []CompFilter
}

const componentEvent = "VEVENT"

// Window es el descarte previo por tiempo: la interseccion de los rangos que el filtro exige a los
// VEVENT. El resto del filtro lo decide Matches.
func (f CalendarFilter) Window() EventWindow {
	var w EventWindow
	for _, c := range f.Comps {
		if c.Name != componentEvent || c.IsNotDefined || c.Range == nil {
			continue
		}
		if c.Range.Start != nil && (w.Start == nil || c.Range.Start.After(*w.Start)) {
			w.Start = c.Range.Start
		}
		if c.Range.End != nil && (w.End == nil || c.Range.End.Before(*w.End)) {
			w.End = c.Range.End
		}
	}
	return w
}

// Matches evalua el filtro sobre un objeto. perEvent acota lo que puede gastar la expansion de sus
// recurrencias, sin pasar de lo que quede en b.
func (f CalendarFilter) Matches(o CalendarObject, b *Budget, perEvent int) bool {
	release := b.Cap(perEvent)
	defer release()
	for _, c := range f.Comps {
		if !c.matches(o, b) {
			return false
		}
	}
	return true
}

func (c CompFilter) matches(o CalendarObject, b *Budget) bool {
	if c.Name != componentEvent {
		// Las colecciones solo guardan VEVENT: de cualquier otro componente no hay ninguno.
		return c.IsNotDefined
	}
	if c.IsNotDefined {
		return false
	}
	if c.Range != nil && !o.Overlaps(*c.Range, b) {
		return false
	}
	if len(c.Props) == 0 {
		return true
	}
	for _, ev := range o.events {
		if c.propsMatch(ev.props) {
			return true
		}
	}
	return false
}

func (c CompFilter) propsMatch(props []Property) bool {
	for _, pf := range c.Props {
		if !pf.matches(props) {
			return false
		}
	}
	return true
}

// NewEvent valida el iCalendar y arma el evento con su etag y sus campos indexados. Los identificadores y
// las fechas los pone quien lo guarda.
func NewEvent(resourceName, raw string, lim CalendarLimits) (Event, error) {
	if !ValidEventResourceName(resourceName) {
		return Event{}, ErrInvalidName
	}
	obj, err := ParseCalendarObject(raw, lim)
	if err != nil {
		return Event{}, err
	}
	return Event{
		ResourceName: resourceName,
		UID:          obj.UID,
		ICal:         raw,
		ETag:         ETagOf(raw),
		Summary:      obj.Summary,
		FirstStart:   obj.FirstStart,
		LastEnd:      obj.LastEnd,
	}, nil
}
