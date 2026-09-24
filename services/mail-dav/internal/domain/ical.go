package domain

import (
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxICalDepth cubre VCALENDAR > VEVENT > VALARM y VCALENDAR > VTIMEZONE > STANDARD.
	maxICalDepth = 3
	// maxRecurrenceDates acota los RDATE y EXDATE de un evento: cada uno es una fecha que hay que guardar
	// y comparar.
	maxRecurrenceDates = 2000
	maxSummaryRunes    = 300
	// maxIndexWork acota la expansion que calcula el ultimo fin de un evento con COUNT al guardarlo.
	maxIndexWork = 1 << 20
)

var icalNameRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func icalError(kind ICalErrorKind, reason string) *ICalError {
	return &ICalError{Kind: kind, Reason: reason}
}

func invalid(reason string) *ICalError { return icalError(ICalInvalid, reason) }

type icalProp struct {
	Name   string
	Params map[string]string
	Value  string
}

type icalComp struct {
	Name  string
	Props []icalProp
	Comps []*icalComp
}

// first devuelve la primera propiedad con ese nombre y cuantas hay.
func (c *icalComp) first(name string) (icalProp, int) {
	var found icalProp
	n := 0
	for _, p := range c.Props {
		if p.Name == name {
			if n == 0 {
				found = p
			}
			n++
		}
	}
	return found, n
}

// splitOutsideQuotes parte por sep sin cortar dentro de comillas.
func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	inQuote, start := false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case s[i] == sep && !inQuote:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// parseContentHead separa el nombre de una propiedad iCalendar de sus parametros.
func parseContentHead(head string) (string, map[string]string, *ICalError) {
	parts := splitOutsideQuotes(head, ';')
	name := strings.ToUpper(parts[0])
	if !icalNameRe.MatchString(name) {
		return "", nil, invalid("nombre de propiedad no válido")
	}
	var params map[string]string
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		k = strings.ToUpper(k)
		if !ok || !icalNameRe.MatchString(k) {
			return "", nil, invalid("parámetro mal formado")
		}
		if params == nil {
			params = map[string]string{}
		}
		params[k] = strings.Trim(v, `"`)
	}
	return name, params, nil
}

// parseICalTree valida la estructura de un iCalendar que llega de un tercero: acotado en bytes, lineas
// y anidamiento, UTF-8 sin caracteres de control, y un solo VCALENDAR con sus BEGIN y END emparejados.
// No interpreta valores: eso lo hace ParseCalendarObject.
func parseICalTree(raw string, lim CalendarLimits) (*icalComp, *ICalError) {
	if len(raw) > lim.MaxEventBytes {
		return nil, icalError(ICalTooLarge, "el evento supera el tamaño máximo")
	}
	if !utf8.ValidString(raw) {
		return nil, invalid("no es UTF-8 válido")
	}
	if reason := controlProblem(raw); reason != "" {
		return nil, invalid(reason)
	}
	lines := unfold(raw)
	if len(lines) > lim.MaxEventProperties {
		return nil, invalid("demasiadas propiedades")
	}
	var root *icalComp
	var stack []*icalComp
	for _, line := range lines {
		head, value, ok := splitContentLine(line)
		if !ok {
			return nil, invalid("línea sin nombre o sin valor")
		}
		name, params, err := parseContentHead(head)
		if err != nil {
			return nil, err
		}
		switch name {
		case "BEGIN":
			cname := strings.ToUpper(strings.TrimSpace(value))
			if !icalNameRe.MatchString(cname) {
				return nil, invalid("nombre de componente no válido")
			}
			if len(stack) == 0 && (root != nil || cname != "VCALENDAR") {
				return nil, invalid("el objeto debe ser un único VCALENDAR")
			}
			if len(stack) >= maxICalDepth {
				return nil, invalid("componentes anidados en exceso")
			}
			c := &icalComp{Name: cname}
			if len(stack) == 0 {
				root = c
			} else {
				parent := stack[len(stack)-1]
				parent.Comps = append(parent.Comps, c)
			}
			stack = append(stack, c)
		case "END":
			if len(stack) == 0 || !strings.EqualFold(strings.TrimSpace(value), stack[len(stack)-1].Name) {
				return nil, invalid("END sin su BEGIN")
			}
			stack = stack[:len(stack)-1]
		default:
			if len(stack) == 0 {
				return nil, invalid("propiedad fuera de un componente")
			}
			top := stack[len(stack)-1]
			top.Props = append(top.Props, icalProp{Name: name, Params: params, Value: value})
		}
	}
	if root == nil || len(stack) > 0 {
		return nil, invalid("debe empezar con BEGIN:VCALENDAR y terminar con END:VCALENDAR")
	}
	return root, nil
}

