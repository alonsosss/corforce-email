package domain

import (
	"strings"
	"time"
)

// Metodos de iTIP (RFC 5546) que la plataforma envia y procesa.
const (
	MethodRequest = "REQUEST"
	MethodReply   = "REPLY"
	MethodCancel  = "CANCEL"
)

// Invitation es un mensaje iTIP ya validado: lo que muestra el lector y lo que hace falta para responderlo.
// Object es el iCalendar sin METHOD, que es como se guarda en un calendario (RFC 4791, 4.1); en una respuesta
// (REPLY) no se guarda nada y Object va vacio. Start y End pueden ir a cero en un REPLY, que no exige DTSTART.
type Invitation struct {
	Method       string
	UID          string
	Sequence     int
	Summary      string
	Location     string
	Description  string
	Start        time.Time
	End          time.Time
	AllDay       bool
	TimeZone     string
	Recurring    bool
	RecurrenceID *time.Time
	Organizer    *Party
	Attendees    []Attendee
	Object       string
}

func withoutMethod(tree *rawComp) string {
	props := tree.props[:0:0]
	for _, l := range tree.props {
		if l.name != "METHOD" {
			props = append(props, l)
		}
	}
	tree.props = props
	var w contentWriter
	tree.write(&w)
	return w.String()
}

// ParseInvitation valida un iCalendar iTIP que llega por correo, acotado como un PUT (lim): un VCALENDAR con un
// METHOD REQUEST, REPLY o CANCEL y eventos de un mismo UID. Un REQUEST y un CANCEL deben ser, sin METHOD, un
// objeto de calendario valido; un REPLY solo necesita UID y el invitado que responde.
func ParseInvitation(raw string, lim CalendarLimits) (Invitation, error) {
	tree, terr := parseICalTree(raw, lim)
	if terr != nil {
		return Invitation{}, terr
	}
	m, n := tree.first("METHOD")
	if n != 1 {
		return Invitation{}, icalError(ICalObject, "una invitación lleva un METHOD")
	}
	method := strings.ToUpper(strings.TrimSpace(m.Value))
	if method != MethodRequest && method != MethodReply && method != MethodCancel {
		return Invitation{}, icalError(ICalObject, "METHOD no admitido")
	}
	stored := withoutMethod(parseRawTree(raw))
	if method == MethodReply {
		return parseReply(tree, stored)
	}
	obj, err := parseCalendarObject(stored, lim)
	if err != nil {
		return Invitation{}, err
	}
	rawTree := parseRawTree(stored)
	f := obj.fields(rawTree)
	main := obj.masterEvent()
	inv := Invitation{
		Method: method, UID: obj.UID, Summary: f.Title, Location: f.Location, Description: f.Description,
		Start: f.Start, End: f.End, AllDay: f.AllDay, TimeZone: f.TimeZone,
		Recurring: main.rrule != nil || len(main.rdates) > 0, Organizer: f.Organizer, Attendees: f.Attendees, Object: stored,
	}
	if raw := rawTree.master(); raw != nil {
		inv.Sequence = raw.sequence()
	}
	if main.recurrenceID != nil {
		rid := obj.zones.instant(*main.recurrenceID)
		inv.RecurrenceID = &rid
	}
	return inv, nil
}

// parseReply lee un REPLY sin exigirle lo que un objeto de calendario necesita (DTSTART, por ejemplo): solo
// eventos de un mismo UID con su organizador y el invitado que responde.
func parseReply(tree *icalComp, stored string) (Invitation, error) {
	zones := zoneSet{custom: map[string]*vtimezone{}}
	for _, c := range tree.Comps {
		switch c.Name {
		case "VTIMEZONE":
			tzid, tz, err := parseVTimezone(c)
			if err != nil {
				return Invitation{}, err
			}
			zones.custom[tzid] = tz
		case componentEvent:
		default:
			return Invitation{}, icalError(ICalComponent, "solo se admiten eventos (VEVENT)")
		}
	}
	inv := Invitation{Method: MethodReply}
	rawTree := parseRawTree(stored)
	var first *rawComp
	for _, c := range rawTree.comps {
		if c.name != componentEvent {
			continue
		}
		uid, ok := c.first("UID")
		value := strings.TrimSpace(uid.value)
		if !ok || value == "" || len(value) > maxUIDLength {
			return Invitation{}, invalid("el VEVENT debe traer un UID de hasta 255 caracteres")
		}
		if inv.UID == "" {
			inv.UID = value
		} else if value != inv.UID {
			return Invitation{}, icalError(ICalObject, "todos los VEVENT deben tener el mismo UID")
		}
		if first == nil {
			first = c
		}
	}
	if first == nil {
		return Invitation{}, icalError(ICalObject, "el objeto no tiene ningún VEVENT")
	}
	inv.Sequence = first.sequence()
	inv.Organizer = first.organizer()
	inv.Attendees = first.attendees()
	if s, ok := first.first("SUMMARY"); ok {
		inv.Summary = truncateRunes(strings.TrimSpace(unescapeText(s.value)), maxSummaryRunes)
	}
	if rid, ok := first.first("RECURRENCE-ID"); ok {
		params := map[string]string{}
		for k := range rid.params {
			params[k] = rid.param(k)
		}
		d, err := parseDateTime(rid.value, params)
		if err != nil {
			return Invitation{}, invalid("RECURRENCE-ID: " + err.Error())
		}
		at := zones.instant(d)
		inv.RecurrenceID = &at
	}
	if len(inv.Attendees) == 0 {
		return Invitation{}, icalError(ICalObject, "una respuesta lleva el invitado que responde")
	}
	return inv, nil
}

