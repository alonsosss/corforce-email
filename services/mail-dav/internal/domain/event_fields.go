package domain

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// ProdID identifica al producto que escribe los objetos de calendario de la API estructurada (RFC 5545, 3.7.3).
	ProdID = "-//Core Force Mail//mail-dav//ES"

	// MaxEventLocationRunes y MaxEventDescriptionRunes acotan el lugar y la descripcion de un evento.
	MaxEventLocationRunes    = 500
	MaxEventDescriptionRunes = 10000
	// MaxEventTitleRunes es el largo maximo del titulo: el mismo con el que se indexa el resumen.
	MaxEventTitleRunes = maxSummaryRunes
	// MaxReminderMinutes acota el aviso de un evento: cuatro semanas antes.
	MaxReminderMinutes = 4 * 7 * 24 * 60
	// MaxRecurrenceInterval y MaxRecurrenceCount son los de una RRULE que el servidor acepta.
	MaxRecurrenceInterval = maxRuleInterval
	MaxRecurrenceCount    = maxRuleCount

	minEventYear = 1
	maxEventYear = maxCalendarYear
)

// Frecuencias de repeticion de la API estructurada.
var recurrenceFreqs = []string{"daily", "weekly", "monthly", "yearly"}

var weekdayCodes = [...]string{time.Sunday: "SU", time.Monday: "MO", time.Tuesday: "TU", time.Wednesday: "WE", time.Thursday: "TH", time.Friday: "FR", time.Saturday: "SA"}

// Recurrence es una repeticion simple: frecuencia, intervalo, fin por numero (Count) o por fecha (Until) y,
// opcionalmente, los dias de la semana (MO..SU).
type Recurrence struct {
	Freq     string
	Interval int
	Count    int
	Until    *time.Time
	ByDay    []string
}

// EventFields es un evento en la forma de la API estructurada. Start y End son instantes en UTC; en un evento
// de dia completo son la medianoche UTC del primer dia y del dia siguiente al ultimo (fin exclusivo, como DTEND).
// TimeZone es la zona IANA en la que se escriben las horas (vacio: UTC); con ella una serie semanal conserva su
// hora de pared a traves del cambio de horario. Organizer y Attendees son la reunion (iTIP): sin invitados no hay
// organizador.
type EventFields struct {
	Title           string
	Start           time.Time
	End             time.Time
	AllDay          bool
	TimeZone        string
	Location        string
	Description     string
	Recurrence      *Recurrence
	ReminderMinutes *int
	Organizer       *Party
	Attendees       []Attendee
}

// ParseEventTime lee una fecha de la API: RFC 3339, o en un evento de dia completo tambien AAAA-MM-DD. De
// un dia completo cuenta la fecha tal como se escribio (en su propio desfase), no el instante.
func ParseEventTime(field, value string, allDay bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fieldError(field, "es obligatorio")
	}
	var t time.Time
	var err error
	if allDay && len(value) == len("2006-01-02") {
		t, err = time.Parse("2006-01-02", value)
	} else {
		t, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		return time.Time{}, fieldError(field, "se espera una fecha RFC 3339")
	}
	if t.Year() < minEventYear || t.Year() > maxEventYear {
		return time.Time{}, fieldError(field, "fecha fuera de rango")
	}
	if allDay {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return t.UTC().Truncate(time.Second), nil
}

func (r *Recurrence) equal(o *Recurrence) bool {
	if r == nil || o == nil {
		return r == nil && o == nil
	}
	sameUntil := (r.Until == nil && o.Until == nil) || (r.Until != nil && o.Until != nil && r.Until.Equal(*o.Until))
	return r.Freq == o.Freq && r.Interval == o.Interval && r.Count == o.Count && sameUntil && slices.Equal(r.ByDay, o.ByDay)
}

