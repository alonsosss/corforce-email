package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// El VTIMEZONE generado reproduce la base IANA: leido con el evaluador propio (como lo leeria un cliente que no
// conoce el nombre de la zona) da los mismos instantes que la zona real, antes y despues de cada cambio de hora y
// en los dos hemisferios.
func TestVTimezoneGeneradoReproduceLaZona(t *testing.T) {
	for _, name := range []string{"Europe/Madrid", "America/New_York", "Australia/Sydney", "America/Lima", "America/Santiago", "Asia/Kolkata"} {
		t.Run(name, func(t *testing.T) {
			loc := mustLoc(t, name)
			var w contentWriter
			w.line("BEGIN:VCALENDAR")
			w.line("VERSION:2.0")
			tz := buildVTimezone(loc, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
			for i, l := range tz.props {
				if l.name == "TZID" {
					tz.props[i] = generated("TZID:Propia-Zona")
				}
			}
			tz.write(&w)
			w.line("BEGIN:VEVENT")
			w.line("UID:z")
			w.line("DTSTART;TZID=Propia-Zona:20260105T100000")
			w.line("END:VEVENT")
			w.line("END:VCALENDAR")
			obj := mustParse(t, w.String())
			if obj.zones.custom["Propia-Zona"] == nil {
				t.Fatalf("sin VTIMEZONE propio:\n%s", w.String())
			}
			for day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); day.Year() < 2031; day = day.AddDate(0, 0, 5) {
				for _, hour := range []int{1, 10, 23} {
					wall := time.Date(day.Year(), day.Month(), day.Day(), hour, 30, 0, 0, time.UTC)
					want := time.Date(wall.Year(), wall.Month(), wall.Day(), hour, 30, 0, 0, loc).UTC()
					if got := obj.zones.toInstant("Propia-Zona", wall); !got.Equal(want) {
						t.Fatalf("%s %v: VTIMEZONE %v, zona %v\n%s", name, wall, got, want, w.String())
					}
				}
			}
		})
	}
}

// Una serie semanal con zona conserva su hora de pared a traves del cambio de horario: en Madrid, las 10:00 son
// las 08:00 UTC en verano y las 09:00 en invierno.
func TestSerieSemanalConZonaNoSeCorre(t *testing.T) {
	madrid := mustLoc(t, "Europe/Madrid")
	start := time.Date(2026, 10, 19, 10, 0, 0, 0, madrid)
	raw := mustBuildEvent(t, "", EventFields{Title: "Comite", Start: start.UTC(), End: start.Add(time.Hour).UTC(), TimeZone: "Europe/Madrid",
		Recurrence: &Recurrence{Freq: "weekly", Count: 4}})
	for _, want := range []string{"BEGIN:VTIMEZONE", "TZID:Europe/Madrid", "BEGIN:DAYLIGHT", "BEGIN:STANDARD", "RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU",
		"DTSTART;TZID=Europe/Madrid:20261019T100000", "DTEND;TZID=Europe/Madrid:20261019T110000"} {
		if !strings.Contains(raw, want) {
			t.Errorf("falta %q en\n%s", want, raw)
		}
	}
	from, to := utc("20261001T000000Z"), utc("20261201T000000Z")
	occs := mustParse(t, raw).Occurrences(TimeRange{Start: &from, End: &to}, NewBudget(10000))
	if len(occs) != 4 {
		t.Fatalf("apariciones: %+v", occs)
	}
	for _, o := range occs {
		if l := o.Start.In(madrid); l.Hour() != 10 || l.Minute() != 0 {
			t.Fatalf("la serie se corrio: %v", l)
		}
	}
	if occs[0].Start.Hour() != 8 || occs[3].Start.Hour() != 9 {
		t.Fatalf("UTC antes y despues del cambio: %v %v", occs[0].Start, occs[3].Start)
	}
	got := mustFields(t, raw)
	if got.TimeZone != "Europe/Madrid" || !got.Start.Equal(start) {
		t.Fatalf("lectura: %+v", got)
	}
}

