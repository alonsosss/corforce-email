package domain

import (
	"runtime"
	"strings"
	"testing"
)

// allocated mide los bytes que reserva f: una medida determinista del trabajo, a diferencia del tiempo.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// Un vCard con cientos de miles de continuaciones de linea no puede costar tiempo cuadratico: unir cada
// continuacion copiando la linea acumulada reserva del orden de bytes al cuadrado.
func TestUnfoldEsLinealEnLasContinuaciones(t *testing.T) {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nFN:a\r\nNOTE:")
	for b.Len() < 250<<10 {
		b.WriteString("\r\n x")
	}
	b.WriteString("\r\nEND:VCARD\r\n")
	raw := b.String()

	bytes := allocated(func() {
		if _, err := ParseVCard(raw, Limits{MaxVCardBytes: 256 << 10, MaxVCardProperties: 500}); err != nil {
			t.Fatal(err)
		}
	})
	if limit := uint64(len(raw)) * 32; bytes > limit {
		t.Fatalf("un vCard de %d bytes reservo %d: el desdoblado no es lineal", len(raw), bytes)
	}

	cal := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:x\r\nDTSTART:20260101T100000Z\r\nDESCRIPTION:" + strings.Repeat("\r\n x", 60<<10) + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	lim := CalendarLimits{MaxEventBytes: 256 << 10, MaxEventProperties: 1000, MaxRecurrenceWork: 1000, MaxQueryWork: 1000}
	bytes = allocated(func() {
		if _, err := ParseCalendarObject(cal, lim); err != nil {
			t.Fatal(err)
		}
	})
	if limit := uint64(len(cal)) * 64; bytes > limit {
		t.Fatalf("un iCalendar de %d bytes reservo %d: el desdoblado no es lineal", len(cal), bytes)
	}
}

// Releer un objeto guardado para decidir una consulta no debe volver a calcular su indice: la expansion de
// un COUNT grande cuesta decenas de milisegundos por evento y una consulta relee todos los candidatos.
func TestReleerUnObjetoGuardadoNoExpandeSuRecurrencia(t *testing.T) {
	raw := ical(vevent("serie", "DTSTART:20200101T100000Z", "RRULE:FREQ=DAILY;COUNT=400000"))
	lim := CalendarLimits{MaxEventBytes: 16 << 10, MaxEventProperties: 100, MaxRecurrenceWork: 5000, MaxQueryWork: 50000}
	if _, err := ParseCalendarObject(raw, lim); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(3, func() {
		if _, err := ParseStoredCalendarObject(raw); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 500 {
		t.Fatalf("releer un objeto guardado hizo %.0f reservas: esta expandiendo la recurrencia", allocs)
	}
}

// Un TZID con forma de nombre de zona pero que no existe se busca una vez: cada aparicion de una serie lo
// convierte a instante, y repetir la busqueda fallida en el sistema de zonas cuesta decenas de
// microsegundos por aparicion.
func TestUnaZonaInexistenteSeBuscaUnaVez(t *testing.T) {
	ianaLocation("Fabula/Inexistente")
	if allocs := testing.AllocsPerRun(50, func() { ianaLocation("Fabula/Inexistente") }); allocs != 0 {
		t.Fatalf("la zona inexistente se vuelve a buscar en cada uso (%.0f reservas)", allocs)
	}
	if ianaLocation("Fabula/Inexistente") != nil {
		t.Fatal("una zona inexistente no puede resolverse")
	}
}

// La cache de zonas inexistentes esta acotada: los nombres los elige el cliente.
func TestLaCacheDeZonasInexistentesEstaAcotada(t *testing.T) {
	for i := 0; i < maxMissingZones*2; i++ {
		ianaLocation("Zona/N" + strings.Repeat("x", 1+i%40) + string(rune('A'+i%26)) + strings.Repeat("y", i/26%20))
	}
	missingZones.mu.Lock()
	n := len(missingZones.names)
	missingZones.mu.Unlock()
	if n > maxMissingZones {
		t.Fatalf("la cache de zonas inexistentes crecio a %d, tope %d", n, maxMissingZones)
	}
}
