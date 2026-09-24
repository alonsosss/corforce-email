package domain

import (
	"testing"
	"time"
)

var fuzzLimits = CalendarLimits{MaxEventBytes: 64 << 10, MaxEventProperties: 200, MaxEventsPerMailbox: 10, MaxCalendarsPerMailbox: 2, MaxRecurrenceWork: 2000, MaxQueryWork: 20000}

// Ninguna entrada, por rara que sea, hace entrar en panico a los analizadores: son la superficie que alcanza
// cualquier cuerpo de un PUT, y un panico en un manejador cierra la conexion del cliente.
func FuzzParseCalendarObject(f *testing.F) {
	f.Add(ical(madridZone, vevent("a", "DTSTART;TZID=Europe/Madrid:20260921T100000", "DTEND;TZID=Europe/Madrid:20260921T110000", "RRULE:FREQ=WEEKLY;BYDAY=MO,WE;COUNT=10")))
	f.Add(ical(vevent("b", "DTSTART;VALUE=DATE:20260921", "RRULE:FREQ=MONTHLY;BYDAY=-1FR;BYSETPOS=-1", "EXDATE;VALUE=DATE:20261021", "RDATE:20261121T100000Z")))
	f.Add(ical(vevent("c", "DTSTART:20260101T000000Z", "DURATION:P1W", "RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29;UNTIL=20400101T000000Z")))
	f.Fuzz(func(t *testing.T, raw string) {
		began := time.Now()
		obj, err := ParseCalendarObject(raw, fuzzLimits)
		if err == nil {
			start, end := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
			obj.Overlaps(TimeRange{Start: &start, End: &end}, NewBudget(fuzzLimits.MaxQueryWork))
		}
		if d := time.Since(began); d > 250*time.Millisecond {
			t.Fatalf("una entrada de %d bytes tardo %v", len(raw), d)
		}
	})
}

func FuzzParseVCard(f *testing.F) {
	f.Add("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nFN:Ana\r\nEMAIL;TYPE=WORK:ana@acme.test\r\nEND:VCARD\r\n")
	f.Add("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:y\r\nN:;Ana;;;\r\nNOTE:linea\r\n larga\r\nEND:VCARD\r\n")
	f.Fuzz(func(t *testing.T, raw string) {
		card, err := ParseVCard(raw, Limits{MaxVCardBytes: 64 << 10, MaxVCardProperties: 200})
		if err != nil {
			return
		}
		card.DisplayName()
		card.Emails()
		Filter{Props: []PropFilter{{Name: "FN", Matches: []TextMatch{{Text: "a", Collation: CollationUnicodeCasemap, Type: MatchContains}}}}}.Matches(card)
	})
}

