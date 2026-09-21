package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var calLimits = CalendarLimits{MaxEventBytes: 16 << 10, MaxEventProperties: 100, MaxEventsPerMailbox: 10, MaxCalendarsPerMailbox: 3, MaxRecurrenceWork: 5000, MaxQueryWork: 50000}

func ical(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Prueba//ES\r\n" + strings.Join(events, "") + "END:VCALENDAR\r\n"
}

func vevent(uid string, props ...string) string {
	return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260101T000000Z\r\n" + strings.Join(props, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func mustParse(t *testing.T, raw string) CalendarObject {
	t.Helper()
	o, err := ParseCalendarObject(raw, calLimits)
	if err != nil {
		t.Fatalf("iCalendar valido rechazado: %v\n%s", err, raw)
	}
	return o
}

func utc(s string) time.Time {
	t, err := time.Parse("20060102T150405Z", s)
	if err != nil {
		panic(err)
	}
	return t
}

func rng(start, end string) TimeRange {
	r, err := ParseTimeRange(start, end)
	if err != nil {
		panic(err)
	}
	return r
}

const madridZone = "BEGIN:VTIMEZONE\r\nTZID:Europe/Madrid\r\nBEGIN:DAYLIGHT\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nDTSTART:19700329T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nEND:DAYLIGHT\r\nBEGIN:STANDARD\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nDTSTART:19701025T030000\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"

func TestParseCalendarObjectComoLoEnviaIOS(t *testing.T) {
	raw := ical(madridZone, vevent("ios-1",
		"SUMMARY:Reunion\\, de equipo",
		"DTSTART;TZID=Europe/Madrid:20260921T100000",
		"DTEND;TZID=Europe/Madrid:20260921T110000",
		"LOCATION:Sala 3"))
	o := mustParse(t, raw)
	if o.UID != "ios-1" || o.Summary != "Reunion, de equipo" {
		t.Fatalf("UID y resumen: %q %q", o.UID, o.Summary)
	}
	if want := utc("20260921T080000Z"); !o.FirstStart.Equal(want) {
		t.Fatalf("FirstStart %v, quiero %v (Madrid en verano es UTC+2)", o.FirstStart, want)
	}
	if o.LastEnd == nil || !o.LastEnd.Equal(utc("20260921T090000Z")) {
		t.Fatalf("LastEnd %v", o.LastEnd)
	}
}

func TestParseCalendarObjectAdmiteAlarmasYSobrescrituras(t *testing.T) {
	master := vevent("serie", "DTSTART:20260105T090000Z", "DTEND:20260105T100000Z", "RRULE:FREQ=WEEKLY;COUNT=4")
	master = strings.Replace(master, "END:VEVENT", "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:x\r\nEND:VALARM\r\nEND:VEVENT", 1)
	override := vevent("serie", "RECURRENCE-ID:20260112T090000Z", "DTSTART:20260113T090000Z", "DTEND:20260113T100000Z")
	o := mustParse(t, ical(master, override))
	if !o.FirstStart.Equal(utc("20260105T090000Z")) || o.LastEnd == nil || !o.LastEnd.Equal(utc("20260126T100000Z")) {
		t.Fatalf("intervalo: %v %v", o.FirstStart, o.LastEnd)
	}
}

