package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDireccionesYRespuestas(t *testing.T) {
	for raw, want := range map[string]string{
		" Ana@Empresa.PE ":                 "ana@empresa.pe",
		"luis.perez@x.co.uk":               "luis.perez@x.co.uk",
		"no-es-correo":                     "",
		"a@b":                              "",
		"a b@c.pe":                         "",
		"a;b@c.pe":                         "",
		"a:b@c.pe":                         "",
		`"a"@c.pe`:                         "",
		"a@c.pe\r\nX: y":                   "",
		strings.Repeat("a", 250) + "@c.pe": "",
	} {
		got, err := NormalizeAddress("f", raw)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("%q: %q %v", raw, got, err)
		}
	}
	for raw, want := range map[string]string{"": PartStatNeedsAction, " accepted ": PartStatAccepted, "Declined": PartStatDeclined} {
		if got, err := NormalizePartStat("p", raw); err != nil || got != want {
			t.Errorf("%q: %q %v", raw, got, err)
		}
	}
	if _, err := NormalizePartStat("p", "X-OTRA"); err == nil {
		t.Fatal("PARTSTAT desconocido")
	}
	if name, err := NormalizePartyName("n", ` Ana "la jefa" `); err != nil || name != "Ana la jefa" {
		t.Fatalf("nombre: %q %v", name, err)
	}
	if _, err := NormalizePartyName("n", "a\x00b"); err == nil {
		t.Fatal("un control en el nombre")
	}
}

func TestInvitadosSeDeduplicanYSeAcotan(t *testing.T) {
	got, err := normalizeAttendees([]Attendee{{Email: "A@x.pe"}, {Email: "a@x.pe", PartStat: "ACCEPTED"}, {Email: "b@x.pe", Name: "Bea"}})
	if err != nil || len(got) != 2 || got[0].Email != "a@x.pe" || got[0].PartStat != PartStatNeedsAction || got[1].Name != "Bea" {
		t.Fatalf("invitados: %+v %v", got, err)
	}
	many := make([]Attendee, MaxEventAttendees+1)
	for i := range many {
		many[i] = Attendee{Email: strings.Repeat("a", i+1) + "@x.pe"}
	}
	var fe *FieldError
	if _, err := normalizeAttendees(many); !errors.As(err, &fe) || fe.Field != "attendees" {
		t.Fatalf("demasiados: %v", err)
	}
	if _, err := normalizeAttendees([]Attendee{{Email: "a@x.pe"}, {Email: "malo"}}); !errors.As(err, &fe) || fe.Field != "attendees[1].email" {
		t.Fatalf("campo del error: %v", err)
	}
}

func TestParametrosDeLasLineasDeReunion(t *testing.T) {
	if quoteParam(`Perez, Ana`) != `"Perez, Ana"` || quoteParam("Ana") != "Ana" || quoteParam("a\"b\x01c") != "abc" {
		t.Fatal("quoteParam")
	}
	if got := organizerLine(Party{Email: "a@x.pe", Name: "A; B"}); got != `ORGANIZER;CN="A; B":mailto:a@x.pe` {
		t.Fatalf("organizador: %q", got)
	}
	if got := attendeeLine(Attendee{Email: "b@x.pe", PartStat: PartStatAccepted}); strings.Contains(got, "RSVP") {
		t.Fatalf("quien ya respondio no lleva RSVP: %q", got)
	}
	l, _ := parseRawLine(`ATTENDEE;CUTYPE=INDIVIDUAL;CN="Bea, Ventas";RSVP=TRUE;PARTSTAT=NEEDS-ACTION:mailto:bea@x.pe`)
	out := withPartStat(l, PartStatTentative)
	for _, want := range []string{"CUTYPE=INDIVIDUAL", `CN="Bea, Ventas"`, "PARTSTAT=TENTATIVE", ":mailto:bea@x.pe"} {
		if !strings.Contains(out.text, want) {
			t.Errorf("falta %q en %q", want, out.text)
		}
	}
	if strings.Contains(out.text, "RSVP") || strings.Contains(out.text, "NEEDS-ACTION") {
		t.Fatalf("respuesta nueva: %q", out.text)
	}
	if a, ok := mailtoAddress("MAILTO:Ana@X.pe"); !ok || a != "ana@x.pe" {
		t.Fatal("mailto en mayusculas")
	}
	if _, ok := mailtoAddress("mailto:"); ok {
		t.Fatal("mailto vacio")
	}
}

