package domain

import "testing"

func starts(occs []Occurrence) []string {
	out := make([]string, len(occs))
	for i, o := range occs {
		out[i] = o.Start.UTC().Format(layoutStamp)
	}
	return out
}

func expectStarts(t *testing.T, name string, occs []Occurrence, want ...string) {
	t.Helper()
	got := starts(occs)
	if len(got) != len(want) {
		t.Fatalf("%s: quiero %v, tengo %v", name, want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: quiero %v, tengo %v", name, want, got)
		}
	}
}

func occurrencesOf(t *testing.T, raw, start, end string) []Occurrence {
	t.Helper()
	return mustParse(t, raw).Occurrences(rng(start, end), NewBudget(100000))
}

func TestOccurrencesDeUnEventoSuelto(t *testing.T) {
	raw := ical(vevent("a", "DTSTART:20260928T140000Z", "DTEND:20260928T150000Z", "SUMMARY:Cita", "LOCATION:Sala"))
	occs := occurrencesOf(t, raw, "20260901T000000Z", "20261001T000000Z")
	expectStarts(t, "suelto", occs, "20260928T140000Z")
	if o := occs[0]; o.Recurring || o.Title != "Cita" || o.Location != "Sala" || !o.End.Equal(utc("20260928T150000Z")) {
		t.Fatalf("aparicion: %+v", o)
	}
	if occs := occurrencesOf(t, raw, "20261001T000000Z", "20261101T000000Z"); len(occs) != 0 {
		t.Fatalf("fuera del rango: %v", starts(occs))
	}
	if occs := mustParse(t, raw).Occurrences(TimeRange{}, NewBudget(10)); occs != nil {
		t.Fatal("sin extremos no se expande")
	}
}

func TestOccurrencesDiariaConCount(t *testing.T) {
	raw := ical(vevent("d", "DTSTART:20260928T080000Z", "DURATION:PT1H", "RRULE:FREQ=DAILY;COUNT=4"))
	occs := occurrencesOf(t, raw, "20260929T000000Z", "20261031T000000Z")
	expectStarts(t, "diaria con COUNT", occs, "20260929T080000Z", "20260930T080000Z", "20261001T080000Z")
	if !occs[0].Recurring {
		t.Fatal("una serie es recurring")
	}
}

func TestOccurrencesSemanalConByDayYExdate(t *testing.T) {
	raw := ical(vevent("w", "DTSTART;TZID=Europe/Madrid:20260105T100000", "DTEND;TZID=Europe/Madrid:20260105T110000",
		"RRULE:FREQ=WEEKLY;BYDAY=MO,WE;UNTIL=20260121T235959Z", "EXDATE;TZID=Europe/Madrid:20260114T100000"))
	occs := occurrencesOf(t, raw, "20260101T000000Z", "20260201T000000Z")
	expectStarts(t, "semanal", occs, "20260105T090000Z", "20260107T090000Z", "20260112T090000Z", "20260119T090000Z", "20260121T090000Z")
}

// La hora de pared se conserva en el cambio de horario: en Madrid pasa de UTC+1 a UTC+2 el 29 de marzo.
func TestOccurrencesRespetaElHorarioDeVerano(t *testing.T) {
	raw := ical(vevent("v", "DTSTART;TZID=Europe/Madrid:20260323T100000", "DURATION:PT1H", "RRULE:FREQ=WEEKLY"))
	occs := occurrencesOf(t, raw, "20260320T000000Z", "20260404T000000Z")
	expectStarts(t, "horario de verano", occs, "20260323T090000Z", "20260330T080000Z")
}