func TestParseCalendarObjectRechazaLoInvalido(t *testing.T) {
	ev := vevent("u", "DTSTART:20260101T100000Z")
	cases := map[string]struct {
		raw  string
		kind ICalErrorKind
	}{
		"sin VCALENDAR":        {"BEGIN:VEVENT\r\nEND:VEVENT\r\n", ICalInvalid},
		"sin cierre":           {"BEGIN:VCALENDAR\r\nVERSION:2.0\r\n", ICalInvalid},
		"END que no coincide":  {"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VEVENT\r\n", ICalInvalid},
		"contenido despues":    {ical(ev) + "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n", ICalInvalid},
		"propiedad suelta":     {"X:1\r\n" + ical(ev), ICalInvalid},
		"linea sin valor":      {ical(ev, "NOVALOR"), ICalInvalid},
		"version 1.0":          {strings.Replace(ical(ev), "VERSION:2.0", "VERSION:1.0", 1), ICalInvalid},
		"sin version":          {strings.Replace(ical(ev), "VERSION:2.0\r\n", "", 1), ICalInvalid},
		"METHOD":               {strings.Replace(ical(ev), "VERSION:2.0", "VERSION:2.0\r\nMETHOD:REQUEST", 1), ICalObject},
		"tarea":                {ical("BEGIN:VTODO\r\nUID:t\r\nEND:VTODO\r\n"), ICalComponent},
		"diario":               {ical(ev, "BEGIN:VJOURNAL\r\nUID:t\r\nEND:VJOURNAL\r\n"), ICalComponent},
		"sin componentes":      {ical(), ICalObject},
		"solo zona":            {ical(madridZone), ICalObject},
		"sin UID":              {ical("BEGIN:VEVENT\r\nDTSTART:20260101T100000Z\r\nEND:VEVENT\r\n"), ICalInvalid},
		"UID repetido":         {ical("BEGIN:VEVENT\r\nUID:a\r\nUID:b\r\nDTSTART:20260101T100000Z\r\nEND:VEVENT\r\n"), ICalInvalid},
		"UIDs distintos":       {ical(ev, vevent("otro", "RECURRENCE-ID:20260102T100000Z", "DTSTART:20260102T100000Z")), ICalObject},
		"dos maestros":         {ical(ev, vevent("u", "DTSTART:20260102T100000Z")), ICalObject},
		"sin DTSTART":          {ical(vevent("u", "SUMMARY:x")), ICalInvalid},
		"DTSTART malo":         {ical(vevent("u", "DTSTART:2026-01-01")), ICalInvalid},
		"DTSTART 30 de feb":    {ical(vevent("u", "DTSTART:20260230T100000Z")), ICalInvalid},
		"DTEND y DURATION":     {ical(vevent("u", "DTSTART:20260101T100000Z", "DTEND:20260101T110000Z", "DURATION:PT1H")), ICalInvalid},
		"DTEND anterior":       {ical(vevent("u", "DTSTART:20260101T100000Z", "DTEND:20260101T090000Z")), ICalInvalid},
		"DTEND de otro tipo":   {ical(vevent("u", "DTSTART:20260101T100000Z", "DTEND;VALUE=DATE:20260102")), ICalInvalid},
		"DURATION mala":        {ical(vevent("u", "DTSTART:20260101T100000Z", "DURATION:1H")), ICalInvalid},
		"Z con TZID":           {ical(vevent("u", "DTSTART;TZID=Europe/Madrid:20260101T100000Z")), ICalInvalid},
		"RRULE sin FREQ":       {ical(vevent("u", "DTSTART:20260101T100000Z", "RRULE:COUNT=3")), ICalInvalid},
		"RRULE COUNT y UNTIL":  {ical(vevent("u", "DTSTART:20260101T100000Z", "RRULE:FREQ=DAILY;COUNT=3;UNTIL=20260201T000000Z")), ICalInvalid},
		"RRULE dos":            {ical(vevent("u", "DTSTART:20260101T100000Z", "RRULE:FREQ=DAILY", "RRULE:FREQ=WEEKLY")), ICalInvalid},
		"BYDAY ordinal diario": {ical(vevent("u", "DTSTART:20260101T100000Z", "RRULE:FREQ=DAILY;BYDAY=1MO")), ICalInvalid},
		"BYMONTH 13":           {ical(vevent("u", "DTSTART:20260101T100000Z", "RRULE:FREQ=YEARLY;BYMONTH=13")), ICalInvalid},
		"componente anidado":   {ical(vevent("u", "DTSTART:20260101T100000Z", "BEGIN:VALARM", "BEGIN:X", "END:X", "END:VALARM")), ICalInvalid},
		"VEVENT con otro":      {ical(strings.Replace(ev, "END:VEVENT", "BEGIN:X\r\nEND:X\r\nEND:VEVENT", 1)), ICalInvalid},
		"zona sin TZID":        {ical("BEGIN:VTIMEZONE\r\nBEGIN:STANDARD\r\nTZOFFSETFROM:+0000\r\nTZOFFSETTO:+0000\r\nDTSTART:19700101T000000\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n", ev), ICalInvalid},
		"zona sin tramos":      {ical("BEGIN:VTIMEZONE\r\nTZID:X\r\nEND:VTIMEZONE\r\n", ev), ICalInvalid},
		"zona repetida":        {ical(madridZone, madridZone, ev), ICalInvalid},
		"desfase malo":         {ical(strings.Replace(madridZone, "+0100", "1:00", 1), ev), ICalInvalid},
		"UTF-8 roto":           {ical(vevent("u", "DTSTART:20260101T100000Z", "SUMMARY:\xff\xfe")), ICalInvalid},
		"NUL":                  {ical(vevent("u", "DTSTART:20260101T100000Z", "SUMMARY:a\x00b")), ICalInvalid},
		"CR suelto":            {ical(vevent("u", "DTSTART:20260101T100000Z", "SUMMARY:a\rb")), ICalInvalid},
		"parametro roto":       {ical(vevent("u", "DTSTART;TZID:20260101T100000")), ICalInvalid},
		"grande":               {ical(vevent("u", "DTSTART:20260101T100000Z", "SUMMARY:"+strings.Repeat("A", calLimits.MaxEventBytes))), ICalTooLarge},
		"demasiadas lineas":    {ical(vevent("u", append([]string{"DTSTART:20260101T100000Z"}, repeat("X-A:1", 200)...)...)), ICalInvalid},
	}
	for name, tc := range cases {
		_, err := ParseCalendarObject(tc.raw, calLimits)
		ie, ok := err.(*ICalError)
		if !ok {
			t.Errorf("%s: se esperaba un ICalError y llego %v", name, err)
			continue
		}
		if ie.Kind != tc.kind {
			t.Errorf("%s: tipo %d, quiero %d (%s)", name, ie.Kind, tc.kind, ie.Reason)
		}
		if strings.Contains(ie.Reason, "\x00") || len(ie.Reason) > 200 {
			t.Errorf("%s: la razon no debe repetir el contenido: %q", name, ie.Reason)
		}
	}
}