func TestAparicionesDeSeriesEnUTCDiaCompletoYFlotantes(t *testing.T) {
	cases := []struct {
		name, start, extra, rid, want string
	}{
		{"utc", "DTSTART:20261001T100000Z", "DTEND:20261001T110000Z", "20261002T100000Z", "RECURRENCE-ID:20261002T100000Z"},
		{"flotante", "DTSTART:20261001T100000", "DTEND:20261001T110000", "20261002T100000Z", "RECURRENCE-ID:20261002T100000"},
		{"dia completo", "DTSTART;VALUE=DATE:20261001", "DTEND;VALUE=DATE:20261002", "20261002T000000Z", "RECURRENCE-ID;VALUE=DATE:20261002"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := ical(vevent("s", c.start, c.extra, "SUMMARY:Serie", "RRULE:FREQ=DAILY;COUNT=3"))
			rid := utc(c.rid)
			f := EventFields{Title: "Cambiada", Start: rid.Add(time.Hour), End: rid.Add(2 * time.Hour)}
			if strings.Contains(c.start, "VALUE=DATE") {
				f = EventFields{Title: "Cambiada", Start: rid, End: rid.AddDate(0, 0, 1), AllDay: true}
			}
			n, err := f.Normalize()
			if err != nil {
				t.Fatal(err)
			}
			edited, err := SetOccurrence(raw, rid, n, stamp)
			if err != nil {
				t.Fatalf("editar: %v\n%s", err, raw)
			}
			if !strings.Contains(edited, c.want) {
				t.Fatalf("falta %q en\n%s", c.want, edited)
			}
			if _, err := ParseCalendarObject(edited, calLimits); err != nil {
				t.Fatalf("no valida: %v\n%s", err, edited)
			}
			deleted, err := DeleteOccurrence(edited, rid, stamp)
			if err != nil || strings.Contains(deleted, "RECURRENCE-ID") || !strings.Contains(deleted, "EXDATE") {
				t.Fatalf("borrar: %v\n%s", err, deleted)
			}
			if got := seriesOccurrences(t, deleted); len(got) != 2 {
				t.Fatalf("quedan %d", len(got))
			}
		})
	}
}

// Una serie con una zona propia de Outlook (VTIMEZONE sin nombre IANA) escribe sus excepciones con ese TZID.
func TestAparicionEnUnaZonaPropia(t *testing.T) {
	raw := ical(outlookZone, vevent("o", "DTSTART;TZID=Romance Standard Time:20261005T100000",
		"DTEND;TZID=Romance Standard Time:20261005T110000", "SUMMARY:Outlook", "RRULE:FREQ=WEEKLY;COUNT=4"))
	occs := seriesOccurrences(t, raw)
	if len(occs) != 4 {
		t.Fatalf("serie: %+v", occs)
	}
	deleted, err := DeleteOccurrence(raw, occs[3].RecurrenceID, stamp)
	if err != nil || !strings.Contains(deleted, "EXDATE;TZID=Romance Standard Time:20261026T100000") {
		t.Fatalf("EXDATE en la zona propia tras el cambio de hora: %v\n%s", err, deleted)
	}
}

const outlookZone = "BEGIN:VTIMEZONE\r\nTZID:Romance Standard Time\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:16010101T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nRRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:16010101T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nRRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=3\r\nEND:DAYLIGHT\r\n" +
	"END:VTIMEZONE\r\n"

