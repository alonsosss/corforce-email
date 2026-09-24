package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func minutes(n int) *int { return &n }

func mustBuildEvent(t *testing.T, existing string, f EventFields) string {
	t.Helper()
	n, err := f.Normalize()
	if err != nil {
		t.Fatalf("normalizar: %v", err)
	}
	raw, err := BuildCalendarObject(existing, "evt-1", n, stamp)
	if err != nil {
		t.Fatalf("escribir: %v", err)
	}
	if _, err := ParseCalendarObject(raw, calLimits); err != nil {
		t.Fatalf("el iCalendar generado no pasa ParseCalendarObject: %v\n%s", err, raw)
	}
	return raw
}

func mustFields(t *testing.T, raw string) EventFields {
	t.Helper()
	f, err := EventFieldsOf(raw)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEventoIdaYVuelta(t *testing.T) {
	until := utc("20261231T235959Z")
	in := EventFields{
		Title: "Revision; trimestral, equipo \\ ventas", Start: utc("20260928T140000Z"), End: utc("20260928T153000Z"),
		Location: "Sala 3", Description: "Orden del dia:\n1. Cifras\n2. Plan",
		Recurrence:      &Recurrence{Freq: "weekly", Interval: 2, Until: &until, ByDay: []string{"MO", "WE"}},
		ReminderMinutes: minutes(15),
	}
	raw := mustBuildEvent(t, "", in)
	for _, want := range []string{"PRODID:" + ProdID, "UID:evt-1", "DTSTART:20260928T140000Z", "DTEND:20260928T153000Z",
		`SUMMARY:Revision\; trimestral\, equipo \\ ventas`, `DESCRIPTION:Orden del dia:\n1. Cifras\n2. Plan`,
		"RRULE:FREQ=WEEKLY;INTERVAL=2;UNTIL=20261231T235959Z;BYDAY=MO,WE", "BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT15M", "DTSTAMP:20260924T100000Z"} {
		if !strings.Contains(raw, want) {
			t.Errorf("falta %q en\n%s", want, raw)
		}
	}
	got := mustFields(t, raw)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("ida y vuelta:\nquiero %+v\ntengo  %+v", in, got)
	}
}

func TestEventoDeDiaCompleto(t *testing.T) {
	start, _ := ParseEventTime("start", "2026-10-05", true)
	end, _ := ParseEventTime("end", "2026-10-05T00:00:00-05:00", true)
	raw := mustBuildEvent(t, "", EventFields{Title: "Feriado", Start: start, End: end, AllDay: true,
		Recurrence: &Recurrence{Freq: "yearly", Count: 5}})
	if !strings.Contains(raw, "DTSTART;VALUE=DATE:20261005") || !strings.Contains(raw, "DTEND;VALUE=DATE:20261006") || !strings.Contains(raw, "RRULE:FREQ=YEARLY;COUNT=5") {
		t.Fatalf("dia completo:\n%s", raw)
	}
	got := mustFields(t, raw)
	if !got.AllDay || !got.Start.Equal(start) || !got.End.Equal(start.AddDate(0, 0, 1)) || got.Recurrence.Count != 5 {
		t.Fatalf("lectura: %+v", got)
	}
	// La fecha de un dia completo es la que se escribio, no la del instante en UTC.
	if d, _ := ParseEventTime("start", "2026-10-05T00:00:00+05:00", true); d.Day() != 5 {
		t.Fatalf("fecha con desfase: %v", d)
	}
	u, _ := ParseEventTime("recurrence.until", "2027-01-01", true)
	raw = mustBuildEvent(t, "", EventFields{Title: "x", Start: start, End: start, AllDay: true, Recurrence: &Recurrence{Freq: "daily", Until: &u}})
	if !strings.Contains(raw, "UNTIL=20270101;") && !strings.Contains(raw, "UNTIL=20270101\r\n") {
		t.Fatalf("UNTIL de un dia completo es una fecha:\n%s", raw)
	}
}

