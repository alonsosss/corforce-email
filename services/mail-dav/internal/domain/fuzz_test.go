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
