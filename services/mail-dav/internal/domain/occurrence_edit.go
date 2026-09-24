package domain

import (
	"strconv"
	"strings"
	"time"
)

func (c *rawComp) sequence() int {
	if s, ok := c.first("SEQUENCE"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s.value)); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}

// touch renueva DTSTAMP y LAST-MODIFIED del componente y, si seq no es negativo, fija su SEQUENCE.
func (c *rawComp) touch(now time.Time, seq int) {
	stamp := now.UTC().Format(layoutStamp)
	props := c.props[:0:0]
	for _, l := range c.props {
		switch l.name {
		case "DTSTAMP", "LAST-MODIFIED":
			continue
		case "SEQUENCE":
			if seq >= 0 {
				continue
			}
		}
		props = append(props, l)
	}
	props = append(props, generated("DTSTAMP:"+stamp), generated("LAST-MODIFIED:"+stamp))
	if seq >= 0 {
		props = append(props, generated("SEQUENCE:"+strconv.Itoa(seq)))
	}
	c.props = props
}

// seriesTarget es un objeto guardado listo para cambiar una aparicion de su serie: el objeto interpretado, su
// arbol de lineas, el VEVENT principal en las dos formas y cada VEVENT del arbol con su interpretacion.
type seriesTarget struct {
	obj    CalendarObject
	tree   *rawComp
	master *rawComp
	main   eventComponent
	pairs  []eventPair
}

type eventPair struct {
	raw *rawComp
	ev  eventComponent
}

func loadSeries(existing string) (seriesTarget, error) {
	obj, err := ParseStoredCalendarObject(existing)
	if err != nil {
		return seriesTarget{}, err
	}
	t := seriesTarget{obj: obj, tree: parseRawTree(existing)}
	i := 0
	for _, c := range t.tree.comps {
		if c.name != componentEvent {
			continue
		}
		if i < len(obj.events) {
			t.pairs = append(t.pairs, eventPair{raw: c, ev: obj.events[i]})
			if obj.events[i].recurrenceID == nil {
				t.master, t.main = c, obj.events[i]
			}
		}
		i++
	}
	if t.master == nil || (t.main.rrule == nil && len(t.main.rdates) == 0) {
		return seriesTarget{}, fieldError("recurrence_id", "el evento no se repite")
	}
	return t, nil
}

// hasOccurrence dice si rid es una aparicion viva de la serie: de la regla (sin las EXDATE) o una sobrescritura.
func (t seriesTarget) hasOccurrence(rid time.Time) bool {
	for _, p := range t.pairs {
		if p.ev.recurrenceID != nil && t.obj.zones.instant(*p.ev.recurrenceID).Equal(rid) {
			return true
		}
	}
	end := rid.Add(time.Second)
	for _, o := range t.obj.Occurrences(TimeRange{Start: &rid, End: &end}, NewBudget(maxIndexWork)) {
		if o.RecurrenceID.Equal(rid) {
			return true
		}
	}
	return false
}

// withoutOverride quita del arbol la sobrescritura de rid, si la hay.
func (t seriesTarget) withoutOverride(rid time.Time) {
	comps := t.tree.comps[:0:0]
	for _, c := range t.tree.comps {
		drop := false
		for _, p := range t.pairs {
			if p.raw == c && p.ev.recurrenceID != nil && t.obj.zones.instant(*p.ev.recurrenceID).Equal(rid) {
				drop = true
			}
		}
		if !drop {
			comps = append(comps, c)
		}
	}
	t.tree.comps = comps
}

func (t seriesTarget) serialize() string {
	var w contentWriter
	t.tree.write(&w)
	return w.String()
}

// SetOccurrence cambia una sola aparicion de una serie: escribe (o reemplaza) su sobrescritura, un VEVENT con el
// mismo UID y RECURRENCE-ID igual a rid, con las horas en la zona de la serie y los invitados de la serie. La
// serie no cambia; su SEQUENCE sube para que una invitacion enviada despues se tenga por nueva. f debe venir
// normalizado y conservar el tipo de dia de la serie. Una rid que no es una aparicion viva es ErrNotFound.
func SetOccurrence(existing string, rid time.Time, f EventFields, now time.Time) (string, error) {
	t, err := loadSeries(existing)
	if err != nil {
		return "", err
	}
	if f.AllDay != t.main.start.date {
		return "", fieldError("all_day", "una aparicion conserva el tipo de dia de la serie")
	}
	rid = rid.UTC()
	if !t.hasOccurrence(rid) {
		return "", ErrNotFound
	}
	seq := t.master.sequence() + 1
	t.master.touch(now, seq)
	t.withoutOverride(rid)

	like, zones := t.main.start, t.obj.zones
	ov := &rawComp{name: componentEvent}
	if l, ok := t.master.first("UID"); ok {
		ov.props = append(ov.props, l)
	}
	ov.props = append(ov.props, generated(zones.formatLike("RECURRENCE-ID", like, rid)))
	if f.AllDay {
		ov.props = append(ov.props, generated(formatWhen("DTSTART", f.Start, true, nil)), generated(formatWhen("DTEND", f.End, true, nil)))
	} else {
		ov.props = append(ov.props, generated(zones.formatLike("DTSTART", like, f.Start)), generated(zones.formatLike("DTEND", like, f.End)))
	}
	for _, p := range []struct{ name, value string }{{"SUMMARY", f.Title}, {"LOCATION", f.Location}, {"DESCRIPTION", f.Description}} {
		if p.value != "" {
			ov.props = append(ov.props, generated(p.name+":"+escapeText(p.value)))
		}
	}
	for _, l := range t.master.props {
		if l.name == "ORGANIZER" || l.name == "ATTENDEE" {
			ov.props = append(ov.props, l)
		}
	}
	ov.touch(now, seq)
	t.tree.comps = append(t.tree.comps, ov)
	return t.serialize(), nil
}

// DeleteOccurrence borra una sola aparicion de una serie: anade su EXDATE (en la forma del DTSTART de la serie)
// y retira su sobrescritura, si la tenia. Una rid que no es una aparicion viva es ErrNotFound.
func DeleteOccurrence(existing string, rid time.Time, now time.Time) (string, error) {
	t, err := loadSeries(existing)
	if err != nil {
		return "", err
	}
	rid = rid.UTC()
	if !t.hasOccurrence(rid) {
		return "", ErrNotFound
	}
	t.withoutOverride(rid)
	t.master.props = append(t.master.props, generated(t.obj.zones.formatLike("EXDATE", t.main.start, rid)))
	t.master.touch(now, t.master.sequence()+1)
	return t.serialize(), nil
}