func TestParseCalendarObjectAcotaElAnidamientoYLosLFSueltos(t *testing.T) {
	lf := strings.ReplaceAll(ical(vevent("lf", "DTSTART:20260101T100000Z")), "\r\n", "\n")
	mustParse(t, lf)
	folded := ical(vevent("plegado", "DTSTART:20260101T100000Z", "SUMMARY:una linea muy larga que\r\n  se pliega en dos"))
	if o := mustParse(t, folded); o.Summary != "una linea muy larga que se pliega en dos" {
		t.Fatalf("resumen plegado: %q", o.Summary)
	}
	deep := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:A\r\nBEGIN:B\r\nBEGIN:C\r\nEND:C\r\nEND:B\r\nEND:A\r\nEND:VCALENDAR\r\n"
	if _, err := ParseCalendarObject(deep, calLimits); err == nil {
		t.Fatal("un anidamiento de cuatro niveles no es un calendario")
	}
}

func TestLaZonaHorariaVieneDeLaBaseIANAOdelVTIMEZONEDelCliente(t *testing.T) {
	custom := strings.NewReplacer("Europe/Madrid", "Hora de Madrid").Replace(madridZone)
	iana := mustParse(t, ical(vevent("a", "DTSTART;TZID=Europe/Madrid:20260315T120000", "DTEND;TZID=Europe/Madrid:20260315T130000")))
	own := mustParse(t, ical(custom, vevent("b", "DTSTART;TZID=Hora de Madrid:20260315T120000", "DTEND;TZID=Hora de Madrid:20260315T130000")))
	if !iana.FirstStart.Equal(own.FirstStart) {
		t.Fatalf("la zona propia debe dar lo mismo que la IANA: %v %v", own.FirstStart, iana.FirstStart)
	}
	// Antes y despues del cambio de hora (29 de marzo y 25 de octubre de 2026).
	for _, tc := range []struct{ local, want string }{
		{"20260101T120000", "20260101T110000Z"},
		{"20260328T120000", "20260328T110000Z"},
		{"20260329T120000", "20260329T100000Z"},
		{"20261024T120000", "20261024T100000Z"},
		{"20261025T120000", "20261025T110000Z"},
		{"20261231T120000", "20261231T110000Z"},
	} {
		o := mustParse(t, ical(custom, vevent("c", "DTSTART;TZID=Hora de Madrid:"+tc.local)))
		if !o.FirstStart.Equal(utc(tc.want)) {
			t.Errorf("%s en la zona propia: %v, quiero %s", tc.local, o.FirstStart, tc.want)
		}
	}
	// Una zona que no es ni IANA ni esta definida se toma como UTC en vez de rechazar el evento.
	unk := mustParse(t, ical(vevent("d", "DTSTART;TZID=Zona Inventada:20260101T120000")))
	if !unk.FirstStart.Equal(utc("20260101T120000Z")) {
		t.Fatalf("zona desconocida: %v", unk.FirstStart)
	}
	// Un TZID con forma de ruta no llega al sistema de ficheros.
	for _, tzid := range []string{"../../etc/passwd", "/etc/localtime", "Local", "a/b/c/d"} {
		if ianaLocation(tzid) != nil {
			t.Errorf("TZID %q no debe resolverse", tzid)
		}
	}
}