func TestUnObjetoSoloDeSobrescriturasNoEsUnaSerie(t *testing.T) {
	raw := ical(vevent("x", "RECURRENCE-ID:20261001T100000Z", "DTSTART:20261001T120000Z", "DTEND:20261001T130000Z"))
	var fe *FieldError
	if _, err := DeleteOccurrence(raw, utc("20261001T100000Z"), stamp); !errors.As(err, &fe) {
		t.Fatalf("sin serie: %v", err)
	}
	if _, err := SetOccurrence("no es ical", utc("20261001T100000Z"), EventFields{}, stamp); err == nil {
		t.Fatal("un objeto ilegible")
	}
}

func TestInvitacionesMalFormadas(t *testing.T) {
	head := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n"
	tail := "END:VCALENDAR\r\n"
	event := "BEGIN:VEVENT\r\nUID:a\r\nDTSTART:20261001T100000Z\r\nORGANIZER:mailto:a@x.pe\r\nATTENDEE:mailto:b@x.pe\r\nEND:VEVENT\r\n"
	for name, raw := range map[string]string{
		"sin METHOD":         head + event + tail,
		"METHOD PUBLISH":     head + "METHOD:PUBLISH\r\n" + event + tail,
		"dos METHOD":         head + "METHOD:REQUEST\r\nMETHOD:REQUEST\r\n" + event + tail,
		"tarea":              head + "METHOD:REQUEST\r\nBEGIN:VTODO\r\nUID:a\r\nEND:VTODO\r\n" + tail,
		"sin DTSTART":        head + "METHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:a\r\nEND:VEVENT\r\n" + tail,
		"REPLY sin invitado": head + "METHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:a\r\nEND:VEVENT\r\n" + tail,
		"REPLY con dos UID":  head + "METHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:a\r\nATTENDEE:mailto:b@x.pe\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nUID:b\r\nATTENDEE:mailto:b@x.pe\r\nEND:VEVENT\r\n" + tail,
		"REPLY sin UID":      head + "METHOD:REPLY\r\nBEGIN:VEVENT\r\nATTENDEE:mailto:b@x.pe\r\nEND:VEVENT\r\n" + tail,
		"REPLY con tarea":    head + "METHOD:REPLY\r\nBEGIN:VTODO\r\nUID:a\r\nEND:VTODO\r\n" + tail,
		"REPLY sin eventos":  head + "METHOD:REPLY\r\n" + tail,
		"REPLY RID malo":     head + "METHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:a\r\nRECURRENCE-ID:ayer\r\nATTENDEE:mailto:b@x.pe\r\nEND:VEVENT\r\n" + tail,
		"demasiado grande":   head + "METHOD:REQUEST\r\n" + strings.Repeat("X-A:"+strings.Repeat("a", 60)+"\r\n", 400) + event + tail,
		"no es iCalendar":    "hola",
	} {
		if _, err := ParseInvitation(raw, calLimits); err == nil {
			t.Errorf("%s: se acepto", name)
		}
	}
	inv, err := ParseInvitation(head+"METHOD:request\r\n"+event+tail, calLimits)
	if err != nil || inv.Method != MethodRequest || inv.Organizer.Email != "a@x.pe" {
		t.Fatalf("METHOD en minusculas: %+v %v", inv, err)
	}
}

