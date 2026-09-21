package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

func event(uid, summary, start, end string, extra ...string) string {
	props := append([]string{"SUMMARY:" + summary, "DTSTART:" + start, "DTEND:" + end}, extra...)
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260101T000000Z\r\n" + strings.Join(props, "\r\n") + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func putEvent(t *testing.T, uc *app.UseCase, p domain.Principal, slug, res, raw string, cond domain.Precondition) (bool, error) {
	t.Helper()
	_, created, err := uc.PutEvent(context.Background(), p, slug, res, raw, cond)
	return created, err
}

func seedCalendar(t *testing.T, uc *app.UseCase, p domain.Principal, slug, uid string) {
	t.Helper()
	if _, err := uc.CreateCalendar(context.Background(), p, slug, slug, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := putEvent(t, uc, p, slug, uid+".ics", event(uid, uid, "20260921T100000Z", "20260921T110000Z"), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
}

func rangeOf(start, end string) domain.TimeRange {
	tr, err := domain.ParseTimeRange(start, end)
	if err != nil {
		panic(err)
	}
	return tr
}

func TestElCalendarioPorDefectoSeCreaUnaVez(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		cals, err := uc.Calendars(ctx, ana.Principal)
		if err != nil || len(cals) != 1 || cals[0].Slug != app.DefaultCalendarSlug || cals[0].DisplayName != "Calendario" {
			t.Fatalf("calendarios: %+v %v", cals, err)
		}
	}
	// La libreta y el calendario por defecto son colecciones distintas.
	if books := booksOf(t, uc, ana.Principal); len(books) != 1 || books[0].ID == mustCalendars(t, uc)[0].ID {
		t.Fatalf("libretas: %+v", books)
	}
}

func mustCalendars(t *testing.T, uc *app.UseCase) []domain.Calendar {
	t.Helper()
	cals, err := uc.Calendars(context.Background(), ana.Principal)
	if err != nil {
		t.Fatal(err)
	}
	return cals
}

func TestCrearCalendariosValidaNombresYLimite(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "Mayusculas", "", ""); !errors.Is(err, domain.ErrInvalidName) {
		t.Fatalf("slug invalido: %v", err)
	}
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "x", strings.Repeat("a", domain.MaxDisplayNameLength+1), ""); !errors.Is(err, domain.ErrInvalidName) {
		t.Fatalf("nombre largo: %v", err)
	}
	cal, err := uc.CreateCalendar(ctx, ana.Principal, "uno", "  ", "")
	if err != nil || cal.DisplayName != "uno" {
		t.Fatalf("sin nombre se usa el slug: %+v %v", cal, err)
	}
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "uno", "", ""); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("repetido: %v", err)
	}
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "dos", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "tres", "", ""); !errors.Is(err, domain.ErrCalendarLimit) {
		t.Fatalf("limite de calendarios: %v", err)
	}
	// El limite es por buzon: el de otro no lo comparte.
	if _, err := uc.CreateCalendar(ctx, cris.Principal, "uno", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := uc.DeleteCalendar(ctx, ana.Principal, "Mala"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar con un slug invalido: %v", err)
	}
}

func TestEventosSeGuardanValidadosYAcotados(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "agenda", "", ""); err != nil {
		t.Fatal(err)
	}
	raw := event("a", "A", "20260921T100000Z", "20260921T110000Z")
	etag, created, err := uc.PutEvent(ctx, ana.Principal, "agenda", "a.ics", raw, domain.Precondition{})
	if err != nil || !created || etag != domain.ETagOf(raw) {
		t.Fatalf("alta: %q %v %v", etag, created, err)
	}
	got, err := uc.Event(ctx, ana.Principal, "agenda", "a.ics")
	if err != nil || got.ICal != raw || got.UID != "a" || got.Summary != "A" || got.LastEnd == nil {
		t.Fatalf("lo guardado se devuelve tal cual, con su indice: %+v %v", got, err)
	}
	var bad *domain.ICalError
	for name, tc := range map[string]struct {
		res, raw string
		want     error
	}{
		"nombre invalido":   {"a.txt", raw, domain.ErrInvalidName},
		"nombre de vcard":   {"a.vcf", raw, domain.ErrInvalidName},
		"no es iCalendar":   {"b.ics", "hola", nil},
		"demasiado grande":  {"c.ics", event("c", strings.Repeat("A", calendarLimits.MaxEventBytes), "20260921T100000Z", "20260921T110000Z"), nil},
		"demasiadas lineas": {"d.ics", event("d", "D", "20260921T100000Z", "20260921T110000Z", strings.Split(strings.Repeat("X-A:1\n", calendarLimits.MaxEventProperties), "\n")...), nil},
	} {
		_, err := putEvent(t, uc, ana.Principal, "agenda", tc.res, tc.raw, domain.Precondition{})
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if tc.want == nil && !errors.As(err, &bad) {
			t.Errorf("%s: se esperaba un ICalError y llego %v", name, err)
		}
	}
	if _, err := putEvent(t, uc, ana.Principal, "Invalido", "a.ics", raw, domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("slug invalido: %v", err)
	}
	if _, err := putEvent(t, uc, ana.Principal, "noexiste", "a.ics", raw, domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("calendario inexistente: %v", err)
	}
}