func TestZonaHorariaValidaYCambio(t *testing.T) {
	base := EventFields{Title: "x", Start: utc("20260928T140000Z"), End: utc("20260928T150000Z")}
	bad := base
	bad.TimeZone = "../etc/passwd"
	var fe *FieldError
	if _, err := bad.Normalize(); !errors.As(err, &fe) || fe.Field != "timezone" {
		t.Fatalf("zona invalida: %v", err)
	}
	allDay := base
	allDay.AllDay, allDay.TimeZone = true, "Europe/Madrid"
	if n, err := allDay.Normalize(); err != nil || n.TimeZone != "" {
		t.Fatalf("un dia completo no lleva zona: %+v %v", n, err)
	}

	raw := mustBuildEvent(t, "", base)
	if strings.Contains(raw, "VTIMEZONE") || !strings.Contains(raw, "DTSTART:20260928T140000Z") {
		t.Fatalf("sin zona va en UTC:\n%s", raw)
	}
	upd := mustFields(t, raw)
	upd.TimeZone = "America/Lima"
	raw = mustBuildEvent(t, raw, upd)
	if !strings.Contains(raw, "DTSTART;TZID=America/Lima:20260928T090000") || !strings.Contains(raw, "TZID:America/Lima") {
		t.Fatalf("cambio de zona:\n%s", raw)
	}
	got := mustFields(t, raw)
	if got.TimeZone != "America/Lima" || !got.Start.Equal(base.Start) {
		t.Fatalf("lectura: %+v", got)
	}
	// Sin zona en la peticion se conserva la del evento.
	got.TimeZone, got.Title = "", "y"
	raw = mustBuildEvent(t, raw, got)
	if !strings.Contains(raw, "DTSTART;TZID=America/Lima:20260928T090000") {
		t.Fatalf("se perdio la zona:\n%s", raw)
	}
	// UTC la quita.
	got.TimeZone = "UTC"
	got.Start = got.Start.Add(time.Hour)
	got.End = got.End.Add(time.Hour)
	raw = mustBuildEvent(t, raw, got)
	if !strings.Contains(raw, "DTSTART:20260928T150000Z") {
		t.Fatalf("a UTC:\n%s", raw)
	}
}

func weeklySeries(t *testing.T) string {
	t.Helper()
	madrid := mustLoc(t, "Europe/Madrid")
	start := time.Date(2026, 10, 5, 10, 0, 0, 0, madrid)
	return mustBuildEvent(t, "", EventFields{Title: "Semanal", Start: start.UTC(), End: start.Add(time.Hour).UTC(), TimeZone: "Europe/Madrid",
		Recurrence: &Recurrence{Freq: "weekly", Count: 6},
		Organizer:  &Party{Email: "ana@acme.test", Name: "Ana"}, Attendees: []Attendee{{Email: "bea@acme.test"}}})
}

func seriesOccurrences(t *testing.T, raw string) []Occurrence {
	t.Helper()
	from, to := utc("20260901T000000Z"), utc("20261231T000000Z")
	return mustParse(t, raw).Occurrences(TimeRange{Start: &from, End: &to}, NewBudget(10000))
}