func sameMinutes(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func normalizeRecurrence(in *Recurrence, start time.Time, allDay bool) (*Recurrence, error) {
	if in == nil {
		return nil, nil
	}
	r := &Recurrence{Freq: strings.ToLower(strings.TrimSpace(in.Freq)), Interval: in.Interval, Count: in.Count}
	if !slices.Contains(recurrenceFreqs, r.Freq) {
		return nil, fieldError("recurrence.freq", "se espera daily, weekly, monthly o yearly")
	}
	if r.Interval == 0 {
		r.Interval = 1
	}
	if r.Interval < 1 || r.Interval > MaxRecurrenceInterval {
		return nil, fieldError("recurrence.interval", "fuera de rango")
	}
	if r.Count < 0 || r.Count > MaxRecurrenceCount {
		return nil, fieldError("recurrence.count", "fuera de rango")
	}
	if in.Until != nil {
		if r.Count > 0 {
			return nil, fieldError("recurrence.until", "count y until no pueden ir juntos")
		}
		u := in.Until.UTC()
		if allDay {
			u = time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
		}
		if u.Before(start) {
			return nil, fieldError("recurrence.until", "es anterior al inicio")
		}
		r.Until = &u
	}
	for i, d := range in.ByDay {
		d = strings.ToUpper(strings.TrimSpace(d))
		if _, ok := weekdayIdx[d]; !ok {
			return nil, fieldError("recurrence.by_day["+strconv.Itoa(i)+"]", "se espera MO, TU, WE, TH, FR, SA o SU")
		}
		if !slices.Contains(r.ByDay, d) {
			r.ByDay = append(r.ByDay, d)
		}
	}
	return r, nil
}

// Normalize valida el evento y lo deja listo para escribirlo. Un evento de dia completo que termina el mismo
// dia en que empieza dura ese dia.
func (f EventFields) Normalize() (EventFields, error) {
	out := f
	out.Title = strings.TrimSpace(f.Title)
	out.Location = strings.TrimSpace(f.Location)
	out.Description = strings.TrimSpace(strings.ReplaceAll(f.Description, "\r\n", "\n"))
	if out.Title == "" {
		return EventFields{}, fieldError("title", "es obligatorio")
	}
	if err := validText("title", out.Title, MaxEventTitleRunes, false); err != nil {
		return EventFields{}, err
	}
	if err := validText("location", out.Location, MaxEventLocationRunes, false); err != nil {
		return EventFields{}, err
	}
	if err := validText("description", out.Description, MaxEventDescriptionRunes, true); err != nil {
		return EventFields{}, err
	}
	if strings.ContainsRune(out.Description, '\r') {
		return EventFields{}, fieldError("description", "contiene un retorno de carro suelto")
	}
	if out.Start.IsZero() {
		return EventFields{}, fieldError("start", "es obligatorio")
	}
	if out.End.IsZero() {
		return EventFields{}, fieldError("end", "es obligatorio")
	}
	if out.AllDay && out.End.Equal(out.Start) {
		out.End = out.Start.AddDate(0, 0, 1)
	}
	if out.End.Before(out.Start) {
		return EventFields{}, fieldError("end", "es anterior al inicio")
	}
	if out.End.Year() > maxEventYear {
		return EventFields{}, fieldError("end", "fecha fuera de rango")
	}
	rec, err := normalizeRecurrence(f.Recurrence, out.Start, out.AllDay)
	if err != nil {
		return EventFields{}, err
	}
	out.Recurrence = rec
	out.TimeZone = ""
	if tz := strings.TrimSpace(f.TimeZone); tz != "" && !out.AllDay {
		loc := ianaLocation(tz)
		if loc == nil {
			return EventFields{}, fieldError("timezone", "no es una zona horaria IANA")
		}
		out.TimeZone = loc.String()
	}
	if out.Organizer, err = normalizeParty("organizer", f.Organizer); err != nil {
		return EventFields{}, err
	}
	if out.Attendees, err = normalizeAttendees(f.Attendees); err != nil {
		return EventFields{}, err
	}
	if len(out.Attendees) == 0 {
		out.Attendees = nil
	}
	if f.ReminderMinutes != nil {
		if m := *f.ReminderMinutes; m < 0 || m > MaxReminderMinutes {
			return EventFields{}, fieldError("reminder_minutes", "fuera de rango")
		}
		m := *f.ReminderMinutes
		out.ReminderMinutes = &m
	}
	return out, nil
}

// rawComp es un componente de iCalendar con sus lineas originales, para reescribirlo conservando lo que el
// traductor no conoce.
type rawComp struct {
	name  string
	props []rawLine
	comps []*rawComp
}

// parseRawTree lee la estructura de un iCalendar ya validado.
func parseRawTree(raw string) *rawComp {
	var root *rawComp
	var stack []*rawComp
	for _, text := range unfold(raw) {
		l, ok := parseRawLine(text)
		if !ok {
			continue
		}
		switch l.name {
		case "BEGIN":
			c := &rawComp{name: strings.ToUpper(strings.TrimSpace(l.value))}
			if len(stack) == 0 {
				root = c
			} else {
				parent := stack[len(stack)-1]
				parent.comps = append(parent.comps, c)
			}
			stack = append(stack, c)
		case "END":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.props = append(top.props, l)
			}
		}
	}
	return root
}