// Tabla de RFC 4791 9.9: el solape de un VEVENT con un rango segun tenga DTEND, DURATION, ninguno o sea de dia.
func TestTimeRangeSegunLaTablaDeLaRFC(t *testing.T) {
	cases := []struct {
		name   string
		props  []string
		start  string
		end    string
		expect bool
	}{
		{"DTEND: dentro", []string{"DTSTART:20260601T100000Z", "DTEND:20260601T110000Z"}, "20260601T103000Z", "20260601T120000Z", true},
		{"DTEND: termina justo al empezar el rango", []string{"DTSTART:20260601T100000Z", "DTEND:20260601T110000Z"}, "20260601T110000Z", "20260601T120000Z", false},
		{"DTEND: empieza justo al terminar el rango", []string{"DTSTART:20260601T100000Z", "DTEND:20260601T110000Z"}, "20260601T090000Z", "20260601T100000Z", false},
		{"DTEND: envuelve el rango", []string{"DTSTART:20260601T100000Z", "DTEND:20260601T200000Z"}, "20260601T120000Z", "20260601T130000Z", true},
		{"DURATION positiva", []string{"DTSTART:20260601T100000Z", "DURATION:PT2H"}, "20260601T115900Z", "20260601T130000Z", true},
		{"DURATION positiva fuera", []string{"DTSTART:20260601T100000Z", "DURATION:PT2H"}, "20260601T120000Z", "20260601T130000Z", false},
		{"sin fin: el inicio dentro", []string{"DTSTART:20260601T100000Z"}, "20260601T100000Z", "20260601T110000Z", true},
		{"sin fin: el inicio es el fin del rango", []string{"DTSTART:20260601T100000Z"}, "20260601T090000Z", "20260601T100000Z", false},
		{"sin fin: antes del rango", []string{"DTSTART:20260601T090000Z"}, "20260601T100000Z", "20260601T110000Z", false},
		{"DURATION cero", []string{"DTSTART:20260601T100000Z", "DURATION:PT0S"}, "20260601T100000Z", "20260601T110000Z", true},
		{"dia completo, un dia", []string{"DTSTART;VALUE=DATE:20260601"}, "20260601T120000Z", "20260601T130000Z", true},
		{"dia completo, al dia siguiente", []string{"DTSTART;VALUE=DATE:20260601"}, "20260602T000000Z", "20260602T010000Z", false},
		{"dia completo con DTEND", []string{"DTSTART;VALUE=DATE:20260601", "DTEND;VALUE=DATE:20260603"}, "20260602T120000Z", "20260602T130000Z", true},
		{"solo inicio", []string{"DTSTART:20260601T100000Z", "DTEND:20260601T110000Z"}, "20260601T105900Z", "", true},
		{"solo fin", []string{"DTSTART:20260601T100000Z", "DTEND:20260601T110000Z"}, "", "20260601T100001Z", true},
		{"solo fin, antes", []string{"DTSTART:20260601T100000Z", "DTEND:20260601T110000Z"}, "", "20260601T100000Z", false},
	}
	for _, tc := range cases {
		o := mustParse(t, ical(vevent("r", tc.props...)))
		if got := o.Overlaps(rng(tc.start, tc.end), NewBudget(1000)); got != tc.expect {
			t.Errorf("%s: %v, quiero %v", tc.name, got, tc.expect)
		}
	}
}

func TestParseTimeRangeExigeUTCYOrden(t *testing.T) {
	for _, bad := range [][2]string{{"", ""}, {"20260101", ""}, {"20260101T100000", ""}, {"", "no"}, {"20260102T000000Z", "20260101T000000Z"}, {"20260101T000000Z", "20260101T000000Z"}} {
		if _, err := ParseTimeRange(bad[0], bad[1]); err == nil {
			t.Errorf("time-range %v debia rechazarse", bad)
		}
	}
	if _, err := ParseTimeRange("20260101T000000Z", ""); err != nil {
		t.Fatal(err)
	}
}