// Una respuesta a una sola aparicion cuenta en su sobrescritura; si esa aparicion no tiene sobrescritura no se
// inventa una.
func TestRespuestaAUnaAparicion(t *testing.T) {
	stored := ical(
		vevent("a", "DTSTART:20261001T100000Z", "DTEND:20261001T110000Z", "RRULE:FREQ=DAILY;COUNT=3", "ORGANIZER:mailto:a@x.pe", "ATTENDEE:mailto:b@x.pe"),
		vevent("a", "RECURRENCE-ID:20261002T100000Z", "DTSTART:20261002T120000Z", "DTEND:20261002T130000Z", "ORGANIZER:mailto:a@x.pe", "ATTENDEE:mailto:b@x.pe"),
	)
	reply := func(rid string) Invitation {
		raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:a\r\nRECURRENCE-ID:" + rid +
			"\r\nATTENDEE;PARTSTAT=DECLINED:mailto:b@x.pe\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		inv, err := ParseInvitation(raw, calLimits)
		if err != nil {
			t.Fatal(err)
		}
		return inv
	}
	updated, changed, err := ApplyReply(stored, reply("20261002T100000Z"), "b@x.pe")
	if err != nil || !changed {
		t.Fatalf("aparicion: %v %v", changed, err)
	}
	f := mustFields(t, updated)
	if f.Attendees[0].PartStat != PartStatNeedsAction || !strings.Contains(updated, "PARTSTAT=DECLINED") {
		t.Fatalf("la serie no cambia, la sobrescritura si:\n%s", updated)
	}
	if _, changed, _ := ApplyReply(stored, reply("20261003T100000Z"), "b@x.pe"); changed {
		t.Fatal("una aparicion sin sobrescritura no se inventa")
	}
	if _, _, err := ApplyReply(stored, Invitation{Method: MethodRequest}, "b@x.pe"); err == nil {
		t.Fatal("aplicar algo que no es una respuesta")
	}
	if _, _, err := ApplyReply(stored, reply("20261002T100000Z"), "no es"); err == nil {
		t.Fatal("remitente ilegible")
	}
	norsvp := ical(vevent("a", "DTSTART:20261001T100000Z", "ORGANIZER:mailto:a@x.pe", "ATTENDEE:mailto:c@x.pe"))
	inv := Invitation{Method: MethodReply, Attendees: []Attendee{{Email: "b@x.pe", PartStat: PartStatAccepted}}}
	if _, changed, _ := ApplyReply(norsvp, inv, "b@x.pe"); changed {
		t.Fatal("quien no esta invitado no cambia nada")
	}
}

func TestRespuestasDelInvitado(t *testing.T) {
	out, err := BuildInvitation(weeklySeries(t), MethodRequest, stamp, false)
	if err != nil {
		t.Fatal(err)
	}
	inv, _ := ParseInvitation(out.ICal, calLimits)
	res, err := RespondToInvitation(inv, []string{"otra@x.pe", "bea@acme.test"}, PartStatDeclined, stamp)
	if err != nil || !strings.Contains(res.Reply, "PARTSTAT=DECLINED") || !strings.Contains(res.Reply, "ORGANIZER") ||
		!strings.Contains(res.Reply, "BEGIN:VTIMEZONE") {
		t.Fatalf("rechazo: %v\n%s", err, res.Reply)
	}
	cancel, _ := BuildInvitation(weeklySeries(t), MethodCancel, stamp, false)
	c, _ := ParseInvitation(cancel.ICal, calLimits)
	if _, err := RespondToInvitation(c, []string{"bea@acme.test"}, PartStatAccepted, stamp); err == nil {
		t.Fatal("una cancelacion no se responde")
	}
	noOrg := Invitation{Method: MethodRequest, Object: inv.Object}
	if _, err := RespondToInvitation(noOrg, []string{"bea@acme.test"}, PartStatAccepted, stamp); err == nil {
		t.Fatal("una invitacion sin organizador no se responde")
	}
	if _, err := BuildInvitation(weeklySeries(t), "PUBLISH", stamp, false); err == nil {
		t.Fatal("metodo no admitido")
	}
	if OrganizerOf("no es") != nil || SequenceOf("no es") != 0 || PartStatOf("no es", "a@x.pe") != "" {
		t.Fatal("un objeto ilegible no tiene organizador")
	}
	if PartStatOf(weeklySeries(t), "bea@acme.test") != PartStatNeedsAction || PartStatOf(weeklySeries(t), "otro@x.pe") != "" {
		t.Fatal("PartStatOf")
	}
}