// eventComponent es un VEVENT ya interpretado: lo que hace falta para indexarlo y decidir si una consulta
// lo alcanza. Approx marca lo que no se sabe evaluar con exactitud (RDATE con periodos, RANGE en un
// RECURRENCE-ID): ante una consulta por rango, el evento no se descarta.
type eventComponent struct {
	props        []Property
	start        dtValue
	dur          time.Duration
	rrule        *RRule
	rdates       []dtValue
	exdates      []dtValue
	recurrenceID *dtValue
	approx       bool
}

// CalendarObject es un iCalendar validado con un VEVENT (y sus sobrescrituras). El texto original no se
// toca: se guarda y se devuelve tal como lo envio el cliente.
type CalendarObject struct {
	UID        string
	Summary    string
	FirstStart time.Time
	LastEnd    *time.Time
	zones      zoneSet
	events     []eventComponent
}

// ParseCalendarObject valida un iCalendar de un PUT segun RFC 4791 (5.3.2.1): un VCALENDAR 2.0 sin METHOD,
// con VEVENT de un mismo UID (a lo sumo uno sin RECURRENCE-ID), DTSTART obligatorio y VTIMEZONE bien
// formados. Solo se admite VEVENT: las tareas y los diarios se rechazan con supported-calendar-component.
func ParseCalendarObject(raw string, lim CalendarLimits) (CalendarObject, error) {
	obj, err := parseCalendarObject(raw, lim)
	if err != nil {
		return CalendarObject{}, err
	}
	obj.index(NewBudget(maxIndexWork))
	return obj, nil
}

func parseCalendarObject(raw string, lim CalendarLimits) (CalendarObject, error) {
	tree, terr := parseICalTree(raw, lim)
	if terr != nil {
		return CalendarObject{}, terr
	}
	if v, n := tree.first("VERSION"); n != 1 || strings.TrimSpace(v.Value) != "2.0" {
		return CalendarObject{}, invalid("solo se admite VERSION 2.0")
	}
	if _, n := tree.first("METHOD"); n > 0 {
		return CalendarObject{}, icalError(ICalObject, "un objeto de calendario no lleva METHOD")
	}
	if cs, n := tree.first("CALSCALE"); n > 1 || (n == 1 && !strings.EqualFold(strings.TrimSpace(cs.Value), "GREGORIAN")) {
		return CalendarObject{}, invalid("solo se admite el calendario gregoriano")
	}

	obj := CalendarObject{zones: zoneSet{custom: map[string]*vtimezone{}}}
	for _, child := range tree.Comps {
		if child.Name != "VTIMEZONE" {
			continue
		}
		tzid, tz, err := parseVTimezone(child)
		if err != nil {
			return CalendarObject{}, err
		}
		if _, dup := obj.zones.custom[tzid]; dup {
			return CalendarObject{}, invalid("VTIMEZONE repetido")
		}
		obj.zones.custom[tzid] = tz
	}
	masters := 0
	for _, child := range tree.Comps {
		switch child.Name {
		case "VTIMEZONE":
		case "VEVENT":
			ev, err := parseEvent(child, obj.zones)
			if err != nil {
				return CalendarObject{}, err
			}
			uid, _ := child.first("UID")
			if value := strings.TrimSpace(uid.Value); obj.UID == "" {
				obj.UID = value
			} else if value != obj.UID {
				return CalendarObject{}, icalError(ICalObject, "todos los VEVENT deben tener el mismo UID")
			}
			if ev.recurrenceID == nil {
				masters++
			}
			obj.events = append(obj.events, ev)
		default:
			return CalendarObject{}, icalError(ICalComponent, "solo se admiten eventos (VEVENT)")
		}
	}
	if len(obj.events) == 0 {
		return CalendarObject{}, icalError(ICalObject, "el objeto no tiene ningún VEVENT")
	}
	if masters > 1 {
		return CalendarObject{}, icalError(ICalObject, "un objeto tiene a lo sumo un VEVENT sin RECURRENCE-ID")
	}
	obj.Summary = obj.summary()
	return obj, nil
}