// Cualquier texto que la validacion admita vuelve igual por el vCard y el iCalendar generados: ningun caracter
// abre una propiedad ni rompe el plegado.
func FuzzTextosEstructurados(f *testing.F) {
	f.Add("Ana; Jr", "linea\ncon, comas\\ y ; puntos")
	f.Add("ñandú áéíóú 漢字", "\\n literal y END:VCARD")
	f.Fuzz(func(t *testing.T, name, notes string) {
		c, err := ContactFields{Name: name, Notes: notes}.Normalize()
		if err != nil {
			return
		}
		raw, err := BuildVCard("", "u", c, time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		got, err := ContactFieldsOf(raw)
		if err != nil || got.Name != c.displayName() || got.Notes != c.Notes {
			t.Fatalf("vCard: %v %+v\n%q", err, got, raw)
		}
		start := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
		e, err := EventFields{Title: name, Description: notes, Start: start, End: start}.Normalize()
		if err != nil {
			return
		}
		ics, err := BuildCalendarObject("", "u", e, time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		ev, err := EventFieldsOf(ics)
		if err != nil || ev.Title != e.Title || ev.Description != e.Description {
			t.Fatalf("iCalendar: %v %+v\n%q", err, ev, ics)
		}
	})
}

// Actualizar desde la API cualquier objeto que el servidor haya aceptado deja un objeto que el servidor acepta.
func FuzzActualizarObjetosAceptados(f *testing.F) {
	f.Add(ical(madridZone, vevent("a", "DTSTART;TZID=Europe/Madrid:20260921T100000", "DTEND;TZID=Europe/Madrid:20260921T110000", "SUMMARY:x", "RRULE:FREQ=WEEKLY;BYDAY=MO,WE;COUNT=10", "EXDATE;TZID=Europe/Madrid:20260923T100000")),
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nFN:Ana\r\nitem1.EMAIL;TYPE=WORK:ana@acme.test\r\nitem1.X-ABLabel:x\r\nEND:VCARD\r\n")
	f.Add(ical(vevent("b", "DTSTART;VALUE=DATE:20260921", "SUMMARY:y", "RRULE:FREQ=MONTHLY;BYDAY=-1FR;BYSETPOS=-1", "EXDATE;VALUE=DATE:20261021"),
		vevent("b", "RECURRENCE-ID;VALUE=DATE:20261121", "DTSTART;VALUE=DATE:20261122", "SUMMARY:z")),
		"BEGIN:VCARD\r\nVERSION:4.0\r\nUID:y\r\nN:;Ana;;;\r\nTEL;VALUE=uri:tel:+1\r\nEND:VCARD\r\n")
	big := CalendarLimits{MaxEventBytes: 1 << 20, MaxEventProperties: 10000}
	f.Fuzz(func(t *testing.T, ics, vcf string) {
		if _, err := ParseCalendarObject(ics, fuzzLimits); err == nil {
			cur, err := EventFieldsOf(ics)
			if err != nil {
				t.Fatalf("un objeto aceptado no se lee: %v", err)
			}
			for _, mod := range []func(*EventFields){
				func(e *EventFields) { e.Location = "Sala 1" },
				func(e *EventFields) { e.End = e.End.Add(time.Hour) },
				func(e *EventFields) {
					e.Start, e.End = e.Start.Add(time.Hour), e.End.Add(time.Hour)
					e.ReminderMinutes = nil
				},
				func(e *EventFields) { e.AllDay = !e.AllDay; e.Recurrence = &Recurrence{Freq: "daily", Count: 3} },
			} {
				e := cur
				mod(&e)
				n, err := e.Normalize()
				if err != nil {
					continue
				}
				out, err := BuildCalendarObject(ics, "u", n, time.Unix(0, 0))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ParseCalendarObject(out, big); err != nil {
					t.Fatalf("la actualizacion no es valida: %v\n%q\n%q", err, ics, out)
				}
			}
		}
		if _, err := ParseVCard(vcf, Limits{MaxVCardBytes: 64 << 10, MaxVCardProperties: 200}); err == nil {
			cur, err := ContactFieldsOf(vcf)
			if err != nil {
				t.Fatalf("un vCard aceptado no se lee: %v", err)
			}
			cur.Title = "Cargo"
			cur.Emails = append(cur.Emails, TypedValue{Value: "nuevo@x.test", Type: "work"})
			n, err := cur.Normalize()
			if err != nil {
				return
			}
			out, err := BuildVCard(vcf, "u", n, time.Unix(0, 0))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseVCard(out, Limits{MaxVCardBytes: 1 << 20, MaxVCardProperties: 10000}); err != nil {
				t.Fatalf("la actualizacion no es valida: %v\n%q\n%q", err, vcf, out)
			}
		}
	})
}

func FuzzParseRRule(f *testing.F) {
	f.Add("FREQ=DAILY;COUNT=5")
	f.Add("FREQ=MONTHLY;BYDAY=1MO,-1FR;BYSETPOS=1,-1;UNTIL=20300101T000000Z")
	f.Add("FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=-1;BYHOUR=1,2;BYMINUTE=0,30")
	f.Fuzz(func(t *testing.T, value string) {
		r, err := ParseRRule(value)
		if err != nil {
			return
		}
		start := dtValue{wall: time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC), utc: true}
		r.Each(start, func(w time.Time) time.Time { return w }, NewBudget(5000), time.Time{}, func(time.Time) bool { return true })
	})
}