func TestEventoValidaLosCampos(t *testing.T) {
	base := EventFields{Title: "x", Start: utc("20260928T140000Z"), End: utc("20260928T150000Z")}
	with := func(mod func(*EventFields)) EventFields {
		f := base
		mod(&f)
		return f
	}
	u := utc("20260901T000000Z")
	cases := map[string]struct {
		in    EventFields
		field string
	}{
		"sin titulo":       {with(func(f *EventFields) { f.Title = " " }), "title"},
		"titulo con salto": {with(func(f *EventFields) { f.Title = "a\r\nATTENDEE:mailto:x@y" }), "title"},
		"lugar con salto":  {with(func(f *EventFields) { f.Location = "a\nb" }), "location"},
		"fin antes":        {with(func(f *EventFields) { f.End = utc("20260928T130000Z") }), "end"},
		"frecuencia":       {with(func(f *EventFields) { f.Recurrence = &Recurrence{Freq: "hourly"} }), "recurrence.freq"},
		"intervalo":        {with(func(f *EventFields) { f.Recurrence = &Recurrence{Freq: "daily", Interval: -1} }), "recurrence.interval"},
		"count y until":    {with(func(f *EventFields) { f.Recurrence = &Recurrence{Freq: "daily", Count: 2, Until: &base.End} }), "recurrence.until"},
		"until anterior":   {with(func(f *EventFields) { f.Recurrence = &Recurrence{Freq: "daily", Until: &u} }), "recurrence.until"},
		"dia raro":         {with(func(f *EventFields) { f.Recurrence = &Recurrence{Freq: "weekly", ByDay: []string{"MO", "XX"}} }), "recurrence.by_day[1]"},
		"aviso negativo":   {with(func(f *EventFields) { f.ReminderMinutes = minutes(-5) }), "reminder_minutes"},
		"aviso enorme":     {with(func(f *EventFields) { f.ReminderMinutes = minutes(MaxReminderMinutes + 1) }), "reminder_minutes"},
	}
	for name, c := range cases {
		var fe *FieldError
		if _, err := c.in.Normalize(); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: quiero error en %q, tengo %v", name, c.field, err)
		}
	}
	for _, v := range []string{"", "ayer", "2026-13-01T00:00:00Z", "2026-09-28 10:00"} {
		if _, err := ParseEventTime("start", v, false); err == nil {
			t.Errorf("%q deberia rechazarse", v)
		}
	}
}

// Una descripcion con saltos y un texto con END:VEVENT no abren componentes ni propiedades.
func TestEventoNoAdmiteInyeccion(t *testing.T) {
	raw := mustBuildEvent(t, "", EventFields{Title: "x", Start: utc("20260928T140000Z"), End: utc("20260928T150000Z"),
		Description: "uno\r\nEND:VEVENT\r\nBEGIN:VTODO\nRRULE:FREQ=SECONDLY"})
	obj := mustParse(t, raw)
	if len(obj.events) != 1 || obj.events[0].rrule != nil {
		t.Fatalf("la descripcion abrio algo:\n%s", raw)
	}
	if got := mustFields(t, raw); got.Description != "uno\nEND:VEVENT\nBEGIN:VTODO\nRRULE:FREQ=SECONDLY" {
		t.Fatalf("descripcion: %q", got.Description)
	}
}