func TestOccurrencesMensualYAnual(t *testing.T) {
	monthly := ical(vevent("m", "DTSTART:20260131T120000Z", "DURATION:PT1H", "RRULE:FREQ=MONTHLY;UNTIL=20260601T000000Z"))
	// El 31 solo existe en algunos meses (RFC 5545: las fechas invalidas se ignoran).
	expectStarts(t, "mensual", occurrencesOf(t, monthly, "20260101T000000Z", "20260701T000000Z"),
		"20260131T120000Z", "20260331T120000Z", "20260531T120000Z")
	yearly := ical(vevent("y", "DTSTART;VALUE=DATE:20200229", "RRULE:FREQ=YEARLY"))
	occs := occurrencesOf(t, yearly, "20240101T000000Z", "20240301T000000Z")
	expectStarts(t, "anual en bisiesto", occs, "20240229T000000Z")
	if !occs[0].AllDay || !occs[0].End.Equal(utc("20240301T000000Z")) {
		t.Fatalf("dia completo: %+v", occs[0])
	}
}

func TestOccurrencesConSobrescrituraYRdate(t *testing.T) {
	raw := ical(
		vevent("s", "DTSTART:20260928T140000Z", "DURATION:PT1H", "SUMMARY:Serie", "RRULE:FREQ=DAILY;COUNT=3", "RDATE:20261010T090000Z"),
		vevent("s", "RECURRENCE-ID:20260929T140000Z", "DTSTART:20260929T170000Z", "DURATION:PT1H", "SUMMARY:Movida"),
	)
	occs := occurrencesOf(t, raw, "20260901T000000Z", "20261031T000000Z")
	expectStarts(t, "sobrescritura", occs, "20260928T140000Z", "20260929T170000Z", "20260930T140000Z", "20261010T090000Z")
	if occs[1].Title != "Movida" || !occs[1].Recurring || occs[0].Title != "Serie" {
		t.Fatalf("textos: %+v", occs)
	}
}

// Lo que no se sabe expandir aparece solo en su primera ocurrencia, marcado como serie.
func TestOccurrencesDeLoQueNoSeSabeExpandir(t *testing.T) {
	raw := ical(vevent("h", "DTSTART:20260928T080000Z", "DURATION:PT10M", "RRULE:FREQ=HOURLY"))
	occs := occurrencesOf(t, raw, "20260928T000000Z", "20260929T000000Z")
	expectStarts(t, "horaria", occs, "20260928T080000Z")
	if !occs[0].Recurring {
		t.Fatal("marcada como serie")
	}
	if occs := occurrencesOf(t, raw, "20261001T000000Z", "20261002T000000Z"); len(occs) != 0 {
		t.Fatalf("despues de la primera no aparece: %v", starts(occs))
	}
	period := ical(vevent("p", "DTSTART:20260928T080000Z", "DURATION:PT1H", "RDATE;VALUE=PERIOD:20261001T080000Z/PT1H"))
	expectStarts(t, "RDATE con periodos", occurrencesOf(t, period, "20260901T000000Z", "20261031T000000Z"), "20260928T080000Z")
}

// Con el presupuesto agotado lo expandido se conserva y el resto no se inventa.
func TestOccurrencesConElPresupuestoAgotado(t *testing.T) {
	raw := ical(vevent("b", "DTSTART:20200101T080000Z", "DURATION:PT1H", "RRULE:FREQ=DAILY;COUNT=100000"))
	obj := mustParse(t, raw)
	occs := obj.Occurrences(rng("20200101T000000Z", "20200110T000000Z"), NewBudget(6))
	if len(occs) == 0 || len(occs) >= 9 || !occs[0].Start.Equal(utc("20200101T080000Z")) || !occs[0].Recurring {
		t.Fatalf("presupuesto corto: %v", starts(occs))
	}
	// Con COUNT no se pueden saltar periodos: una ventana lejana agota el presupuesto y no devuelve nada.
	if occs := obj.Occurrences(rng("20240101T000000Z", "20240110T000000Z"), NewBudget(1000)); len(occs) != 0 {
		t.Fatalf("ventana lejana: %v", starts(occs))
	}
	b := NewBudget(1000)
	obj.Occurrences(rng("20200101T000000Z", "20200301T000000Z"), b)
	if b.Spend(1000) {
		t.Fatal("la expansion gasta del presupuesto")
	}
}