func (c *rawComp) has(name string) bool {
	return slices.ContainsFunc(c.props, func(l rawLine) bool { return l.name == name })
}

func (c *rawComp) first(name string) (rawLine, bool) {
	for _, l := range c.props {
		if l.name == name {
			return l, true
		}
	}
	return rawLine{}, false
}

func (c *rawComp) write(w *contentWriter) {
	w.line("BEGIN:" + c.name)
	for _, l := range c.props {
		w.line(l.text)
	}
	for _, sub := range c.comps {
		sub.write(w)
	}
	w.line("END:" + c.name)
}

// master es el VEVENT sin RECURRENCE-ID (o el primero, si el objeto solo trae sobrescrituras).
func (c *rawComp) master() *rawComp {
	var first *rawComp
	for _, sub := range c.comps {
		if sub.name != componentEvent {
			continue
		}
		if !sub.has("RECURRENCE-ID") {
			return sub
		}
		if first == nil {
			first = sub
		}
	}
	return first
}

// reminderOf lee un VALARM como el aviso de la API: un disparo relativo al inicio, no posterior a el y en
// minutos enteros.
func reminderOf(alarm *rawComp) (int, bool) {
	if alarm.name != "VALARM" {
		return 0, false
	}
	if action, ok := alarm.first("ACTION"); ok {
		if a := strings.ToUpper(strings.TrimSpace(action.value)); a != "DISPLAY" && a != "AUDIO" {
			return 0, false
		}
	}
	trigger, ok := alarm.first("TRIGGER")
	if !ok || strings.EqualFold(trigger.param("VALUE"), "DATE-TIME") || strings.EqualFold(trigger.param("RELATED"), "END") {
		return 0, false
	}
	d, err := parseDuration(trigger.value)
	if err != nil || d > 0 || d%time.Minute != 0 || -d > MaxReminderMinutes*time.Minute {
		return 0, false
	}
	return int(-d / time.Minute), true
}

func (c *rawComp) reminder() (*int, int) {
	for i, sub := range c.comps {
		if m, ok := reminderOf(sub); ok {
			return &m, i
		}
	}
	return nil, -1
}

func (o CalendarObject) masterEvent() eventComponent {
	for _, c := range o.events {
		if c.recurrenceID == nil {
			return c
		}
	}
	return o.events[0]
}

func (c eventComponent) text(name string) string {
	for _, p := range c.props {
		if p.Name == name {
			return strings.TrimSpace(p.Value)
		}
	}
	return ""
}

func recurrenceOf(c eventComponent, zones zoneSet) *Recurrence {
	rule := c.rrule
	if rule == nil || rule.Unsupported || !slices.Contains(recurrenceFreqs, strings.ToLower(rule.Freq)) {
		return nil
	}
	r := &Recurrence{Freq: strings.ToLower(rule.Freq), Interval: rule.Interval, Count: rule.Count}
	if u := rule.Until; u != nil {
		t := u.wall
		if !u.utc && !u.date {
			t = zones.toInstant(c.start.tzid, u.wall)
		}
		r.Until = &t
	}
	for _, d := range rule.ByDay {
		if code := weekdayCodes[d.Day]; d.N == 0 && !slices.Contains(r.ByDay, code) {
			r.ByDay = append(r.ByDay, code)
		}
	}
	return r
}

// EventFieldsOf lee un objeto de calendario guardado en la forma de la API estructurada: su VEVENT principal.
// Una regla de repeticion que la API no puede expresar (frecuencias menores que un dia, partes de otras
// extensiones) se lee como sin repeticion; lo que se lee de menos se conserva al escribir (BuildCalendarObject).
func EventFieldsOf(raw string) (EventFields, error) {
	obj, err := ParseStoredCalendarObject(raw)
	if err != nil {
		return EventFields{}, err
	}
	return obj.fields(parseRawTree(raw)), nil
}