// Actualizar la serie conserva lo que la API no expresa; cambiar el inicio retira las excepciones.
func TestEventoConservaLoQueNoConoce(t *testing.T) {
	existing := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple Inc.//macOS 14//EN\r\nX-WR-CALNAME:Trabajo\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Madrid\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:serie@apple\r\nDTSTAMP:20260101T000000Z\r\nSEQUENCE:3\r\nDTSTART;TZID=Europe/Madrid:20260105T100000\r\nDTEND;TZID=Europe/Madrid:20260105T110000\r\n" +
		"SUMMARY;LANGUAGE=es:Reunion\r\nRRULE:FREQ=WEEKLY;BYDAY=MO;BYSETPOS=1\r\nEXDATE;TZID=Europe/Madrid:20260112T100000\r\nATTENDEE;CN=Bea:mailto:bea@acme.test\r\nX-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Aviso\r\nTRIGGER:-PT10M\r\nEND:VALARM\r\n" +
		"BEGIN:VALARM\r\nACTION:EMAIL\r\nDESCRIPTION:Correo\r\nSUMMARY:s\r\nATTENDEE:mailto:ana@acme.test\r\nTRIGGER:-P1D\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:serie@apple\r\nDTSTAMP:20260101T000000Z\r\nRECURRENCE-ID;TZID=Europe/Madrid:20260119T100000\r\nDTSTART;TZID=Europe/Madrid:20260119T120000\r\nDTEND;TZID=Europe/Madrid:20260119T130000\r\nSUMMARY:Movida\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	cur := mustFields(t, existing)
	if cur.Title != "Reunion" || cur.Recurrence == nil || cur.Recurrence.Freq != "weekly" || *cur.ReminderMinutes != 10 ||
		!cur.Start.Equal(utc("20260105T090000Z")) {
		t.Fatalf("lectura: %+v", cur)
	}

	// Solo cambia el lugar: la regla (con partes que la API no expresa), las zonas, las excepciones, los
	// asistentes y los avisos quedan como estaban.
	upd := cur
	upd.Location = "Sala 2"
	raw := mustBuildEvent(t, existing, upd)
	for _, want := range []string{"X-WR-CALNAME:Trabajo", "TZID:Europe/Madrid", "DTSTART;TZID=Europe/Madrid:20260105T100000", "SUMMARY;LANGUAGE=es:Reunion",
		"RRULE:FREQ=WEEKLY;BYDAY=MO;BYSETPOS=1", "EXDATE;TZID=Europe/Madrid:20260112T100000", "ATTENDEE;CN=Bea:mailto:bea@acme.test",
		"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC", "TRIGGER:-PT10M", "ACTION:EMAIL", "RECURRENCE-ID;TZID=Europe/Madrid:20260119T100000",
		"LOCATION:Sala 2", "SEQUENCE:3", "DTSTAMP:20260924T100000Z"} {
		if !strings.Contains(raw, want) {
			t.Errorf("falta %q en\n%s", want, raw)
		}
	}

	// Cambia la hora y el aviso: las horas se escriben en la zona del evento, las excepciones se retiran y el
	// aviso de la API se sustituye sin tocar el de correo.
	upd.Start, upd.End = cur.Start.Add(time.Hour), cur.End.Add(time.Hour)
	upd.ReminderMinutes = minutes(30)
	raw = mustBuildEvent(t, raw, upd)
	for _, want := range []string{"DTSTART;TZID=Europe/Madrid:20260105T110000", "DTEND;TZID=Europe/Madrid:20260105T120000", "RRULE:FREQ=WEEKLY;BYDAY=MO;BYSETPOS=1",
		"TRIGGER:-PT30M", "ACTION:EMAIL", "SEQUENCE:4", "ATTENDEE;CN=Bea:mailto:bea@acme.test"} {
		if !strings.Contains(raw, want) {
			t.Errorf("falta %q en\n%s", want, raw)
		}
	}
	for _, gone := range []string{"EXDATE", "RECURRENCE-ID", "TRIGGER:-PT10M", "Movida"} {
		if strings.Contains(raw, gone) {
			t.Errorf("sobra %q en\n%s", gone, raw)
		}
	}
	if got := mustFields(t, raw); !got.Start.Equal(upd.Start) || *got.ReminderMinutes != 30 || got.Location != "Sala 2" {
		t.Fatalf("despues de cambiar la hora: %+v", got)
	}

	// Quitar la repeticion y el aviso.
	upd.Recurrence, upd.ReminderMinutes = nil, nil
	raw = mustBuildEvent(t, raw, upd)
	if strings.Contains(raw, "RRULE") || strings.Contains(raw, "TRIGGER:-PT30M") || !strings.Contains(raw, "ACTION:EMAIL") {
		t.Fatalf("sin repeticion ni aviso:\n%s", raw)
	}
}

// Una regla que la API no sabe expresar se lee como sin repeticion y, si no se toca, se conserva.
func TestEventoConReglaNoExpresableLaConserva(t *testing.T) {
	existing := ical(vevent("h1", "DTSTART:20260928T090000Z", "DURATION:PT30M", "SUMMARY:Toma", "RRULE:FREQ=HOURLY;INTERVAL=8"))
	cur := mustFields(t, existing)
	if cur.Recurrence != nil || !cur.End.Equal(utc("20260928T093000Z")) {
		t.Fatalf("lectura: %+v", cur)
	}
	cur.Title = "Toma de medicacion"
	raw := mustBuildEvent(t, existing, cur)
	if !strings.Contains(raw, "RRULE:FREQ=HOURLY;INTERVAL=8") || !strings.Contains(raw, "DURATION:PT30M") || !strings.Contains(raw, "SUMMARY:Toma de medicacion") {
		t.Fatalf("regla conservada:\n%s", raw)
	}
}