func normalizedAddresses(addresses []string) map[string]bool {
	out := map[string]bool{}
	for _, a := range addresses {
		if n, err := NormalizeAddress("address", a); err == nil {
			out[n] = true
		}
	}
	return out
}

// InvitationReply es lo que deja responder a una invitacion: el objeto que se guarda en el calendario del
// invitado (sin METHOD y con su respuesta), el REPLY para el organizador y la direccion con la que respondio.
type InvitationReply struct {
	Stored   string
	Reply    string
	Attendee string
}

// RespondToInvitation responde a un REQUEST en nombre del buzon: su direccion es la primera de addresses que
// figura entre los invitados. partStat es ACCEPTED, TENTATIVE o DECLINED.
func RespondToInvitation(inv Invitation, addresses []string, partStat string, now time.Time) (InvitationReply, error) {
	ps, err := NormalizePartStat("response", partStat)
	if err != nil || (ps != PartStatAccepted && ps != PartStatTentative && ps != PartStatDeclined) {
		return InvitationReply{}, fieldError("response", "se espera ACCEPTED, TENTATIVE o DECLINED")
	}
	if inv.Method != MethodRequest {
		return InvitationReply{}, fieldError("method", "solo se responde a una invitación (REQUEST)")
	}
	if inv.Organizer == nil {
		return InvitationReply{}, fieldError("organizer", "la invitación no tiene organizador")
	}
	mine := normalizedAddresses(addresses)
	tree := parseRawTree(inv.Object)
	me := ""
	for _, c := range tree.comps {
		if c.name != componentEvent || me != "" {
			continue
		}
		for _, a := range c.attendees() {
			if mine[a.Email] {
				me = a.Email
				break
			}
		}
	}
	if me == "" {
		return InvitationReply{}, fieldError("attendee", "el buzón no figura entre los invitados")
	}

	var w contentWriter
	w.line("BEGIN:VCALENDAR")
	w.line("VERSION:2.0")
	w.line("PRODID:" + ProdID)
	w.line("METHOD:" + MethodReply)
	for _, c := range tree.comps {
		if c.name == "VTIMEZONE" {
			c.write(&w)
		}
	}
	stamp := now.UTC().Format(layoutStamp)
	for _, c := range tree.comps {
		if c.name != componentEvent {
			continue
		}
		idx := c.attendeeIndex(me)
		if idx < 0 {
			continue
		}
		c.props[idx] = withPartStat(c.props[idx], ps)
		reply := &rawComp{name: componentEvent}
		for _, l := range c.props {
			switch l.name {
			case "UID", "RECURRENCE-ID", "DTSTART", "DTEND", "DURATION", "SUMMARY", "ORGANIZER", "SEQUENCE":
				reply.props = append(reply.props, l)
			}
		}
		reply.props = append(reply.props, c.props[idx], generated("DTSTAMP:"+stamp))
		reply.write(&w)
	}
	w.line("END:VCALENDAR")
	var stored contentWriter
	tree.write(&stored)
	return InvitationReply{Stored: stored.String(), Reply: w.String(), Attendee: me}, nil
}

// ApplyReply anota en el evento del organizador la respuesta de un invitado. from es el remitente del correo que
// la trajo: solo cuenta si es el propio invitado (nadie responde por otro). Una respuesta a una version anterior
// del evento (SEQUENCE menor), de alguien que no esta invitado o que no cambia nada deja el objeto igual
// (changed falso).
func ApplyReply(stored string, reply Invitation, from string) (string, bool, error) {
	if reply.Method != MethodReply {
		return "", false, fieldError("method", "no es una respuesta (REPLY)")
	}
	sender, err := NormalizeAddress("from", from)
	if err != nil {
		return "", false, err
	}
	var who *Attendee
	for i := range reply.Attendees {
		if reply.Attendees[i].Email == sender {
			who = &reply.Attendees[i]
			break
		}
	}
	if who == nil {
		return "", false, fieldError("attendee", "la respuesta no es del remitente del correo")
	}
	t := parseRawTree(stored)
	master := t.master()
	if master == nil || master.sequence() > reply.Sequence {
		return stored, false, nil
	}
	target := master
	if reply.RecurrenceID != nil {
		obj, err := ParseStoredCalendarObject(stored)
		if err != nil {
			return "", false, err
		}
		target = nil
		i := 0
		for _, c := range t.comps {
			if c.name != componentEvent {
				continue
			}
			if i < len(obj.events) && obj.events[i].recurrenceID != nil && obj.zones.instant(*obj.events[i].recurrenceID).Equal(*reply.RecurrenceID) {
				target = c
			}
			i++
		}
		if target == nil {
			return stored, false, nil
		}
	}
	idx := target.attendeeIndex(who.Email)
	if idx < 0 {
		return stored, false, nil
	}
	if cur, err := NormalizePartStat("partstat", target.props[idx].param("PARTSTAT")); err == nil && cur == who.PartStat {
		return stored, false, nil
	}
	target.props[idx] = withPartStat(target.props[idx], who.PartStat)
	var w contentWriter
	t.write(&w)
	return w.String(), true, nil
}