func (o CalendarObject) fields(tree *rawComp) EventFields {
	m := o.masterEvent()
	start := o.zones.instant(m.start)
	f := EventFields{
		Title: m.text("SUMMARY"), Location: m.text("LOCATION"), Description: m.text("DESCRIPTION"),
		Start: start, End: start.Add(m.dur), AllDay: m.start.date,
		Recurrence: recurrenceOf(m, o.zones),
	}
	if tree != nil {
		if raw := tree.master(); raw != nil {
			f.ReminderMinutes, _ = raw.reminder()
			f.Organizer = raw.organizer()
			f.Attendees = raw.attendees()
			if dt, ok := raw.first("DTSTART"); ok && !m.start.date {
				if loc := ianaLocation(dt.param("TZID")); loc != nil && loc != time.UTC {
					f.TimeZone = loc.String()
				}
			}
		}
	}
	return f
}

// zoneOf es la zona en la que se escriben las horas de f: nil es UTC.
func zoneOf(f EventFields) *time.Location {
	if f.AllDay || f.TimeZone == "" {
		return nil
	}
	if loc := ianaLocation(f.TimeZone); loc != nil && loc != time.UTC {
		return loc
	}
	return nil
}

// meetingLines son el organizador y los invitados de una reunion; sin invitados no hay reunion.
func meetingLines(org *Party, attendees []Attendee) []rawLine {
	if len(attendees) == 0 {
		return nil
	}
	var out []rawLine
	if org != nil {
		out = append(out, generated(organizerLine(*org)))
	}
	for _, a := range attendees {
		out = append(out, generated(attendeeLine(a)))
	}
	return out
}

func formatWhen(prop string, t time.Time, allDay bool, loc *time.Location) string {
	switch {
	case allDay:
		return prop + ";VALUE=DATE:" + t.UTC().Format(layoutDate)
	case loc != nil:
		return prop + ";TZID=" + loc.String() + ":" + t.In(loc).Format(layoutDateTime)
	}
	return prop + ":" + t.UTC().Format(layoutStamp)
}

func formatRRule(r *Recurrence, allDay bool) string {
	parts := []string{"FREQ=" + strings.ToUpper(r.Freq)}
	if r.Interval > 1 {
		parts = append(parts, "INTERVAL="+strconv.Itoa(r.Interval))
	}
	if r.Count > 0 {
		parts = append(parts, "COUNT="+strconv.Itoa(r.Count))
	}
	if r.Until != nil {
		// UNTIL es del mismo tipo que DTSTART: fecha en un dia completo y UTC en los demas (RFC 5545, 3.3.10).
		if allDay {
			parts = append(parts, "UNTIL="+r.Until.UTC().Format(layoutDate))
		} else {
			parts = append(parts, "UNTIL="+r.Until.UTC().Format(layoutStamp))
		}
	}
	if len(r.ByDay) > 0 {
		parts = append(parts, "BYDAY="+strings.Join(r.ByDay, ","))
	}
	return "RRULE:" + strings.Join(parts, ";")
}

func newAlarm(title string, minutes int) *rawComp {
	lines := []string{"ACTION:DISPLAY", "DESCRIPTION:" + escapeText(title), "TRIGGER:-PT" + strconv.Itoa(minutes) + "M"}
	c := &rawComp{name: "VALARM"}
	for _, text := range lines {
		l, _ := parseRawLine(text)
		c.props = append(c.props, l)
	}
	return c
}

func generated(text string) rawLine {
	l, _ := parseRawLine(text)
	return l
}