func TestEventosUIDUnicoPrecondicionesYLimite(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "agenda", "", ""); err != nil {
		t.Fatal(err)
	}
	e := func(uid string) string { return event(uid, uid, "20260921T100000Z", "20260921T110000Z") }
	if _, err := putEvent(t, uc, ana.Principal, "agenda", "a.ics", e("mismo"), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	var conflict *domain.UIDConflictError
	if _, err := putEvent(t, uc, ana.Principal, "agenda", "b.ics", e("mismo"), domain.Precondition{}); !errors.As(err, &conflict) || conflict.Resource != "a.ics" {
		t.Fatalf("el UID no se repite en un calendario: %v", err)
	}
	// El mismo UID en otro calendario del buzon si vale.
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "otro", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := putEvent(t, uc, ana.Principal, "otro", "a.ics", e("mismo"), domain.Precondition{}); err != nil {
		t.Fatalf("otro calendario: %v", err)
	}
	if _, err := putEvent(t, uc, ana.Principal, "agenda", "a.ics", e("nuevo"), domain.Precondition{IfNoneMatchAny: true}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("If-None-Match: * sobre uno que existe: %v", err)
	}
	if _, err := putEvent(t, uc, ana.Principal, "agenda", "a.ics", e("nuevo"), domain.Precondition{IfMatch: []string{"viejo"}}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("If-Match viejo: %v", err)
	}
	// El limite cuenta los eventos de todos los calendarios del buzon.
	if _, err := putEvent(t, uc, ana.Principal, "otro", "b.ics", e("b"), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := putEvent(t, uc, ana.Principal, "otro", "c.ics", e("c"), domain.Precondition{}); !errors.Is(err, domain.ErrEventLimit) {
		t.Fatalf("limite de eventos: %v", err)
	}
	if _, err := putEvent(t, uc, cris.Principal, "x", "c.ics", e("c"), domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otro buzon: %v", err)
	}
	if err := uc.DeleteEvent(ctx, ana.Principal, "agenda", "a.ics", domain.Precondition{IfMatch: []string{"viejo"}}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("borrar con un etag viejo: %v", err)
	}
	if err := uc.DeleteEvent(ctx, ana.Principal, "agenda", "a.ics", domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	if err := uc.DeleteEvent(ctx, ana.Principal, "agenda", "a.ics", domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar dos veces: %v", err)
	}
	if err := uc.DeleteEvent(ctx, ana.Principal, "agenda", "a.txt", domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar un nombre invalido: %v", err)
	}
}

func TestConsultaDeEventosDecideConExactitudSobreLoQueLaBaseDescarta(t *testing.T) {
	uc, _, store := newUseCase(t)
	ctx := context.Background()
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "agenda", "", ""); err != nil {
		t.Fatal(err)
	}
	for res, raw := range map[string]string{
		"a.ics": event("a", "Reunion", "20260921T100000Z", "20260921T110000Z"),
		"b.ics": event("b", "Serie", "20260105T100000Z", "20260105T110000Z", "RRULE:FREQ=WEEKLY;BYDAY=MO"),
		"c.ics": event("c", "Ya paso", "20250101T100000Z", "20250101T110000Z"),
	} {
		if _, err := putEvent(t, uc, ana.Principal, "agenda", res, raw, domain.Precondition{}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(r domain.TimeRange) string {
		_, evs, err := uc.QueryEvents(ctx, ana.Principal, "agenda", domain.CalendarFilter{Comps: []domain.CompFilter{{Name: "VEVENT", Range: &r}}})
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(evs))
		for i, e := range evs {
			out[i] = e.ResourceName
		}
		return strings.Join(out, ",")
	}
	// Un lunes: el evento y la serie; un martes solo lo que cae en el.
	if got := names(rangeOf("20260921T000000Z", "20260922T000000Z")); got != "a.ics,b.ics" {
		t.Fatalf("lunes: %s", got)
	}
	if got := names(rangeOf("20260922T000000Z", "20260923T000000Z")); got != "" {
		t.Fatalf("martes: %s", got)
	}
	if got := names(rangeOf("20250101T000000Z", "20250102T000000Z")); got != "c.ics" {
		t.Fatalf("el pasado: %s", got)
	}
	// La base descarta sin leer: con un rango lejano la serie sin fin es el unico candidato.
	_, candidates, err := store.ListEvents(ctx, ana.Principal, "agenda", domain.CalendarFilter{Comps: []domain.CompFilter{{Name: "VEVENT", Range: ptrRange(rangeOf("20300101T000000Z", "20300102T000000Z"))}}}.Window())
	if err != nil || len(candidates) != 1 || candidates[0].ResourceName != "b.ics" {
		t.Fatalf("candidatos: %+v %v", candidates, err)
	}
	// Un evento guardado que ya no se puede leer no rompe la consulta ni aparece en ella.
	broken := domain.Event{ID: uuid.New(), ResourceName: "roto.ics", UID: "roto", ICal: "esto ya no es un iCalendar", ETag: domain.ETagOf("x"), FirstStart: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)}
	if _, err := store.PutEvent(ctx, ana.Principal, "agenda", broken, domain.Precondition{}, 10, 10); err != nil {
		t.Fatal(err)
	}
	if got := names(rangeOf("20260921T000000Z", "20260922T000000Z")); got != "a.ics,b.ics" {
		t.Fatalf("con un evento ilegible: %s", got)
	}
}

func ptrRange(r domain.TimeRange) *domain.TimeRange { return &r }

func TestElPresupuestoDeConsultaNoOmiteEventos(t *testing.T) {
	// Con un presupuesto minimo una serie que nunca coincide no se puede decidir y se devuelve.
	uc2, _, _ := newUseCaseWith(t, app.Config{Limits: limits, DefaultAddressbookName: "x", DefaultCalendarName: "y",
		Calendar: domain.CalendarLimits{MaxEventBytes: 4096, MaxEventProperties: 60, MaxEventsPerMailbox: 100, MaxCalendarsPerMailbox: 2, MaxRecurrenceWork: 3, MaxQueryWork: 6}})
	ctx := context.Background()
	if _, err := uc2.CreateCalendar(ctx, ana.Principal, "agenda", "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		raw := event(fmt.Sprintf("s%d", i), "Serie", "20260105T100000Z", "20260105T110000Z", "RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30")
		if _, err := putEvent(t, uc2, ana.Principal, "agenda", fmt.Sprintf("s%d.ics", i), raw, domain.Precondition{}); err != nil {
			t.Fatal(err)
		}
	}
	r := rangeOf("20400101T000000Z", "20400102T000000Z")
	_, evs, err := uc2.QueryEvents(ctx, ana.Principal, "agenda", domain.CalendarFilter{Comps: []domain.CompFilter{{Name: "VEVENT", Range: &r}}})
	if err != nil || len(evs) != 5 {
		t.Fatalf("todas las series se devuelven aunque no se puedan decidir: %d %v", len(evs), err)
	}
}

func TestSincronizacionDeCalendarios(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	if _, err := uc.CreateCalendar(ctx, ana.Principal, "agenda", "", ""); err != nil {
		t.Fatal(err)
	}
	e := func(uid string) string { return event(uid, uid, "20260921T100000Z", "20260921T110000Z") }
	first, err := uc.SyncCalendar(ctx, ana.Principal, "agenda", "")
	if err != nil || len(first.Changed) != 0 {
		t.Fatalf("inicial: %+v %v", first, err)
	}
	putEvent(t, uc, ana.Principal, "agenda", "a.ics", e("a"), domain.Precondition{})
	putEvent(t, uc, ana.Principal, "agenda", "b.ics", e("b"), domain.Precondition{})
	next, err := uc.SyncCalendar(ctx, ana.Principal, "agenda", first.Token)
	if err != nil || len(next.Changed) != 2 || next.Token == first.Token {
		t.Fatalf("cambios: %+v %v", next, err)
	}
	uc.DeleteEvent(ctx, ana.Principal, "agenda", "a.ics", domain.Precondition{})
	delta, err := uc.SyncCalendar(ctx, ana.Principal, "agenda", next.Token)
	if err != nil || len(delta.Changed) != 0 || len(delta.Removed) != 1 || delta.Removed[0] != "a.ics" {
		t.Fatalf("borrado: %+v %v", delta, err)
	}
	// Un token de otro calendario, roto, o mas viejo de lo que se conserva, no se resuelve.
	uc.CreateCalendar(ctx, ana.Principal, "otro", "", "")
	other, _ := uc.SyncCalendar(ctx, ana.Principal, "otro", "")
	if _, err := uc.SyncCalendar(ctx, ana.Principal, "agenda", other.Token); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("token de otro calendario: %v", err)
	}
	if _, err := uc.SyncCalendar(ctx, ana.Principal, "agenda", "basura"); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("token roto: %v", err)
	}
	for i := 0; i < limits.MaxChangesRetained+2; i++ {
		putEvent(t, uc, ana.Principal, "agenda", "b.ics", e(fmt.Sprintf("b%d", i)), domain.Precondition{})
		// Un UID nuevo por vuelta cambia el contenido; el etag cambia con el.
	}
	if _, err := uc.SyncCalendar(ctx, ana.Principal, "agenda", first.Token); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("un token anterior a lo que se conserva no se resuelve: %v", err)
	}
}

