package domain

import "testing"

// Costo por evento candidato de un calendar-query: leer el iCalendar guardado y decidir con exactitud si toca
// el rango. QueryEvents lo paga por cada fila que la base no descarto por tiempo (hasta el tope de eventos por
// buzon si el filtro no trae ventana), asi que estos numeros dimensionan el CPU de la consulta.
//
//	go test -run '^$' -bench BenchmarkCalendarQuery -benchmem ./services/mail-dav/internal/domain
func benchQuery(b *testing.B, raw string, workPerEvent int) {
	b.Helper()
	window := rng("20260301T000000Z", "20260308T000000Z")
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o, err := ParseStoredCalendarObject(raw)
		if err != nil {
			b.Fatal(err)
		}
		_ = o.Overlaps(window, NewBudget(workPerEvent))
	}
}

func BenchmarkCalendarQueryPlainEvent(b *testing.B) {
	benchQuery(b, ical(vevent("plain", "DTSTART:20260305T100000Z", "DTEND:20260305T110000Z", "SUMMARY:Reunion", "DESCRIPTION:"+repeatString("x", 150))), 1000)
}

func BenchmarkCalendarQueryDailyRecurrence(b *testing.B) {
	benchQuery(b, ical(vevent("daily", "DTSTART:20250101T100000Z", "DTEND:20250101T110000Z", "RRULE:FREQ=DAILY;COUNT=2000")), 20000)
}

func BenchmarkCalendarQueryUnboundedWeeklyRecurrence(b *testing.B) {
	benchQuery(b, ical(vevent("weekly", "DTSTART:20200101T100000Z", "DTEND:20200101T110000Z", "RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR")), 20000)
}

func repeatString(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