// BuildCalendarObject escribe el iCalendar de un evento. Sin existing es un VCALENDAR nuevo con un VEVENT de
// ese UID, con las horas en UTC (o fechas, en un dia completo). Con existing (un objeto ya guardado) cambia
// solo el VEVENT principal y conserva todo lo que el traductor no conoce: propiedades (ATTENDEE, X-*,
// CATEGORIES...), otros VALARM, VTIMEZONE y la linea original de cada valor que no cambio. Si cambia el inicio,
// el tipo de dia o la repeticion, las excepciones de la serie (EXDATE, RDATE y los VEVENT con RECURRENCE-ID)
// se retiran: nombran apariciones que ya no existen. Si el inicio tenia una zona IANA, las horas nuevas se
// escriben en esa zona, de modo que una serie semanal no se desplaza con el horario de verano. f debe venir
// normalizado.
func BuildCalendarObject(existing, uid string, f EventFields, now time.Time) (string, error) {
	stamp := now.UTC().Format(layoutStamp)
	if existing == "" {
		loc := zoneOf(f)
		var w contentWriter
		w.line("BEGIN:VCALENDAR")
		w.line("VERSION:2.0")
		w.line("PRODID:" + ProdID)
		w.line("CALSCALE:GREGORIAN")
		if loc != nil {
			buildVTimezone(loc, f.Start).write(&w)
		}
		ev := &rawComp{name: componentEvent}
		for _, text := range []string{"UID:" + escapeText(uid), "DTSTAMP:" + stamp, "CREATED:" + stamp, "LAST-MODIFIED:" + stamp, "SEQUENCE:0"} {
			ev.props = append(ev.props, generated(text))
		}
		ev.props = append(ev.props, generated(formatWhen("DTSTART", f.Start, f.AllDay, loc)), generated(formatWhen("DTEND", f.End, f.AllDay, loc)))
		for _, p := range []struct{ name, value string }{{"SUMMARY", f.Title}, {"LOCATION", f.Location}, {"DESCRIPTION", f.Description}} {
			if p.value != "" {
				ev.props = append(ev.props, generated(p.name+":"+escapeText(p.value)))
			}
		}
		if f.Recurrence != nil {
			ev.props = append(ev.props, generated(formatRRule(f.Recurrence, f.AllDay)))
		}
		ev.props = append(ev.props, meetingLines(f.Organizer, f.Attendees)...)
		if f.ReminderMinutes != nil {
			ev.comps = append(ev.comps, newAlarm(f.Title, *f.ReminderMinutes))
		}
		ev.write(&w)
		w.line("END:VCALENDAR")
		return w.String(), nil
	}

	obj, err := ParseStoredCalendarObject(existing)
	if err != nil {
		return "", err
	}
	tree := parseRawTree(existing)
	master := tree.master()
	old := obj.fields(tree)

	var loc *time.Location
	if dt, ok := master.first("DTSTART"); ok && !f.AllDay {
		if loc = ianaLocation(dt.param("TZID")); loc == time.UTC {
			loc = nil
		}
	}
	zoneChanged := false
	if f.TimeZone != "" && !f.AllDay {
		next := zoneOf(f)
		zoneChanged = next.String() != loc.String() || (next == nil) != (loc == nil)
		loc = next
	}

	startChanged := !old.Start.Equal(f.Start) || old.AllDay != f.AllDay || zoneChanged
	endChanged := startChanged || !old.End.Equal(f.End)
	seriesChanged := startChanged || !old.Recurrence.equal(f.Recurrence)
	keepRule := old.AllDay == f.AllDay && old.Recurrence.equal(f.Recurrence)

	oldOrganizer := master.organizer()
	organizer := oldOrganizer
	if organizer == nil {
		organizer = f.Organizer
	}
	// Solo el organizador pide de nuevo respuesta al cambiar la hora; la copia de un invitado no la toca.
	isOrganizer := oldOrganizer == nil || (f.Organizer != nil && f.Organizer.Email == oldOrganizer.Email)
	resetReplies := isOrganizer && (endChanged || seriesChanged)
	sequence := 0
	if s, ok := master.first("SEQUENCE"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s.value)); err == nil && n >= 0 {
			sequence = n
		}
	}
	if endChanged || seriesChanged {
		sequence++
	}

	var props []rawLine
	for _, l := range master.props {
		drop := false
		switch l.name {
		case "DTSTART":
			drop = startChanged
		case "DTEND", "DURATION":
			drop = endChanged
		case "SUMMARY":
			drop = old.Title != f.Title
		case "LOCATION":
			drop = old.Location != f.Location
		case "DESCRIPTION":
			drop = old.Description != f.Description
		case "RRULE":
			drop = !keepRule
		case "EXDATE", "RDATE":
			drop = seriesChanged
		case "DTSTAMP", "LAST-MODIFIED", "SEQUENCE", "ATTENDEE":
			drop = true
		case "ORGANIZER":
			drop = len(f.Attendees) == 0
		}
		if !drop {
			props = append(props, l)
		}
	}
	for _, text := range []string{"DTSTAMP:" + stamp, "LAST-MODIFIED:" + stamp, "SEQUENCE:" + strconv.Itoa(sequence)} {
		props = append(props, generated(text))
	}
	if startChanged {
		props = append(props, generated(formatWhen("DTSTART", f.Start, f.AllDay, loc)))
	}
	if endChanged {
		props = append(props, generated(formatWhen("DTEND", f.End, f.AllDay, loc)))
	}
	for _, p := range []struct{ name, old, value string }{
		{"SUMMARY", old.Title, f.Title}, {"LOCATION", old.Location, f.Location}, {"DESCRIPTION", old.Description, f.Description},
	} {
		if p.value != p.old && p.value != "" {
			props = append(props, generated(p.name+":"+escapeText(p.value)))
		}
	}
	if !keepRule && f.Recurrence != nil {
		props = append(props, generated(formatRRule(f.Recurrence, f.AllDay)))
	}
	if len(f.Attendees) > 0 {
		organizerEmail := ""
		if organizer != nil {
			organizerEmail = organizer.Email
			if oldOrganizer == nil {
				props = append(props, generated(organizerLine(*organizer)))
			}
		}
		props = append(props, rewriteAttendees(master, f.Attendees, organizerEmail, resetReplies)...)
	}
	master.props = props
	if loc != nil && (startChanged || endChanged) {
		ensureVTimezone(tree, loc, f.Start)
	}

	if oldMinutes, idx := master.reminder(); !sameMinutes(oldMinutes, f.ReminderMinutes) || (oldMinutes != nil && old.Title != f.Title) {
		if idx >= 0 {
			master.comps = slices.Delete(master.comps, idx, idx+1)
		}
		if f.ReminderMinutes != nil {
			master.comps = append(master.comps, newAlarm(f.Title, *f.ReminderMinutes))
		}
	}

	var w contentWriter
	w.line("BEGIN:VCALENDAR")
	for _, l := range tree.props {
		w.line(l.text)
	}
	for _, c := range tree.comps {
		if c != master && c.name == componentEvent && seriesChanged && c.has("RECURRENCE-ID") {
			continue
		}
		c.write(&w)
	}
	w.line("END:VCALENDAR")
	return w.String(), nil
}