func TestCancelacionSoloLlevaLaSerie(t *testing.T) {
	f, _ := EventFields{Title: "Movida", Start: utc("20261012T100000Z"), End: utc("20261012T110000Z")}.Normalize()
	series := weeklySeries(t)
	rid := seriesOccurrences(t, series)[1].RecurrenceID
	edited, err := SetOccurrence(series, rid, f, stamp)
	if err != nil {
		t.Fatal(err)
	}
	cancel, _ := BuildInvitation(edited, MethodCancel, stamp, false)
	if strings.Count(cancel.ICal, "BEGIN:VEVENT") != 1 || strings.Contains(cancel.ICal, "RECURRENCE-ID") {
		t.Fatalf("cancelacion:\n%s", cancel.ICal)
	}
	request, _ := BuildInvitation(edited, MethodRequest, stamp, false)
	if strings.Count(request.ICal, "BEGIN:VEVENT") != 2 {
		t.Fatalf("una invitacion lleva la serie y sus sobrescrituras:\n%s", request.ICal)
	}
}

func TestOcupacionDeSeriesConExcepciones(t *testing.T) {
	plan := BusyPlan{From: utc("20260901T000000Z"), To: utc("20261201T000000Z"), MaxIntervals: 50}
	raw := ical(
		vevent("s", "DTSTART:20261001T100000Z", "DTEND:20261001T110000Z", "RRULE:FREQ=DAILY;COUNT=5", "EXDATE:20261002T100000Z"),
		vevent("s", "RECURRENCE-ID:20261003T100000Z", "DTSTART:20261003T100000Z", "DTEND:20261003T110000Z", "STATUS:CANCELLED"),
		vevent("s", "RECURRENCE-ID:20261004T100000Z", "DTSTART:20261004T150000Z", "DTEND:20261004T160000Z"),
	)
	ev, err := NewEventAt("s.ics", raw, calLimits, plan)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range ev.Busy {
		got = append(got, b.Start.Format("0215"))
	}
	if strings.Join(got, ",") != "0110,0415,0510" || ev.BusyUntil != nil {
		t.Fatalf("ocupacion: %v %v", got, ev.BusyUntil)
	}
	approx := ical(vevent("h", "DTSTART:20261001T100000Z", "DTEND:20261001T110000Z", "RRULE:FREQ=HOURLY;COUNT=3"))
	if ev, _ := NewEventAt("h.ics", approx, calLimits, plan); len(ev.Busy) != 1 || ev.BusyUntil == nil {
		t.Fatalf("una regla que no se sabe expandir cuenta su primera aparicion y queda pendiente: %+v %v", ev.Busy, ev.BusyUntil)
	}
	if _, err := NewEventAt("mal nombre", raw, calLimits, plan); !errors.Is(err, ErrInvalidName) {
		t.Fatal("nombre de recurso")
	}
	if MergeIntervals(nil) == nil || len(MergeIntervals([]Interval{{utc("20261001T100000Z"), utc("20261001T110000Z")}, {utc("20261001T110000Z"), utc("20261001T120000Z")}})) != 1 {
		t.Fatal("tramos que se tocan se funden")
	}
}