// OrganizerOf es el organizador del VEVENT principal de un objeto guardado, o nil.
func OrganizerOf(stored string) *Party {
	master := parseRawTree(stored).master()
	if master == nil {
		return nil
	}
	return master.organizer()
}

// SequenceOf es el SEQUENCE del VEVENT principal de un objeto guardado.
func SequenceOf(stored string) int {
	master := parseRawTree(stored).master()
	if master == nil {
		return 0
	}
	return master.sequence()
}

// OrganizedBy dice si el organizador del objeto guardado es una de las direcciones.
func OrganizedBy(stored string, addresses []string) bool {
	org := OrganizerOf(stored)
	return org != nil && normalizedAddresses(addresses)[org.Email]
}

// PartStatOf es la respuesta de esa direccion en el VEVENT principal del objeto guardado, o vacio si no esta
// invitada.
func PartStatOf(stored, address string) string {
	master := parseRawTree(stored).master()
	if master == nil {
		return ""
	}
	for _, a := range master.attendees() {
		if a.Email == address {
			return a.PartStat
		}
	}
	return ""
}

// Outgoing es una invitacion que el organizador envia: el iCalendar con METHOD y a quien va.
type Outgoing struct {
	Method     string
	ICal       string
	Recipients []string
}

// BuildInvitation escribe el iTIP que el organizador envia de un objeto guardado: REQUEST con todos sus eventos
// (la serie y sus sobrescrituras) o CANCEL de la serie entera. Los avisos (VALARM) son del organizador y no
// viajan; omitDescription deja fuera tambien la descripcion (una cita reservada desde la pagina publica lleva ahi
// la nota del visitante, que no se reenvia a la direccion que el escribio). Los destinatarios son los invitados
// menos el organizador.
func BuildInvitation(stored, method string, now time.Time, omitDescription bool) (Outgoing, error) {
	if method != MethodRequest && method != MethodCancel {
		return Outgoing{}, fieldError("method", "se espera REQUEST o CANCEL")
	}
	tree := parseRawTree(stored)
	master := tree.master()
	if master == nil {
		return Outgoing{}, ErrNotFound
	}
	org := master.organizer()
	if org == nil {
		return Outgoing{}, fieldError("organizer", "el evento no tiene organizador")
	}
	out := Outgoing{Method: method}
	seen := map[string]bool{org.Email: true}
	for _, c := range tree.comps {
		if c.name != componentEvent {
			continue
		}
		for _, a := range c.attendees() {
			if !seen[a.Email] {
				seen[a.Email] = true
				out.Recipients = append(out.Recipients, a.Email)
			}
		}
	}
	if len(out.Recipients) == 0 {
		return Outgoing{}, fieldError("attendees", "el evento no tiene invitados")
	}

	strip := func(c *rawComp) *rawComp {
		cp := &rawComp{name: c.name}
		for _, l := range c.props {
			if l.name == "DESCRIPTION" && omitDescription {
				continue
			}
			cp.props = append(cp.props, l)
		}
		return cp
	}
	var w contentWriter
	w.line("BEGIN:VCALENDAR")
	for _, l := range tree.props {
		if l.name != "METHOD" {
			w.line(l.text)
		}
	}
	w.line("METHOD:" + method)
	for _, c := range tree.comps {
		switch {
		case c.name == "VTIMEZONE":
			c.write(&w)
		case c.name == componentEvent && method == MethodRequest:
			strip(c).write(&w)
		case c == master:
			cancel := strip(c)
			props := cancel.props[:0:0]
			for _, l := range cancel.props {
				if l.name != "STATUS" {
					props = append(props, l)
				}
			}
			cancel.props = append(props, generated("STATUS:CANCELLED"))
			cancel.touch(now, master.sequence()+1)
			cancel.write(&w)
		}
	}
	w.line("END:VCALENDAR")
	out.ICal = w.String()
	return out, nil
}