// rewriteAttendees escribe los invitados en el orden pedido. Uno que no cambio conserva su linea original (con
// los parametros que el traductor no conoce); si el organizador cambio la hora, los demas vuelven a
// NEEDS-ACTION con RSVP para que respondan de nuevo.
func rewriteAttendees(master *rawComp, attendees []Attendee, organizer string, reset bool) []rawLine {
	old := map[string]rawLine{}
	oldParsed := map[string]Attendee{}
	for _, l := range master.props {
		if l.name != "ATTENDEE" {
			continue
		}
		if email, ok := mailtoAddress(l.value); ok {
			if _, dup := old[email]; !dup {
				old[email] = l
			}
		}
	}
	for _, a := range master.attendees() {
		oldParsed[a.Email] = a
	}
	out := make([]rawLine, 0, len(attendees))
	for _, a := range attendees {
		if reset && a.Email != organizer {
			a.PartStat = PartStatNeedsAction
		}
		if l, ok := old[a.Email]; ok {
			if prev := oldParsed[a.Email]; prev.Name == a.Name && prev.PartStat == a.PartStat {
				out = append(out, l)
				continue
			}
		}
		out = append(out, generated(attendeeLine(a)))
	}
	return out
}

// ensureVTimezone anade el VTIMEZONE de la zona si el objeto no lo trae: todo TZID que se escribe debe tener el
// suyo (RFC 5545, 3.6.5).
func ensureVTimezone(tree *rawComp, loc *time.Location, from time.Time) {
	for _, c := range tree.comps {
		if c.name != "VTIMEZONE" {
			continue
		}
		if l, ok := c.first("TZID"); ok && strings.TrimSpace(l.value) == loc.String() {
			return
		}
	}
	tree.comps = append([]*rawComp{buildVTimezone(loc, from)}, tree.comps...)
}