// ParseStoredCalendarObject vuelve a leer un objeto ya guardado para evaluar una consulta. No aplica los
// topes de hoy: uno aceptado bajo limites mas holgados sigue siendo un objeto valido. No calcula el
// indice (FirstStart y LastEnd, que ya estan en la base): hacerlo expande la recurrencia de cada evento
// con un presupuesto de un millon de unidades, y una consulta relee todos los candidatos.
func ParseStoredCalendarObject(raw string) (CalendarObject, error) {
	return parseCalendarObject(raw, CalendarLimits{MaxEventBytes: len(raw), MaxEventProperties: math.MaxInt})
}

func (o CalendarObject) summary() string {
	for _, c := range o.events {
		for _, p := range c.props {
			if p.Name == "SUMMARY" {
				return truncateRunes(strings.TrimSpace(p.Value), maxSummaryRunes)
			}
		}
	}
	return ""
}

// parseDateList lee el valor de un RDATE o un EXDATE: una lista de fechas o de periodos. Un periodo
// (VALUE=PERIOD) es valido pero no se evalua: devuelve approx.
func parseDateList(p icalProp) (dates []dtValue, approx bool, err *ICalError) {
	if strings.EqualFold(p.Params["VALUE"], "PERIOD") {
		return nil, true, nil
	}
	for _, part := range strings.Split(p.Value, ",") {
		d, perr := parseDateTime(part, p.Params)
		if perr != nil {
			return nil, false, invalid(p.Name + ": " + perr.Error())
		}
		dates = append(dates, d)
	}
	return dates, false, nil
}

func parseEvent(c *icalComp, zones zoneSet) (eventComponent, *ICalError) {
	var ev eventComponent
	single := func(name string) (icalProp, bool, *ICalError) {
		p, n := c.first(name)
		if n > 1 {
			return p, false, invalid(name + " repetido")
		}
		return p, n == 1, nil
	}
	uid, ok, err := single("UID")
	if err != nil {
		return ev, err
	}
	if v := strings.TrimSpace(uid.Value); !ok || v == "" || len(v) > maxUIDLength {
		return ev, invalid("el VEVENT debe traer un UID de hasta 255 caracteres")
	}
	for _, sub := range c.Comps {
		if sub.Name != "VALARM" {
			return ev, invalid("un VEVENT solo puede contener VALARM")
		}
	}
	ev.props = make([]Property, 0, len(c.Props))
	for _, p := range c.Props {
		ev.props = append(ev.props, Property{Name: p.Name, Value: unescapeText(p.Value)})
	}

	dtstart, ok, err := single("DTSTART")
	if err != nil {
		return ev, err
	}
	if !ok {
		return ev, invalid("el VEVENT debe traer DTSTART")
	}
	start, perr := parseDateTime(dtstart.Value, dtstart.Params)
	if perr != nil {
		return ev, invalid("DTSTART: " + perr.Error())
	}
	ev.start = start

	dtend, hasEnd, err := single("DTEND")
	if err != nil {
		return ev, err
	}
	duration, hasDuration, err := single("DURATION")
	if err != nil {
		return ev, err
	}
	if hasEnd && hasDuration {
		return ev, invalid("DTEND y DURATION no pueden ir juntos")
	}
	switch {
	case hasEnd:
		end, perr := parseDateTime(dtend.Value, dtend.Params)
		if perr != nil {
			return ev, invalid("DTEND: " + perr.Error())
		}
		if end.date != start.date {
			return ev, invalid("DTEND debe ser del mismo tipo que DTSTART")
		}
		if ev.dur = zones.instant(end).Sub(zones.instant(start)); ev.dur < 0 {
			return ev, invalid("DTEND anterior a DTSTART")
		}
	case hasDuration:
		d, perr := parseDuration(duration.Value)
		if perr != nil {
			return ev, invalid("DURATION: " + perr.Error())
		}
		if d < 0 {
			return ev, invalid("DURATION negativa")
		}
		ev.dur = d
	case start.date:
		ev.dur = 24 * time.Hour
	}

	if rid, ok, err := single("RECURRENCE-ID"); err != nil {
		return ev, err
	} else if ok {
		d, perr := parseDateTime(rid.Value, rid.Params)
		if perr != nil {
			return ev, invalid("RECURRENCE-ID: " + perr.Error())
		}
		ev.recurrenceID = &d
		ev.approx = rid.Params["RANGE"] != ""
	}
	if rule, ok, err := single("RRULE"); err != nil {
		return ev, err
	} else if ok {
		r, perr := ParseRRule(rule.Value)
		if perr != nil {
			return ev, invalid("RRULE: " + perr.Error())
		}
		ev.rrule = &r
	}
	for _, p := range c.Props {
		if p.Name != "RDATE" && p.Name != "EXDATE" {
			continue
		}
		dates, approx, err := parseDateList(p)
		if err != nil {
			return ev, err
		}
		if p.Name == "RDATE" {
			ev.rdates = append(ev.rdates, dates...)
			ev.approx = ev.approx || approx
		} else {
			ev.exdates = append(ev.exdates, dates...)
		}
		if len(ev.rdates)+len(ev.exdates) > maxRecurrenceDates {
			return ev, invalid("demasiadas fechas de recurrencia")
		}
	}
	return ev, nil
}