func TestLosEventosSonDeSuBuzon(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	seedCalendar(t, uc, ana.Principal, "agenda", "a1")
	seedCalendar(t, uc, cris.Principal, "agenda", "c1")
	seedCalendar(t, uc, bea.Principal, "agenda", "b1")
	for name, p := range map[string]domain.Principal{"misma empresa": cris.Principal, "otra empresa": bea.Principal} {
		if _, err := uc.Event(ctx, p, "agenda", "a1.ics"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s lee el evento de Ana: %v", name, err)
		}
		evs, err := uc.EventsByName(ctx, p, "agenda", []string{"a1.ics"})
		if err != nil || len(evs) != 0 {
			t.Errorf("%s: multiget %v %v", name, evs, err)
		}
		if _, evs, err := uc.Events(ctx, p, "agenda"); err != nil || len(evs) != 1 || evs[0].UID == "a1" {
			t.Errorf("%s ve lo de otro: %v %v", name, evs, err)
		}
	}
	if _, err := uc.EventsByName(ctx, ana.Principal, "noexiste", []string{"a1.ics"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("calendario inexistente: %v", err)
	}
	if _, err := uc.EventsByName(ctx, ana.Principal, "noexiste", []string{"no-valido"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("calendario inexistente sin nombres validos: %v", err)
	}
}

func TestPurgeBorraTambienLosCalendariosDelBuzon(t *testing.T) {
	uc, _, _ := newUseCase(t)
	ctx := context.Background()
	seedBook(t, uc, ana.Principal, "personal", "a1")
	seedCalendar(t, uc, ana.Principal, "agenda", "a1")
	seedCalendar(t, uc, ana.Principal, "trabajo", "a2")
	seedCalendar(t, uc, cris.Principal, "agenda", "c1")
	seedCalendar(t, uc, bea.Principal, "agenda", "b1")
	recreated := domain.Principal{TenantID: ana.Principal.TenantID, MailboxID: uuid.New(), Username: ana.Principal.Username}
	seedCalendar(t, uc, recreated, "agenda", "n1")

	removed, err := uc.PurgeMailbox(ctx, ana.Principal.TenantID, ana.Principal.MailboxID)
	if err != nil || removed != (domain.PurgeResult{Addressbooks: 1, Calendars: 2}) {
		t.Fatalf("PurgeMailbox: %+v %v", removed, err)
	}
	if _, _, err := uc.Events(ctx, ana.Principal, "agenda"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("el calendario borrado no existe: %v", err)
	}
	for name, p := range map[string]domain.Principal{"otro buzon": cris.Principal, "otra empresa": bea.Principal, "buzon recreado": recreated} {
		if _, evs, err := uc.Events(ctx, p, "agenda"); err != nil || len(evs) != 1 {
			t.Errorf("%s perdio sus eventos: %v %d", name, err, len(evs))
		}
	}
	if again, err := uc.PurgeMailbox(ctx, ana.Principal.TenantID, ana.Principal.MailboxID); err != nil || again != (domain.PurgeResult{}) {
		t.Fatalf("idempotente: %+v %v", again, err)
	}
}