// Editar una sola aparicion escribe su sobrescritura; borrarla anade su EXDATE. La serie queda igual.
func TestEditarYBorrarUnaAparicion(t *testing.T) {
	raw := weeklySeries(t)
	occs := seriesOccurrences(t, raw)
	if len(occs) != 6 {
		t.Fatalf("serie: %d", len(occs))
	}
	second := occs[1].RecurrenceID
	f, err := EventFields{Title: "Movida", Start: second.Add(2 * time.Hour), End: second.Add(3 * time.Hour), Location: "Sala 9"}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	edited, err := SetOccurrence(raw, second, f, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCalendarObject(edited, calLimits); err != nil {
		t.Fatalf("la sobrescritura no valida: %v\n%s", err, edited)
	}
	for _, want := range []string{"RECURRENCE-ID;TZID=Europe/Madrid:20261012T100000", "DTSTART;TZID=Europe/Madrid:20261012T120000",
		"SUMMARY:Movida", "LOCATION:Sala 9", "ATTENDEE", "SEQUENCE:1"} {
		if !strings.Contains(edited, want) {
			t.Errorf("falta %q en\n%s", want, edited)
		}
	}
	occs = seriesOccurrences(t, edited)
	if len(occs) != 6 || occs[1].Title != "Movida" || !occs[1].Start.Equal(second.Add(2*time.Hour)) || !occs[1].RecurrenceID.Equal(second) {
		t.Fatalf("despues de editar: %+v", occs)
	}
	// Editarla otra vez reemplaza la sobrescritura, no anade otra.
	f.Title = "Otra vez"
	edited, err = SetOccurrence(edited, second, f, stamp)
	if err != nil || strings.Count(edited, "RECURRENCE-ID") != 1 || !strings.Contains(edited, "SUMMARY:Otra vez") {
		t.Fatalf("segunda edicion: %v\n%s", err, edited)
	}

	// Borrar la editada retira la sobrescritura y anade su EXDATE; borrar la primera (el DTSTART) tambien vale.
	deleted, err := DeleteOccurrence(edited, second, stamp)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err = DeleteOccurrence(deleted, occs[0].RecurrenceID, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(deleted, "RECURRENCE-ID") || !strings.Contains(deleted, "EXDATE;TZID=Europe/Madrid:20261012T100000") {
		t.Fatalf("borrado:\n%s", deleted)
	}
	occs = seriesOccurrences(t, deleted)
	if len(occs) != 4 {
		t.Fatalf("quedan %d apariciones", len(occs))
	}
	if _, err := DeleteOccurrence(deleted, second, stamp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("una aparicion ya borrada: %v", err)
	}
	if _, err := DeleteOccurrence(deleted, second.Add(time.Minute), stamp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("un instante que no es aparicion: %v", err)
	}
	single := mustBuildEvent(t, "", EventFields{Title: "x", Start: utc("20260928T140000Z"), End: utc("20260928T150000Z")})
	var fe *FieldError
	if _, err := DeleteOccurrence(single, utc("20260928T140000Z"), stamp); !errors.As(err, &fe) {
		t.Fatalf("un evento sin repeticion: %v", err)
	}
	allDay, _ := EventFields{Title: "x", Start: utc("20261012T000000Z"), End: utc("20261013T000000Z"), AllDay: true}.Normalize()
	if _, err := SetOccurrence(raw, second, allDay, stamp); !errors.As(err, &fe) || fe.Field != "all_day" {
		t.Fatalf("cambiar el tipo de dia de una aparicion: %v", err)
	}
}

// Ida y vuelta de iTIP: el organizador invita (REQUEST), el invitado acepta (se guarda en su calendario sin METHOD
// y sale un REPLY), el REPLY actualiza al organizador y la cancelacion (CANCEL) sube la secuencia.
func TestInvitacionIdaYVuelta(t *testing.T) {
	organizer := weeklySeries(t)
	out, err := BuildInvitation(organizer, MethodRequest, stamp, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Recipients) != 1 || out.Recipients[0] != "bea@acme.test" || !strings.Contains(out.ICal, "METHOD:REQUEST") || strings.Contains(out.ICal, "VALARM") {
		t.Fatalf("REQUEST: %+v\n%s", out, out.ICal)
	}
	inv, err := ParseInvitation(out.ICal, calLimits)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Method != MethodRequest || inv.Organizer.Email != "ana@acme.test" || !inv.Recurring || inv.TimeZone != "Europe/Madrid" || inv.Summary != "Semanal" {
		t.Fatalf("invitacion leida: %+v", inv)
	}
	if strings.Contains(inv.Object, "METHOD") {
		t.Fatalf("lo que se guarda no lleva METHOD:\n%s", inv.Object)
	}
	var fe *FieldError
	if _, err := RespondToInvitation(inv, []string{"otro@acme.test"}, PartStatAccepted, stamp); !errors.As(err, &fe) || fe.Field != "attendee" {
		t.Fatalf("un buzon no invitado: %v", err)
	}
	if _, err := RespondToInvitation(inv, []string{"bea@acme.test"}, PartStatNeedsAction, stamp); !errors.As(err, &fe) {
		t.Fatalf("respuesta no admitida: %v", err)
	}
	res, err := RespondToInvitation(inv, []string{"BEA@acme.test"}, "accepted", stamp)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attendee != "bea@acme.test" || !strings.Contains(res.Stored, "PARTSTAT=ACCEPTED") || strings.Contains(res.Stored, "METHOD") {
		t.Fatalf("guardado: %+v", res)
	}
	if _, err := ParseCalendarObject(res.Stored, calLimits); err != nil {
		t.Fatalf("lo guardado no es un objeto valido: %v", err)
	}
	reply, err := ParseInvitation(res.Reply, calLimits)
	if err != nil {
		t.Fatalf("REPLY: %v\n%s", err, res.Reply)
	}
	if reply.Method != MethodReply || len(reply.Attendees) != 1 || reply.Attendees[0].PartStat != PartStatAccepted {
		t.Fatalf("REPLY leido: %+v", reply)
	}

	if _, _, err := ApplyReply(organizer, reply, "mallory@evil.test"); !errors.As(err, &fe) {
		t.Fatalf("una respuesta de otro remitente: %v", err)
	}
	updated, changed, err := ApplyReply(organizer, reply, "bea@acme.test")
	if err != nil || !changed {
		t.Fatalf("aplicar: %v %v", changed, err)
	}
	f := mustFields(t, updated)
	if len(f.Attendees) != 1 || f.Attendees[0].PartStat != PartStatAccepted {
		t.Fatalf("el organizador no ve la respuesta: %+v", f.Attendees)
	}
	if _, changed, _ := ApplyReply(updated, reply, "bea@acme.test"); changed {
		t.Fatal("repetir la respuesta no cambia nada")
	}
	// Una respuesta a una version anterior (SEQUENCE menor) no cuenta.
	newer := strings.Replace(updated, "SEQUENCE:0", "SEQUENCE:5", 1)
	if _, changed, _ := ApplyReply(newer, reply, "bea@acme.test"); changed {
		t.Fatal("una respuesta antigua no cuenta")
	}
	if !OrganizedBy(organizer, []string{"ANA@acme.test"}) || OrganizedBy(organizer, []string{"bea@acme.test"}) {
		t.Fatal("OrganizedBy")
	}

	cancel, err := BuildInvitation(organizer, MethodCancel, stamp, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseInvitation(cancel.ICal, calLimits)
	if err != nil {
		t.Fatalf("CANCEL: %v\n%s", err, cancel.ICal)
	}
	if c.Method != MethodCancel || c.Sequence != 1 || !strings.Contains(cancel.ICal, "STATUS:CANCELLED") {
		t.Fatalf("CANCEL leido: %+v\n%s", c, cancel.ICal)
	}
	if _, err := BuildInvitation(mustBuildEvent(t, "", EventFields{Title: "x", Start: utc("20260928T140000Z"), End: utc("20260928T150000Z")}), MethodRequest, stamp, false); !errors.As(err, &fe) {
		t.Fatalf("un evento sin reunion no se envia: %v", err)
	}
}

// Cambiar la hora pide respuesta de nuevo; los invitados sin cambios conservan su linea.
func TestInvitadosAlActualizar(t *testing.T) {
	raw := weeklySeries(t)
	f := mustFields(t, raw)
	f.Attendees[0].PartStat = PartStatAccepted
	f.Attendees = append(f.Attendees, Attendee{Email: "carla@acme.test", Name: "Carla, Ventas"})
	f.Organizer = &Party{Email: "ana@acme.test"}
	raw = mustBuildEvent(t, raw, f)
	if !strings.Contains(raw, `ATTENDEE;CN="Carla, Ventas"`) || !strings.Contains(raw, "PARTSTAT=ACCEPTED") {
		t.Fatalf("invitados:\n%s", raw)
	}
	got := mustFields(t, raw)
	if len(got.Attendees) != 2 || got.Attendees[1].Name != "Carla, Ventas" || got.Organizer == nil || got.Organizer.Name != "Ana" {
		t.Fatalf("lectura: %+v %+v", got.Attendees, got.Organizer)
	}
	got.Start, got.End = got.Start.Add(time.Hour), got.End.Add(time.Hour)
	raw = mustBuildEvent(t, raw, got)
	if unfolded := strings.ReplaceAll(raw, "\r\n ", ""); strings.Contains(unfolded, "PARTSTAT=ACCEPTED") || strings.Count(unfolded, "RSVP=TRUE") != 2 {
		t.Fatalf("al cambiar la hora se pide respuesta de nuevo:\n%s", raw)
	}
	got = mustFields(t, raw)
	got.Attendees = nil
	raw = mustBuildEvent(t, raw, got)
	if strings.Contains(raw, "ATTENDEE") || strings.Contains(raw, "ORGANIZER") {
		t.Fatalf("sin invitados no hay reunion:\n%s", raw)
	}
	if _, err := (EventFields{Title: "x", Start: utc("20260928T140000Z"), End: utc("20260928T150000Z"), Attendees: []Attendee{{Email: "no es correo"}}}).Normalize(); err == nil {
		t.Fatal("direccion invalida")
	}
}

func TestOcupacionMaterializada(t *testing.T) {
	plan := BusyPlan{From: utc("20260901T000000Z"), To: utc("20261101T000000Z"), MaxIntervals: 100}
	ev, err := NewEventAt("s.ics", weeklySeries(t), calLimits, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.BusyPlanned || len(ev.Busy) != 4 || ev.BusyUntil == nil || !ev.BusyUntil.Equal(plan.To) {
		t.Fatalf("serie mas alla del horizonte: %+v %v", ev.Busy, ev.BusyUntil)
	}
	plan.To = utc("20270101T000000Z")
	ev, _ = NewEventAt("s.ics", weeklySeries(t), calLimits, plan)
	if len(ev.Busy) != 6 || ev.BusyUntil != nil {
		t.Fatalf("serie completa: %d %v", len(ev.Busy), ev.BusyUntil)
	}
	plan.MaxIntervals = 2
	ev, _ = NewEventAt("s.ics", weeklySeries(t), calLimits, plan)
	if len(ev.Busy) != 2 || ev.BusyUntil == nil || !ev.BusyUntil.Equal(ev.Busy[1].Start.AddDate(0, 0, 7)) {
		t.Fatalf("tope de tramos: %+v %v", ev.Busy, ev.BusyUntil)
	}
	plan.MaxIntervals = 100
	for _, raw := range []string{
		ical(vevent("t", "DTSTART:20261001T100000Z", "DTEND:20261001T110000Z", "TRANSP:TRANSPARENT")),
		ical(vevent("c", "DTSTART:20261001T100000Z", "DTEND:20261001T110000Z", "STATUS:CANCELLED")),
		ical(vevent("d", "DTSTART;VALUE=DATE:20261001")),
	} {
		if ev, err := NewEventAt("x.ics", raw, calLimits, plan); err != nil || len(ev.Busy) != 0 {
			t.Fatalf("libre: %v %+v", err, ev.Busy)
		}
	}
	if ev, _ := NewEventAt("x.ics", ical(vevent("o", "DTSTART;VALUE=DATE:20261001", "TRANSP:OPAQUE")), calLimits, plan); len(ev.Busy) != 1 {
		t.Fatal("un dia completo opaco ocupa")
	}
	if ev, _ := NewEventAt("x.ics", weeklySeries(t), calLimits, BusyPlan{}); ev.BusyPlanned || ev.Busy != nil {
		t.Fatal("sin plan no se materializa")
	}
	merged := MergeIntervals([]Interval{{utc("20261001T100000Z"), utc("20261001T110000Z")}, {utc("20261001T103000Z"), utc("20261001T120000Z")}, {utc("20261001T130000Z"), utc("20261001T140000Z")}})
	if len(merged) != 2 || !merged[0].End.Equal(utc("20261001T120000Z")) {
		t.Fatalf("fusion: %+v", merged)
	}
}

func bookingPage(t *testing.T) BookingPage {
	t.Helper()
	s := BookingSettings{Title: "Demo de producto", DurationMinutes: 30, BufferMinutes: 15, MinNoticeMinutes: 120, MaxAdvanceDays: 14,
		DailyLimit: 10, TimeZone: "America/Lima", Active: true}
	s.Weekly[time.Monday] = []DayWindow{{Start: 9 * 60, End: 11 * 60}}
	s.Weekly[time.Wednesday] = []DayWindow{{Start: 15 * 60, End: 16 * 60}}
	n, err := s.Normalize(50)
	if err != nil {
		t.Fatal(err)
	}
	return BookingPage{BookingSettings: n, OwnerAddress: "ana@acme.test", OwnerName: "Ana"}
}

func TestHuecosDeLaPaginaDeCitas(t *testing.T) {
	p := bookingPage(t)
	lima := mustLoc(t, "America/Lima")
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, lima) // lunes
	from, to := now, now.AddDate(0, 0, 7)
	slots := p.Slots(from, to, now, nil, 100)
	// El lunes la antelacion (2 h) deja fuera 9:00 y 9:30; quedan 10:00 y 10:30. El miercoles, 15:00 y 15:30.
	var got []string
	for _, s := range slots {
		got = append(got, s.Start.In(lima).Format("Mon 15:04"))
	}
	if strings.Join(got, ",") != "Mon 10:00,Mon 10:30,Wed 15:00,Wed 15:30" {
		t.Fatalf("huecos: %v", got)
	}
	// Una reunion de 15:10 a 15:20 bloquea sus huecos y los vecinos dentro del margen.
	busy := []Interval{{time.Date(2026, 9, 30, 15, 10, 0, 0, lima).UTC(), time.Date(2026, 9, 30, 15, 20, 0, 0, lima).UTC()}}
	if slots := p.Slots(from, to, now, busy, 100); len(slots) != 2 {
		t.Fatalf("con ocupacion: %v", slots)
	}
	if !p.SlotAvailable(slots[0].Start, now, nil) || p.SlotAvailable(slots[0].Start.Add(5*time.Minute), now, nil) ||
		p.SlotAvailable(time.Date(2026, 9, 28, 9, 0, 0, 0, lima).UTC(), now, nil) {
		t.Fatal("SlotAvailable")
	}
	if p.SlotAvailable(slots[0].Start.AddDate(0, 0, 21), now, nil) {
		t.Fatal("mas alla de la antelacion maxima")
	}
	if len(p.Slots(from, to, now, nil, 1)) != 1 {
		t.Fatal("tope de huecos")
	}

	ev := p.BookingEventFields(BookingRequest{Start: slots[0].Start, Name: "Luis", Email: "luis@cliente.test", Note: "Hola"})
	n, err := ev.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	raw := mustBuildEvent(t, "", n)
	out, err := BuildInvitation(raw, MethodRequest, stamp, true)
	if err != nil || len(out.Recipients) != 1 || out.Recipients[0] != "luis@cliente.test" || strings.Contains(out.ICal, "Hola") || !strings.Contains(raw, "Hola") {
		t.Fatalf("la invitacion al visitante no lleva su nota: %v %+v\n%s", err, out, out.ICal)
	}
}

func TestConfiguracionDeCitasInvalida(t *testing.T) {
	base := bookingPage(t).BookingSettings
	cases := map[string]func(s *BookingSettings){
		"title":            func(s *BookingSettings) { s.Title = " " },
		"duration_minutes": func(s *BookingSettings) { s.DurationMinutes = 1 },
		"daily_limit":      func(s *BookingSettings) { s.DailyLimit = 51 },
		"timezone":         func(s *BookingSettings) { s.TimeZone = "Marte/Olympus" },
		"weekly.MO":        func(s *BookingSettings) { s.Weekly[time.Monday] = []DayWindow{{540, 600}, {570, 660}} },
		"weekly":           func(s *BookingSettings) { s.Weekly = [7][]DayWindow{} },
	}
	for field, mut := range cases {
		s := base
		mut(&s)
		var fe *FieldError
		if _, err := s.Normalize(50); !errors.As(err, &fe) || fe.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	if _, err := (BookingRequest{Start: stamp, Name: "x", Email: "x@y"}).Normalize(); err == nil {
		t.Fatal("correo invalido")
	}
	if _, err := (BookingRequest{Start: stamp, Name: "", Email: "x@y.test"}).Normalize(); err == nil {
		t.Fatal("nombre obligatorio")
	}
	if m, err := ParseClock("h", "24:00"); err != nil || m != 1440 || FormatClock(m) != "24:00" {
		t.Fatal("ParseClock")
	}
	if _, err := ParseClock("h", "7:00"); err == nil {
		t.Fatal("hora mal formada")
	}
	if !ValidPublicID("abcdefghijklmnopqrstuv") || ValidPublicID("corto") || ValidPublicID("abcdefghijklmnopqrstuv/..") {
		t.Fatal("ValidPublicID")
	}
}

// Lo que llega por correo no rompe el analizador de invitaciones ni lo que se deriva de el.
func FuzzParseInvitation(f *testing.F) {
	f.Add("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTART:20261001T100000Z\r\nORGANIZER:mailto:a@b.test\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:c@d.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	f.Add("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:a\r\nRECURRENCE-ID;TZID=X:20261001T100000\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:c@d.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	f.Add("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTART;VALUE=DATE:20261001\r\nSTATUS:CANCELLED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	f.Fuzz(func(t *testing.T, raw string) {
		inv, err := ParseInvitation(raw, fuzzLimits)
		if err != nil {
			return
		}
		switch inv.Method {
		case MethodRequest:
			if res, err := RespondToInvitation(inv, []string{"c@d.test"}, PartStatTentative, stamp); err == nil {
				if _, err := ParseCalendarObject(res.Stored, CalendarLimits{MaxEventBytes: 1 << 20, MaxEventProperties: 10000}); err != nil {
					t.Fatalf("la respuesta guardada no es valida: %v", err)
				}
				if _, err := ParseInvitation(res.Reply, CalendarLimits{MaxEventBytes: 1 << 20, MaxEventProperties: 10000}); err != nil {
					t.Fatalf("el REPLY no se lee: %v\n%q", err, res.Reply)
				}
			}
		case MethodReply:
			_, _, _ = ApplyReply(weeklySeriesRaw, inv, "c@d.test")
		}
	})
}

var weeklySeriesRaw = ical(vevent("a", "DTSTART:20261001T100000Z", "DTEND:20261001T110000Z", "RRULE:FREQ=WEEKLY;COUNT=3",
	"ORGANIZER:mailto:a@b.test", "ATTENDEE:mailto:c@d.test"))