func parseVTimezone(c *icalComp) (string, *vtimezone, *ICalError) {
	tzid, n := c.first("TZID")
	id := strings.TrimSpace(tzid.Value)
	if n != 1 || id == "" || len(id) > maxUIDLength {
		return "", nil, invalid("el VTIMEZONE debe traer un TZID")
	}
	tz := &vtimezone{}
	for _, sub := range c.Comps {
		if sub.Name != "STANDARD" && sub.Name != "DAYLIGHT" {
			return "", nil, invalid("un VTIMEZONE solo contiene STANDARD y DAYLIGHT")
		}
		obs, err := parseObservance(sub)
		if err != nil {
			return "", nil, err
		}
		tz.observances = append(tz.observances, obs)
	}
	if len(tz.observances) == 0 {
		return "", nil, invalid("el VTIMEZONE no define ningún STANDARD ni DAYLIGHT")
	}
	return id, tz, nil
}

func parseObservance(c *icalComp) (observance, *ICalError) {
	var o observance
	dtstart, n := c.first("DTSTART")
	if n != 1 {
		return o, invalid(c.Name + " sin DTSTART")
	}
	start, err := parseDateTime(dtstart.Value, nil)
	if err != nil || start.utc || start.date {
		return o, invalid(c.Name + ": DTSTART debe ser una hora local")
	}
	o.onset = start
	from, nf := c.first("TZOFFSETFROM")
	to, nt := c.first("TZOFFSETTO")
	if nf != 1 || nt != 1 {
		return o, invalid(c.Name + " sin TZOFFSETFROM o TZOFFSETTO")
	}
	var perr error
	if o.from, perr = parseUTCOffset(from.Value); perr != nil {
		return o, invalid(c.Name + ": " + perr.Error())
	}
	if o.to, perr = parseUTCOffset(to.Value); perr != nil {
		return o, invalid(c.Name + ": " + perr.Error())
	}
	if rule, nr := c.first("RRULE"); nr > 1 {
		return o, invalid(c.Name + ": RRULE repetido")
	} else if nr == 1 {
		r, err := ParseRRule(rule.Value)
		if err != nil {
			return o, invalid(c.Name + ": RRULE: " + err.Error())
		}
		o.rule = &r
	}
	for _, p := range c.Props {
		if p.Name != "RDATE" {
			continue
		}
		dates, _, err := parseDateList(p)
		if err != nil {
			return o, err
		}
		for _, d := range dates {
			o.rdates = append(o.rdates, d.wall)
		}
		if len(o.rdates) > maxRecurrenceDates {
			return o, invalid(c.Name + ": demasiados RDATE")
		}
	}
	return o, nil
}