// instances devuelve las apariciones de una regla (relojes de pared en UTC) hasta n.
func instances(t *testing.T, dtstart, rule string, n int) []string {
	t.Helper()
	r, err := ParseRRule(rule)
	if err != nil {
		t.Fatal(err)
	}
	start, perr := parseDateTime(dtstart, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	var out []string
	res := r.Each(start, func(w time.Time) time.Time { return w }, NewBudget(100000), time.Time{}, func(w time.Time) bool {
		out = append(out, w.Format("20060102T150405"))
		return len(out) < n
	})
	if res == ExpandIncomplete {
		t.Fatalf("expansion incompleta para %s", rule)
	}
	return out
}

func TestExpansionDeReglasComoLaRFC(t *testing.T) {
	cases := []struct {
		name, dtstart, rule string
		want                []string
	}{
		{"diaria con COUNT", "20260101T090000", "FREQ=DAILY;COUNT=3", []string{"20260101T090000", "20260102T090000", "20260103T090000"}},
		{"cada dos dias", "20260101T090000", "FREQ=DAILY;INTERVAL=2;COUNT=3", []string{"20260101T090000", "20260103T090000", "20260105T090000"}},
		{"UNTIL incluye el dia final", "20260101T090000", "FREQ=DAILY;UNTIL=20260103T090000Z", []string{"20260101T090000", "20260102T090000", "20260103T090000"}},
		{"UNTIL de fecha", "20260101T090000", "FREQ=DAILY;UNTIL=20260102", []string{"20260101T090000", "20260102T090000"}},
		{"semanal lunes y miercoles", "20260105T090000", "FREQ=WEEKLY;BYDAY=MO,WE;COUNT=4", []string{"20260105T090000", "20260107T090000", "20260112T090000", "20260114T090000"}},
		{"semanal sin BYDAY usa el dia de DTSTART", "20260107T090000", "FREQ=WEEKLY;COUNT=2", []string{"20260107T090000", "20260114T090000"}},
		{"cada dos semanas con WKST", "20260104T090000", "FREQ=WEEKLY;INTERVAL=2;BYDAY=SU,MO;WKST=SU;COUNT=4", []string{"20260104T090000", "20260105T090000", "20260118T090000", "20260119T090000"}},
		{"mensual el dia 31 salta los meses cortos", "20260131T090000", "FREQ=MONTHLY;COUNT=3", []string{"20260131T090000", "20260331T090000", "20260531T090000"}},
		{"mensual ultimo viernes", "20260101T090000", "FREQ=MONTHLY;BYDAY=-1FR;COUNT=3", []string{"20260130T090000", "20260227T090000", "20260327T090000"}},
		{"mensual segundo martes", "20260101T090000", "FREQ=MONTHLY;BYDAY=2TU;COUNT=2", []string{"20260113T090000", "20260210T090000"}},
		{"mensual ultimo dia", "20260101T090000", "FREQ=MONTHLY;BYMONTHDAY=-1;COUNT=3", []string{"20260131T090000", "20260228T090000", "20260331T090000"}},
		{"mensual con BYSETPOS el ultimo dia habil", "20260101T090000", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3", []string{"20260130T090000", "20260227T090000", "20260331T090000"}},
		{"anual por omision", "20260315T090000", "FREQ=YEARLY;COUNT=3", []string{"20260315T090000", "20270315T090000", "20280315T090000"}},
		{"anual 29 de febrero solo en bisiestos", "20240229T090000", "FREQ=YEARLY;COUNT=2", []string{"20240229T090000", "20280229T090000"}},
		{"anual ultimo domingo de marzo", "20260329T020000", "FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU;COUNT=3", []string{"20260329T020000", "20270328T020000", "20280326T020000"}},
		{"anual dia del anio", "20260101T090000", "FREQ=YEARLY;BYYEARDAY=100,-1;COUNT=3", []string{"20260410T090000", "20261231T090000", "20270410T090000"}},
		{"diaria con varias horas", "20260101T090000", "FREQ=DAILY;BYHOUR=9,17;COUNT=3", []string{"20260101T090000", "20260101T170000", "20260102T090000"}},
		{"BYMONTH limita la diaria", "20260130T090000", "FREQ=DAILY;BYMONTH=2;COUNT=2", []string{"20260201T090000", "20260202T090000"}},
	}
	for _, tc := range cases {
		got := instances(t, tc.dtstart, tc.rule, 100)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s:\n  obtuve %v\n  quiero %v", tc.name, got, tc.want)
		}
	}
}

func TestReglasQueNoSeSabenExpandirSeMarcan(t *testing.T) {
	for _, rule := range []string{"FREQ=HOURLY;COUNT=3", "FREQ=YEARLY;BYWEEKNO=20;BYDAY=MO", "FREQ=DAILY;RSCALE=HEBREW", "FREQ=WEEKLY;BYYEARDAY=1"} {
		r, err := ParseRRule(rule)
		if err != nil || !r.Unsupported {
			t.Errorf("%s: %v %v", rule, r.Unsupported, err)
		}
		if res := r.Each(dtValue{wall: utc("20260101T000000Z")}, func(w time.Time) time.Time { return w }, NewBudget(100), time.Time{}, func(time.Time) bool { return true }); res != ExpandIncomplete {
			t.Errorf("%s: la expansion debe ser incompleta, no silenciosa", rule)
		}
	}
	// Y una consulta por rango no descarta un evento con una regla asi.
	o := mustParse(t, ical(vevent("h", "DTSTART:20260101T090000Z", "DTEND:20260101T093000Z", "RRULE:FREQ=HOURLY")))
	if !o.Overlaps(rng("20300101T000000Z", "20300102T000000Z"), NewBudget(1000)) {
		t.Fatal("un evento con una regla que no se sabe expandir debe devolverse")
	}
	if o.LastEnd != nil {
		t.Fatal("sin fin conocido no hay cota superior")
	}
}

func TestOverlapsConRecurrenciasExcepcionesYSobrescrituras(t *testing.T) {
	master := vevent("s", "DTSTART:20260105T090000Z", "DTEND:20260105T100000Z", "RRULE:FREQ=WEEKLY;COUNT=5", "EXDATE:20260119T090000Z")
	move := vevent("s", "RECURRENCE-ID:20260112T090000Z", "DTSTART:20260114T150000Z", "DTEND:20260114T160000Z")
	o := mustParse(t, ical(master, move))
	budget := func() *Budget { return NewBudget(10000) }
	cases := []struct {
		name, start, end string
		want             bool
	}{
		{"la primera aparicion", "20260105T000000Z", "20260106T000000Z", true},
		{"una aparicion intermedia", "20260126T000000Z", "20260127T000000Z", true},
		{"la excluida por EXDATE", "20260119T000000Z", "20260120T000000Z", false},
		{"el hueco de la sobrescrita en su fecha original", "20260112T000000Z", "20260113T000000Z", false},
		{"la sobrescrita en su fecha nueva", "20260114T000000Z", "20260115T000000Z", true},
		{"despues de la ultima", "20260209T000000Z", "20260301T000000Z", false},
		{"la ultima aparicion (COUNT=5)", "20260202T000000Z", "20260203T000000Z", true},
		{"antes de la primera", "20251201T000000Z", "20260101T000000Z", false},
	}
	for _, tc := range cases {
		if got := o.Overlaps(rng(tc.start, tc.end), budget()); got != tc.want {
			t.Errorf("%s: %v, quiero %v", tc.name, got, tc.want)
		}
	}
	// Sin fin: el salto de periodos llega lejos sin gastar el presupuesto de recorrer desde el inicio.
	forever := mustParse(t, ical(vevent("f", "DTSTART:20150105T090000Z", "DTEND:20150105T100000Z", "RRULE:FREQ=DAILY")))
	small := NewBudget(50)
	if !forever.Overlaps(rng("20320615T000000Z", "20320616T000000Z"), small) {
		t.Fatal("una diaria sin fin cae en cualquier dia")
	}
	if forever.LastEnd != nil {
		t.Fatal("una recurrencia sin fin no tiene cota superior")
	}
	weekly := mustParse(t, ical(vevent("w", "DTSTART:20150105T090000Z", "DTEND:20150105T100000Z", "RRULE:FREQ=WEEKLY;BYDAY=MO")))
	if weekly.Overlaps(rng("20320615T100000Z", "20320615T110000Z"), NewBudget(50)) {
		t.Fatal("un martes no es lunes: con el salto de periodos se decide con poco trabajo")
	}
}

func TestElPresupuestoAcotaLaExpansionYNuncaOmiteUnEvento(t *testing.T) {
	// Una regla que nunca coincide (30 de febrero) recorreria anios sin producir nada.
	never := mustParse(t, ical(vevent("n", "DTSTART:20260101T090000Z", "DTEND:20260101T100000Z", "RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30")))
	if !never.Overlaps(rng("20400101T000000Z", "20400102T000000Z"), NewBudget(200)) {
		t.Fatal("agotar el presupuesto no puede descartar el evento")
	}
	// Un evento hostil con millones de apariciones por periodo se corta al momento.
	hostile := mustParse(t, ical(vevent("x", "DTSTART:20260101T090000Z", "DTEND:20260101T100000Z",
		"RRULE:FREQ=YEARLY;BYYEARDAY="+allDays()+";BYHOUR="+allNumbers(24)+";BYMINUTE="+allNumbers(60)+";BYSECOND="+allNumbers(60))))
	began := time.Now()
	hostile.Overlaps(rng("20400101T000000Z", "20400102T000000Z"), NewBudget(20000))
	if time.Since(began) > 2*time.Second {
		t.Fatalf("la expansion hostil tardo %v", time.Since(began))
	}
	// El presupuesto de la consulta se reparte: uno agotado deja sin trabajo a los siguientes, que se devuelven.
	shared := NewBudget(300)
	f := CalendarFilter{Comps: []CompFilter{{Name: "VEVENT", Range: &TimeRange{Start: ptr(utc("20400101T000000Z")), End: ptr(utc("20400102T000000Z"))}}}}
	for i := 0; i < 3; i++ {
		if !f.Matches(never, shared, 200) {
			t.Fatalf("evento %d: sin presupuesto se devuelve", i)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func allNumbers(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprint(i)
	}
	return strings.Join(parts, ",")
}

func allDays() string {
	parts := make([]string, 0, 366)
	for i := 1; i <= 366; i++ {
		parts = append(parts, fmt.Sprint(i))
	}
	return strings.Join(parts, ",")
}

func TestFiltrosDeCalendarQuery(t *testing.T) {
	o := mustParse(t, ical(vevent("f", "SUMMARY:Reunion de Presupuesto", "DTSTART:20260601T100000Z", "DTEND:20260601T110000Z", "LOCATION:Sala 3")))
	tm := func(text string, negate bool) []PropFilter {
		return []PropFilter{{Name: "SUMMARY", Matches: []TextMatch{{Text: text, Collation: CollationASCIICasemap, Type: MatchContains, Negate: negate}}}}
	}
	event := func(mut func(*CompFilter)) CalendarFilter {
		c := CompFilter{Name: "VEVENT"}
		mut(&c)
		return CalendarFilter{Comps: []CompFilter{c}}
	}
	budget := NewBudget(1000)
	for name, tc := range map[string]struct {
		filter CalendarFilter
		want   bool
	}{
		"sin filtros admite todo":      {CalendarFilter{}, true},
		"VEVENT sin condiciones":       {event(func(*CompFilter) {}), true},
		"texto que esta":               {event(func(c *CompFilter) { c.Props = tm("presupuesto", false) }), true},
		"texto que no esta":            {event(func(c *CompFilter) { c.Props = tm("vacaciones", false) }), false},
		"negado":                       {event(func(c *CompFilter) { c.Props = tm("vacaciones", true) }), true},
		"la propiedad no existe":       {event(func(c *CompFilter) { c.Props = []PropFilter{{Name: "DESCRIPTION", IsNotDefined: true}} }), true},
		"la propiedad no debe existir": {event(func(c *CompFilter) { c.Props = []PropFilter{{Name: "LOCATION", IsNotDefined: true}} }), false},
		"rango que lo alcanza":         {event(func(c *CompFilter) { r := rng("20260601T000000Z", "20260602T000000Z"); c.Range = &r }), true},
		"rango que no lo alcanza":      {event(func(c *CompFilter) { r := rng("20260701T000000Z", "20260702T000000Z"); c.Range = &r }), false},
		"rango y texto, los dos": {event(func(c *CompFilter) {
			r := rng("20260601T000000Z", "20260602T000000Z")
			c.Range = &r
			c.Props = tm("nada", false)
		}), false},
		"VEVENT is-not-defined":            {event(func(c *CompFilter) { c.IsNotDefined = true }), false},
		"VTODO no existe: no hay ninguno":  {CalendarFilter{Comps: []CompFilter{{Name: "VTODO"}}}, false},
		"VTODO is-not-defined: se cumple":  {CalendarFilter{Comps: []CompFilter{{Name: "VTODO", IsNotDefined: true}}}, true},
		"VEVENT y VTODO is-not-defined":    {CalendarFilter{Comps: []CompFilter{{Name: "VEVENT"}, {Name: "VTODO", IsNotDefined: true}}}, true},
		"VEVENT o VTODO exigidos: los dos": {CalendarFilter{Comps: []CompFilter{{Name: "VEVENT"}, {Name: "VTODO"}}}, false},
	} {
		if got := tc.filter.Matches(o, budget, 100); got != tc.want {
			t.Errorf("%s: %v, quiero %v", name, got, tc.want)
		}
	}
	w := CalendarFilter{Comps: []CompFilter{
		{Name: "VEVENT", Range: &TimeRange{Start: ptr(utc("20260101T000000Z")), End: ptr(utc("20260301T000000Z"))}},
		{Name: "VEVENT", Range: &TimeRange{Start: ptr(utc("20260201T000000Z")), End: ptr(utc("20260401T000000Z"))}},
		{Name: "VEVENT", IsNotDefined: true, Range: &TimeRange{Start: ptr(utc("20300101T000000Z"))}},
	}}.Window()
	if w.Start == nil || !w.Start.Equal(utc("20260201T000000Z")) || w.End == nil || !w.End.Equal(utc("20260301T000000Z")) {
		t.Fatalf("la ventana es la interseccion de los rangos: %+v", w)
	}
	if (CalendarFilter{}).Window() != (EventWindow{}) {
		t.Fatal("sin rango no hay ventana")
	}
}

func TestRRuleRechazaLoMalFormado(t *testing.T) {
	for _, rule := range []string{"", "FREQ", "FREQ=", "FREQ=SOMETIMES", "FREQ=DAILY;FREQ=DAILY", "FREQ=DAILY;INTERVAL=0", "FREQ=DAILY;INTERVAL=1001",
		"FREQ=DAILY;COUNT=0", "FREQ=DAILY;UNTIL=manana", "FREQ=WEEKLY;WKST=XX", "FREQ=MONTHLY;BYMONTHDAY=0",
		"FREQ=MONTHLY;BYMONTHDAY=32", "FREQ=YEARLY;BYDAY=0MO", "FREQ=YEARLY;BYDAY=54MO", "FREQ=DAILY;BYHOUR=24", "FREQ=DAILY;BYSECOND=60", "FREQ=DAILY;;=x",
		"FREQ=DAILY;BYHOUR=" + strings.Repeat("1,", 500) + "1"} {
		if _, err := ParseRRule(rule); err == nil {
			t.Errorf("%q debia rechazarse", rule)
		}
	}
}

func TestDuracionesDeRFC5545(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"PT1H": time.Hour, "P1D": 24 * time.Hour, "P1W": 7 * 24 * time.Hour, "PT1H30M": 90 * time.Minute, "P1DT2H": 26 * time.Hour,
		"PT0S": 0, "-PT15M": -15 * time.Minute, "+P2D": 48 * time.Hour,
	} {
		got, err := parseDuration(in)
		if err != nil || got != want {
			t.Errorf("%s: %v %v, quiero %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "P", "PT", "1H", "P1H", "PT1D", "P1DT", "PTT1H", "P99999999999D", "P40000D", "PxD"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("%q debia rechazarse", bad)
		}
	}
}

func TestLosEventosSinCambiosIndexanElMismoIntervalo(t *testing.T) {
	o := mustParse(t, ical(vevent("d", "DTSTART;VALUE=DATE:20260601", "DTEND;VALUE=DATE:20260603")))
	if !o.FirstStart.Equal(utc("20260601T000000Z")) || o.LastEnd == nil || !o.LastEnd.Equal(utc("20260603T000000Z")) {
		t.Fatalf("dia completo: %v %v", o.FirstStart, o.LastEnd)
	}
	withUntil := mustParse(t, ical(vevent("u", "DTSTART:20260601T090000Z", "DTEND:20260601T100000Z", "RRULE:FREQ=DAILY;UNTIL=20260610T090000Z")))
	if withUntil.LastEnd == nil || !withUntil.LastEnd.Equal(utc("20260610T100000Z")) {
		t.Fatalf("UNTIL acota: %v", withUntil.LastEnd)
	}
	rdates := mustParse(t, ical(vevent("r", "DTSTART:20260601T090000Z", "DTEND:20260601T100000Z", "RDATE:20260701T090000Z,20260501T090000Z")))
	if !rdates.FirstStart.Equal(utc("20260501T090000Z")) || rdates.LastEnd == nil || !rdates.LastEnd.Equal(utc("20260701T100000Z")) {
		t.Fatalf("RDATE: %v %v", rdates.FirstStart, rdates.LastEnd)
	}
	period := mustParse(t, ical(vevent("p", "DTSTART:20260601T090000Z", "RDATE;VALUE=PERIOD:20260701T090000Z/PT1H")))
	if period.LastEnd != nil || !period.Overlaps(rng("20300101T000000Z", "20300102T000000Z"), NewBudget(10)) {
		t.Fatal("un RDATE con periodos no se evalua: sin cota y siempre candidato")
	}
}