func TestHuecosEnElDiaDelCambioDeHora(t *testing.T) {
	madrid := mustLoc(t, "Europe/Madrid")
	s := BookingSettings{Title: "x", DurationMinutes: 60, MaxAdvanceDays: 30, DailyLimit: 5, TimeZone: "Europe/Madrid", Active: true}
	s.Weekly[time.Sunday] = []DayWindow{{Start: 9 * 60, End: 12 * 60}}
	n, err := s.Normalize(10)
	if err != nil {
		t.Fatal(err)
	}
	p := BookingPage{BookingSettings: n}
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, madrid)
	slots := p.Slots(now, now.AddDate(0, 0, 14), now, nil, 100)
	if len(slots) != 6 {
		t.Fatalf("dos domingos con tres huecos: %d", len(slots))
	}
	for _, sl := range slots {
		if l := sl.Start.In(madrid); l.Hour() < 9 || l.Hour() > 11 {
			t.Fatalf("un hueco fuera de su franja de pared: %v", l)
		}
	}
	// El domingo 25 es el del cambio: las 9:00 son las 08:00 UTC, no las 07:00.
	if slots[0].Start.UTC().Hour() != 8 || slots[3].Start.UTC().Hour() != 8 {
		t.Fatalf("UTC del domingo del cambio y del siguiente: %v %v", slots[0].Start, slots[3].Start)
	}
	if got := p.Slots(now.AddDate(0, 0, 10), now, now, nil, 10); len(got) != 0 {
		t.Fatal("una ventana al reves no tiene huecos")
	}
	if got := (BookingPage{BookingSettings: BookingSettings{TimeZone: "Luna/Base"}}).Slots(now, now.AddDate(0, 0, 1), now, nil, 10); len(got) != 0 {
		t.Fatal("una zona desconocida no tiene huecos")
	}
	inactive := s
	inactive.Active = false
	inactive.Weekly = [7][]DayWindow{}
	if _, err := inactive.Normalize(10); err != nil {
		t.Fatalf("una pagina inactiva puede no tener franjas: %v", err)
	}
	long := s
	long.Weekly[time.Monday] = make([]DayWindow, MaxBookingWindowsPerDay+1)
	if _, err := long.Normalize(10); err == nil {
		t.Fatal("demasiadas franjas")
	}
	if _, ok := WeekdayOf("xx"); ok || WeekdayCode(time.Wednesday) != "WE" {
		t.Fatal("codigos de dia")
	}
}

func TestReservasYSuEvento(t *testing.T) {
	req, err := (BookingRequest{Start: utc("20261001T150000Z").Add(300 * time.Millisecond), Name: "  Luis  ", Email: "LUIS@c.pe", Note: "a\r\nb"}).Normalize()
	if err != nil || req.Name != "Luis" || req.Email != "luis@c.pe" || req.Note != "a\nb" || req.Start.Nanosecond() != 0 {
		t.Fatalf("reserva: %+v %v", req, err)
	}
	for name, bad := range map[string]BookingRequest{
		"sin hora":     {Name: "x", Email: "x@c.pe"},
		"nombre largo": {Start: stamp, Name: strings.Repeat("a", MaxBookingNameRunes+1), Email: "x@c.pe"},
		"nota larga":   {Start: stamp, Name: "x", Email: "x@c.pe", Note: strings.Repeat("a", MaxBookingNoteRunes+1)},
		"control":      {Start: stamp, Name: "x\x07", Email: "x@c.pe"},
	} {
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("%s: se acepto", name)
		}
	}
	p := bookingPage(t)
	f := p.BookingEventFields(req)
	if f.Organizer.Email != "ana@acme.test" || len(f.Attendees) != 2 || f.Attendees[0].PartStat != PartStatAccepted ||
		f.TimeZone != "America/Lima" || !f.End.Equal(req.Start.Add(30*time.Minute)) || !strings.Contains(f.Description, "luis@c.pe") {
		t.Fatalf("evento de la cita: %+v", f)
	}
}

func TestVTimezoneSinCambiosDeHora(t *testing.T) {
	var w contentWriter
	buildVTimezone(mustLoc(t, "Asia/Tokyo"), stamp).write(&w)
	out := w.String()
	if strings.Count(out, "BEGIN:STANDARD") != 1 || strings.Contains(out, "DAYLIGHT") || !strings.Contains(out, "TZOFFSETTO:+0900") {
		t.Fatalf("zona fija:\n%s", out)
	}
	if formatUTCOffset(-(5*3600+30*60+15)) != "-053015" || formatUTCOffset(3600) != "+0100" {
		t.Fatal("desfase con segundos")
	}
	day := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	if !ruleFits(day, true, 4, time.October, time.Sunday) || ruleFits(day, false, 3, time.October, time.Sunday) {
		t.Fatal("ruleFits")
	}
	var z zoneSet
	if !z.wallIn("", stamp).Equal(stamp) || !z.wallIn("Desconocida", stamp).Equal(stamp) {
		t.Fatal("sin zona el reloj es el instante")
	}
}

// Un ano entero de una serie semanal conserva su hora de pared en zonas con cambio de horario en ambos hemisferios
// y en zonas sin el; y lo mismo cuando el cliente solo conoce el VTIMEZONE (el TZID no es un nombre IANA).
func TestSerieDeUnAnoEnVariasZonas(t *testing.T) {
	for _, name := range []string{"Europe/Madrid", "America/New_York", "Australia/Sydney", "America/Santiago", "Asia/Tokyo", "America/Lima"} {
		t.Run(name, func(t *testing.T) {
			loc := mustLoc(t, name)
			start := time.Date(2026, 10, 5, 9, 30, 0, 0, loc)
			raw := mustBuildEvent(t, "", EventFields{Title: "Semanal", Start: start.UTC(), End: start.Add(45 * time.Minute).UTC(), TimeZone: name,
				Recurrence: &Recurrence{Freq: "weekly", Count: 52}})
			from, to := start.Add(-time.Hour).UTC(), start.AddDate(1, 0, 0).UTC()
			for label, text := range map[string]string{"IANA": raw, "solo VTIMEZONE": strings.ReplaceAll(raw, name, "Zona/Propia")} {
				occs := mustParse(t, text).Occurrences(TimeRange{Start: &from, End: &to}, NewBudget(10000))
				if len(occs) != 52 {
					t.Fatalf("%s: %d apariciones", label, len(occs))
				}
				for i, o := range occs {
					l := o.Start.In(loc)
					if l.Hour() != 9 || l.Minute() != 30 || l.Weekday() != time.Monday || o.End.Sub(o.Start) != 45*time.Minute {
						t.Fatalf("%s: la aparicion %d se corrio: %v - %v", label, i, l, o.End.In(loc))
					}
				}
			}
			busy, _ := mustParse(t, raw).Busy(BusyPlan{From: from, To: to, MaxIntervals: 100}, nil, NewBudget(10000))
			if len(busy) != 52 || !busy[51].Start.Equal(time.Date(2027, 9, 27, 9, 30, 0, 0, loc)) {
				t.Fatalf("ocupacion: %d tramos", len(busy))
			}
		})
	}
}

// Lo que no se puede leer no tiene organizador ni invitacion (y no revienta); los destinatarios son los invitados
// de la serie y de sus sobrescrituras, una vez cada uno y nunca el propio organizador.
func TestDestinatariosDeUnaInvitacion(t *testing.T) {
	for _, raw := range []string{"", "no es", "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"} {
		if OrganizerOf(raw) != nil || SequenceOf(raw) != 0 || PartStatOf(raw, "ana@acme.test") != "" || OrganizedBy(raw, []string{"ana@acme.test"}) {
			t.Fatalf("%q tiene datos de reunion", raw)
		}
		if _, err := BuildInvitation(raw, MethodRequest, stamp, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	series := mustBuildEvent(t, "", EventFields{Title: "Serie", Start: utc("20261005T090000Z"), End: utc("20261005T100000Z"),
		Recurrence: &Recurrence{Freq: "weekly", Count: 3}, Organizer: &Party{Email: "ana@acme.test"},
		Attendees: []Attendee{{Email: "ana@acme.test"}, {Email: "cris@acme.test"}}})
	edited, err := SetOccurrence(series, utc("20261012T090000Z"), EventFields{Title: "Movida", Start: utc("20261012T110000Z"), End: utc("20261012T120000Z")}, stamp)
	if err != nil {
		t.Fatal(err)
	}
	// La sobrescritura hereda los invitados de la serie; un cliente puede anadir alguno solo a esa aparicion.
	if strings.Count(edited, "ATTENDEE") != 4 {
		t.Fatalf("la aparicion editada perdio invitados:\n%s", edited)
	}
	edited = strings.Replace(edited, "RECURRENCE-ID", "ATTENDEE:mailto:eva@acme.test\r\nRECURRENCE-ID", 1)
	out, err := BuildInvitation(edited, MethodRequest, stamp, false)
	if err != nil || strings.Join(out.Recipients, ",") != "cris@acme.test,eva@acme.test" {
		t.Fatalf("destinatarios: %+v %v", out.Recipients, err)
	}
}
