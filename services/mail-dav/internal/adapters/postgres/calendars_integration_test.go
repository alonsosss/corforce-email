//go:build integration

package postgres

// CalDAV contra Postgres real, con la credencial que tendra el servicio en produccion (rol miembro de
// mail_dav_service, sujeto a las politicas de fila) y, para los filtros, como dueno de las tablas. Cubre el
// ciclo de vida de calendarios y eventos, el descarte por tiempo con los indices, el registro de cambios,
// los limites, la concurrencia y el aislamiento entre buzones y empresas, con una mutacion que demuestra que
// la prueba detectaria una politica ausente.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxEvents = 5

var calendarLimits = domain.CalendarLimits{MaxEventBytes: 1 << 20, MaxEventProperties: 200, MaxEventsPerMailbox: 1, MaxCalendarsPerMailbox: 1, MaxRecurrenceWork: 10000, MaxQueryWork: 100000}

func (e *env) calendar(t *testing.T, p domain.Principal, slug string) domain.Calendar {
	t.Helper()
	c, err := e.repo.CreateCalendar(e.ctx, p, domain.Calendar{ID: uuid.New(), Slug: slug, DisplayName: slug}, maxBooks)
	if err != nil {
		t.Fatalf("crear calendario %s: %v", slug, err)
	}
	return c
}

// evt arma un evento de una hora que empieza en start (con props adicionales, como una recurrencia).
func evt(t *testing.T, res, uid, summary, start string, extra ...string) domain.Event {
	t.Helper()
	begin, err := time.Parse("20060102T150405Z", start)
	if err != nil {
		t.Fatal(err)
	}
	props := append([]string{"SUMMARY:" + summary, "DTSTART:" + start, "DTEND:" + begin.Add(time.Hour).Format("20060102T150405Z")}, extra...)
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260101T000000Z\r\n" + strings.Join(props, "\r\n") + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	ev, err := domain.NewEvent(res, raw, calendarLimits)
	if err != nil {
		t.Fatal(err)
	}
	ev.ID = uuid.New()
	return ev
}

func (e *env) putEvent(t *testing.T, p domain.Principal, slug string, ev domain.Event, cond domain.Precondition) (bool, error) {
	t.Helper()
	ev.TenantID, ev.MailboxID = p.TenantID, p.MailboxID
	return e.repo.PutEvent(e.ctx, p, slug, ev, cond, maxEvents, maxChanges)
}

func at(s string) *time.Time {
	t, err := time.Parse("20060102T150405Z", s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestCicloDeVidaDeCalendariosYEventos(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)

	if cals, err := e.repo.ListCalendars(e.ctx, ana); err != nil || len(cals) != 0 {
		t.Fatalf("sin calendarios: %v %+v", err, cals)
	}
	c := e.calendar(t, ana, "calendar")
	if c.SyncSeq != 0 || c.MailboxID != ana.MailboxID || c.TenantID != ana.TenantID {
		t.Fatalf("calendario nuevo: %+v", c)
	}
	if _, err := e.repo.CreateCalendar(e.ctx, ana, domain.Calendar{ID: uuid.New(), Slug: "calendar", DisplayName: "otro"}, maxBooks); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("calendario repetido: %v", err)
	}
	// Una libreta y un calendario pueden llamarse igual: son colecciones distintas.
	e.book(t, ana, "calendar")

	ev := evt(t, "a.ics", "a", "Reunion", "20260921T100000Z")
	if created, err := e.putEvent(t, ana, "calendar", ev, domain.Precondition{IfNoneMatchAny: true}); err != nil || !created {
		t.Fatalf("alta: %v %v", created, err)
	}
	got, err := e.repo.GetEvent(e.ctx, ana, "calendar", "a.ics")
	if err != nil || got.ICal != ev.ICal || got.ETag != ev.ETag || got.UID != "a" || got.Summary != "Reunion" ||
		!got.FirstStart.Equal(*at("20260921T100000Z")) || got.LastEnd == nil || !got.LastEnd.Equal(*at("20260921T110000Z")) {
		t.Fatalf("lectura: %v %+v", err, got)
	}
	if _, err := e.putEvent(t, ana, "calendar", evt(t, "a.ics", "a", "Reunion", "20260921T100000Z"), domain.Precondition{IfNoneMatchAny: true}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("If-None-Match * sobre uno existente: %v", err)
	}
	before, _ := e.repo.GetCalendar(e.ctx, ana, "calendar")
	if created, err := e.putEvent(t, ana, "calendar", ev, domain.Precondition{}); err != nil || created {
		t.Fatalf("mismo contenido: %v %v", created, err)
	}
	if same, _ := e.repo.GetCalendar(e.ctx, ana, "calendar"); same.SyncSeq != before.SyncSeq {
		t.Fatalf("el ctag avanzo sin cambio: %d -> %d", before.SyncSeq, same.SyncSeq)
	}
	edited := evt(t, "a.ics", "a", "Reunion movida", "20260922T100000Z")
	if created, err := e.putEvent(t, ana, "calendar", edited, domain.Precondition{IfMatch: []string{ev.ETag}}); err != nil || created {
		t.Fatalf("edicion con If-Match: %v %v", created, err)
	}
	if moved, _ := e.repo.GetEvent(e.ctx, ana, "calendar", "a.ics"); !moved.FirstStart.Equal(*at("20260922T100000Z")) || moved.Summary != "Reunion movida" {
		t.Fatalf("la edicion reindexa: %+v", moved)
	}
	if _, err := e.putEvent(t, ana, "calendar", evt(t, "a.ics", "a", "Tarde", "20260923T100000Z"), domain.Precondition{IfMatch: []string{ev.ETag}}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("If-Match viejo: %v", err)
	}
	var conflict *domain.UIDConflictError
	if _, err := e.putEvent(t, ana, "calendar", evt(t, "b.ics", "a", "Mismo UID", "20260924T100000Z"), domain.Precondition{}); !errors.As(err, &conflict) || conflict.Resource != "a.ics" {
		t.Fatalf("conflicto de UID: %v", err)
	}
	if _, err := e.putEvent(t, ana, "noexiste", evt(t, "z.ics", "z", "Z", "20260924T100000Z"), domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("calendario inexistente: %v", err)
	}
	// El UID solo es unico dentro de un calendario.
	e.calendar(t, ana, "otro")
	if _, err := e.putEvent(t, ana, "otro", evt(t, "a.ics", "a", "Mismo UID en otro calendario", "20260924T100000Z"), domain.Precondition{}); err != nil {
		t.Fatalf("otro calendario: %v", err)
	}

	cal, events, err := e.repo.ListEvents(e.ctx, ana, "calendar", domain.EventWindow{})
	if err != nil || len(events) != 1 || cal.SyncSeq != 2 {
		t.Fatalf("listado: %v %+v %+v", err, cal, events)
	}
	if many, err := e.repo.GetEvents(e.ctx, ana, "calendar", []string{"a.ics", "nada.ics"}); err != nil || len(many) != 1 {
		t.Fatalf("multiget: %v %+v", err, many)
	}

	if err := e.repo.DeleteEvent(e.ctx, ana, "calendar", "a.ics", domain.Precondition{IfMatch: []string{ev.ETag}}, maxChanges); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("borrar con etag viejo: %v", err)
	}
	if err := e.repo.DeleteEvent(e.ctx, ana, "calendar", "a.ics", domain.Precondition{IfMatch: []string{edited.ETag}}, maxChanges); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DeleteEvent(e.ctx, ana, "calendar", "a.ics", domain.Precondition{}, maxChanges); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar dos veces: %v", err)
	}
	if _, err := e.repo.GetEvent(e.ctx, ana, "calendar", "a.ics"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrado: %v", err)
	}

	if _, err := e.putEvent(t, ana, "otro", evt(t, "f.ics", "f", "F", "20260925T100000Z"), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DeleteCalendar(e.ctx, ana, "otro"); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := e.owner.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM mail_dav.events WHERE mailbox_id = $1 AND uid = 'f') + (SELECT count(*) FROM mail_dav.calendar_changes WHERE mailbox_id = $1 AND resource_name = 'f.ics')`, ana.MailboxID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("borrar el calendario borra sus eventos y su registro de cambios: %d %v", left, err)
	}
	if err := e.repo.DeleteCalendar(e.ctx, ana, "otro"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar dos veces: %v", err)
	}
	// Borrar el calendario no toca la libreta del mismo nombre.
	if _, err := e.repo.GetAddressbook(e.ctx, ana, "calendar"); err != nil {
		t.Fatalf("la libreta sigue: %v", err)
	}
}

func TestElDescartePorTiempoUsaLosIndices(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)
	e.calendar(t, ana, "calendar")
	for _, ev := range []domain.Event{
		evt(t, "pasado.ics", "pasado", "Pasado", "20250101T100000Z"),
		evt(t, "hoy.ics", "hoy", "Hoy", "20260921T100000Z"),
		evt(t, "futuro.ics", "futuro", "Futuro", "20270101T100000Z"),
		evt(t, "acaba.ics", "acaba", "Serie con fin", "20260105T100000Z", "RRULE:FREQ=WEEKLY;UNTIL=20260301T000000Z"),
		evt(t, "sinfin.ics", "sinfin", "Serie sin fin", "20260105T100000Z", "RRULE:FREQ=WEEKLY"),
	} {
		if _, err := e.repo.PutEvent(e.ctx, ana, "calendar", withOwner(ev, ana), domain.Precondition{}, 10, maxChanges); err != nil {
			t.Fatal(err)
		}
	}
	names := func(w domain.EventWindow) string {
		_, evs, err := e.repo.ListEvents(e.ctx, ana, "calendar", w)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(evs))
		for i, ev := range evs {
			out[i] = strings.TrimSuffix(ev.ResourceName, ".ics")
		}
		return strings.Join(out, ",")
	}
	for name, tc := range map[string]struct {
		w    domain.EventWindow
		want string
	}{
		"sin ventana":                {domain.EventWindow{}, "acaba,futuro,hoy,pasado,sinfin"},
		"septiembre de 2026":         {domain.EventWindow{Start: at("20260901T000000Z"), End: at("20261001T000000Z")}, "hoy,sinfin"},
		"enero de 2026":              {domain.EventWindow{Start: at("20260101T000000Z"), End: at("20260201T000000Z")}, "acaba,sinfin"},
		"2025":                       {domain.EventWindow{Start: at("20250101T000000Z"), End: at("20260101T000000Z")}, "pasado"},
		"solo inicio, en 2027":       {domain.EventWindow{Start: at("20261231T000000Z")}, "futuro,sinfin"},
		"solo fin, antes de todo":    {domain.EventWindow{End: at("20241231T000000Z")}, ""},
		"el fin es exclusivo":        {domain.EventWindow{Start: at("20260921T000000Z"), End: at("20260921T100000Z")}, "sinfin"},
		"el fin de un evento cuenta": {domain.EventWindow{Start: at("20260921T110000Z"), End: at("20260921T120000Z")}, "sinfin,hoy"},
		"despues del UNTIL, sin fin": {domain.EventWindow{Start: at("20260601T000000Z"), End: at("20260701T000000Z")}, "sinfin"},
	} {
		got := names(tc.w)
		if strings.Join(sorted(strings.Split(got, ",")), ",") != strings.Join(sorted(strings.Split(tc.want, ",")), ",") {
			t.Errorf("%s: %q, quiero %q", name, got, tc.want)
		}
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func withOwner(ev domain.Event, p domain.Principal) domain.Event {
	ev.TenantID, ev.MailboxID = p.TenantID, p.MailboxID
	return ev
}

func TestSincronizacionDeEventosPorCambios(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)
	c := e.calendar(t, ana, "calendar")
	for i := 1; i <= 2; i++ {
		e.putEvent(t, ana, "calendar", evt(t, fmt.Sprintf("c%d.ics", i), fmt.Sprintf("c%d", i), "C", "20260921T100000Z"), domain.Precondition{})
	}
	afterTwo, changed, removed, err := e.repo.EventChangesSince(e.ctx, ana, "calendar", 0)
	if err != nil || afterTwo.SyncSeq != 2 || len(changed) != 2 || len(removed) != 0 {
		t.Fatalf("desde el principio: %v %+v %d %v", err, afterTwo, len(changed), removed)
	}
	e.putEvent(t, ana, "calendar", evt(t, "c1.ics", "c1", "C1 editado", "20260921T100000Z"), domain.Precondition{})
	if err := e.repo.DeleteEvent(e.ctx, ana, "calendar", "c2.ics", domain.Precondition{}, maxChanges); err != nil {
		t.Fatal(err)
	}
	cal, changed, removed, err := e.repo.EventChangesSince(e.ctx, ana, "calendar", afterTwo.SyncSeq)
	if err != nil || cal.SyncSeq != 4 || len(changed) != 1 || changed[0].ResourceName != "c1.ics" || len(removed) != 1 || removed[0] != "c2.ics" {
		t.Fatalf("diferencia: %v %+v %+v %v", err, cal, changed, removed)
	}
	e.putEvent(t, ana, "calendar", evt(t, "c3.ics", "c3", "C3", "20260921T100000Z"), domain.Precondition{})
	_ = e.repo.DeleteEvent(e.ctx, ana, "calendar", "c3.ics", domain.Precondition{}, maxChanges)
	_, changed, removed, err = e.repo.EventChangesSince(e.ctx, ana, "calendar", cal.SyncSeq)
	if err != nil || len(changed) != 0 || len(removed) != 1 || removed[0] != "c3.ics" {
		t.Fatalf("creado y borrado: %v %+v %v", err, changed, removed)
	}
	current, _ := e.repo.GetCalendar(e.ctx, ana, "calendar")
	if current.ChangesFloor != current.SyncSeq-maxChanges {
		t.Fatalf("suelo de cambios %d con secuencia %d", current.ChangesFloor, current.SyncSeq)
	}
	if _, _, _, err := e.repo.EventChangesSince(e.ctx, ana, "calendar", current.ChangesFloor-1); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("token podado: %v", err)
	}
	if _, _, _, err := e.repo.EventChangesSince(e.ctx, ana, "calendar", current.SyncSeq+1); !errors.Is(err, domain.ErrInvalidSyncToken) {
		t.Fatalf("token del futuro: %v", err)
	}
	var kept int
	if err := e.owner.QueryRow(e.ctx, `SELECT count(*) FROM mail_dav.calendar_changes WHERE calendar_id = $1`, c.ID).Scan(&kept); err != nil || kept != maxChanges {
		t.Fatalf("cambios conservados: %d %v", kept, err)
	}
	// Los cambios de un calendario no aparecen en el registro de las libretas.
	var crossed int
	if err := e.owner.QueryRow(e.ctx, `SELECT count(*) FROM mail_dav.collection_changes WHERE mailbox_id = $1`, ana.MailboxID).Scan(&crossed); err != nil || crossed != 0 {
		t.Fatalf("el registro de las libretas no recibe cambios de calendarios: %d %v", crossed, err)
	}
}

func TestLimitesYConcurrenciaDeEventos(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	e.cleanup(t, ana)
	e.calendar(t, ana, "uno")
	e.calendar(t, ana, "dos")
	e.calendar(t, ana, "tres")
	if _, err := e.repo.CreateCalendar(e.ctx, ana, domain.Calendar{ID: uuid.New(), Slug: "cuatro", DisplayName: "x"}, maxBooks); !errors.Is(err, domain.ErrCalendarLimit) {
		t.Fatalf("limite de calendarios: %v", err)
	}
	// Los limites de libretas y de calendarios son independientes.
	if _, err := e.repo.CreateAddressbook(e.ctx, ana, domain.Addressbook{ID: uuid.New(), Slug: "libreta", DisplayName: "x"}, maxBooks); err != nil {
		t.Fatalf("una libreta no cuenta como calendario: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slug := []string{"uno", "dos"}[i%2]
			ev := withOwner(evt(t, fmt.Sprintf("p%d.ics", i), fmt.Sprintf("p%d", i), "P", "20260921T100000Z"), ana)
			_, err := e.repo.PutEvent(e.ctx, ana, slug, ev, domain.Precondition{}, maxEvents, 1000)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	var ok, full int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrEventLimit):
			full++
		default:
			t.Fatalf("error inesperado: %v", err)
		}
	}
	if ok != maxEvents || full != 20-maxEvents {
		t.Fatalf("altas concurrentes: %d admitidas y %d rechazadas, el limite es %d", ok, full, maxEvents)
	}
	var seqs []int64
	rows, err := e.owner.Query(e.ctx, `SELECT seq FROM mail_dav.calendar_changes ch JOIN mail_dav.calendars c ON c.id = ch.calendar_id WHERE c.mailbox_id = $1 AND c.slug = 'uno' ORDER BY seq`, ana.MailboxID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s int64
		_ = rows.Scan(&s)
		seqs = append(seqs, s)
	}
	rows.Close()
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("secuencias con huecos o repetidas: %v", seqs)
		}
	}
}

func TestAislamientoDeEventosEntreBuzonesYEmpresas(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	cris := sameTenant(ana, "cris")
	bea := newPrincipal("bea")
	e.cleanup(t, ana, cris, bea)
	for _, p := range []domain.Principal{ana, cris, bea} {
		e.calendar(t, p, "calendar")
	}
	e.putEvent(t, ana, "calendar", evt(t, "secreto.ics", "secreto", "Secreto de Ana", "20260921T100000Z"), domain.Precondition{})

	for name, other := range map[string]domain.Principal{"misma empresa": cris, "otra empresa": bea} {
		if _, err := e.repo.GetEvent(e.ctx, other, "calendar", "secreto.ics"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s lee: %v", name, err)
		}
		if _, evs, err := e.repo.ListEvents(e.ctx, other, "calendar", domain.EventWindow{}); err != nil || len(evs) != 0 {
			t.Errorf("%s lista: %v %+v", name, err, evs)
		}
		if _, evs, err := e.repo.ListEvents(e.ctx, other, "calendar", domain.EventWindow{Start: at("20260101T000000Z"), End: at("20271231T000000Z")}); err != nil || len(evs) != 0 {
			t.Errorf("%s lista por rango: %v %+v", name, err, evs)
		}
		if got, err := e.repo.GetEvents(e.ctx, other, "calendar", []string{"secreto.ics"}); err != nil || len(got) != 0 {
			t.Errorf("%s pide por nombre: %v %+v", name, err, got)
		}
		if err := e.repo.DeleteEvent(e.ctx, other, "calendar", "secreto.ics", domain.Precondition{}, maxChanges); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s borra: %v", name, err)
		}
		if _, _, _, err := e.repo.EventChangesSince(e.ctx, other, "calendar", 0); err != nil {
			t.Errorf("%s pide cambios de su propio calendario: %v", name, err)
		}
		if cals, err := e.repo.ListCalendars(e.ctx, other); err != nil || len(cals) != 1 || cals[0].MailboxID != other.MailboxID {
			t.Errorf("%s ve calendarios ajenos: %v %+v", name, err, cals)
		}
	}
	if err := e.repo.DeleteCalendar(e.ctx, cris, "calendar"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.GetEvent(e.ctx, ana, "calendar", "secreto.ics"); err != nil {
		t.Fatalf("borrar el calendario de otro buzon con el mismo nombre toco el de Ana: %v", err)
	}

	e.calendar(t, cris, "calendar")
	e.putEvent(t, cris, "calendar", evt(t, "mio.ics", "mio", "De Cris", "20260921T100000Z"), domain.Precondition{})
	e.asService(t, &cris, func(tx pgx.Tx) {
		for table, own := range map[string]int{"events": 1, "calendars": 1, "calendar_changes": 1} {
			if n := count(t, tx, `SELECT count(*) FROM mail_dav.`+table); n != own {
				t.Errorf("cris ve %d filas de %s sin filtro, tiene %d", n, table, own)
			}
			if n := count(t, tx, `SELECT count(*) FROM mail_dav.`+table+` WHERE mailbox_id = $1`, ana.MailboxID); n != 0 {
				t.Errorf("cris ve %d filas de Ana en %s", n, table)
			}
		}
	})
	e.asService(t, &bea, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.events WHERE tenant_id = $1`, ana.TenantID); n != 0 {
			t.Errorf("otra empresa ve %d eventos de Ana", n)
		}
	})
	e.asService(t, &ana, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.events`); n != 1 {
			t.Errorf("Ana ve %d eventos, tiene 1", n)
		}
	})
	e.asService(t, nil, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.events`); n != 0 {
			t.Errorf("sin sesion se ven %d eventos: la politica no es fail-closed", n)
		}
	})
	e.asService(t, &cris, func(tx pgx.Tx) {
		_, err := tx.Exec(e.ctx, `INSERT INTO mail_dav.calendars (id, tenant_id, mailbox_id, slug, display_name) VALUES ($1, $2, $3, 'robado', 'x')`, uuid.New(), ana.TenantID, ana.MailboxID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("insertar como Ana siendo Cris: %v", err)
		}
	})
	e.asService(t, &cris, func(tx pgx.Tx) {
		tag, err := tx.Exec(e.ctx, `UPDATE mail_dav.events SET summary = 'hackeado' WHERE mailbox_id = $1`, ana.MailboxID)
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("Cris modifico eventos de Ana: %v %d", err, tag.RowsAffected())
		}
		tag, err = tx.Exec(e.ctx, `DELETE FROM mail_dav.events WHERE mailbox_id = $1`, ana.MailboxID)
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("Cris borro eventos de Ana: %v %d", err, tag.RowsAffected())
		}
	})

	// Mutacion: sin la politica, la misma consulta veria los eventos de Ana. La transaccion se revierte.
	tx, err := e.owner.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err := tx.Exec(e.ctx, `ALTER TABLE mail_dav.events DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `SET LOCAL ROLE mail_dav_service`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `SELECT set_config('app.current_tenant_id', $1, true), set_config('app.current_user_id', $2, true)`, bea.TenantID.String(), bea.MailboxID.String()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, tx, `SELECT count(*) FROM mail_dav.events WHERE mailbox_id = $1`, ana.MailboxID); n != 1 {
		t.Fatalf("con la politica desactivada Bea deberia ver el evento de Ana (%d): la prueba no detectaria una politica ausente", n)
	}
}

// Como dueno de las tablas (exento de las politicas) el aislamiento lo dan solo los filtros de cada consulta.
func TestLosFiltrosDeLasConsultasAislanLosEventosSinLaPolitica(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	cris := sameTenant(ana, "cris")
	bea := newPrincipal("bea")
	e.cleanup(t, ana, cris, bea)
	for _, p := range []domain.Principal{ana, cris, bea} {
		e.calendar(t, p, "calendar")
	}
	e.putEvent(t, ana, "calendar", evt(t, "secreto.ics", "secreto", "Secreto de Ana", "20260921T100000Z"), domain.Precondition{})

	asOwner := db.WithPool(context.Background(), e.owner)
	for name, other := range map[string]domain.Principal{"misma empresa": cris, "otra empresa": bea} {
		if _, err := e.repo.GetEvent(asOwner, other, "calendar", "secreto.ics"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s lee: %v", name, err)
		}
		if _, evs, err := e.repo.ListEvents(asOwner, other, "calendar", domain.EventWindow{}); err != nil || len(evs) != 0 {
			t.Errorf("%s lista: %v %+v", name, err, evs)
		}
		if _, evs, err := e.repo.ListEvents(asOwner, other, "calendar", domain.EventWindow{Start: at("20260101T000000Z"), End: at("20271231T000000Z")}); err != nil || len(evs) != 0 {
			t.Errorf("%s lista por rango: %v %+v", name, err, evs)
		}
		if got, err := e.repo.GetEvents(asOwner, other, "calendar", []string{"secreto.ics"}); err != nil || len(got) != 0 {
			t.Errorf("%s pide por nombre: %v %+v", name, err, got)
		}
		if err := e.repo.DeleteEvent(asOwner, other, "calendar", "secreto.ics", domain.Precondition{}, maxChanges); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s borra: %v", name, err)
		}
		own := withOwner(evt(t, "secreto.ics", "otro", "De otro", "20260921T100000Z"), other)
		if _, err := e.repo.PutEvent(asOwner, other, "calendar", own, domain.Precondition{}, maxEvents, maxChanges); err != nil {
			t.Errorf("%s escribe en SU calendario, con el mismo nombre de recurso: %v", name, err)
		}
		if cals, err := e.repo.ListCalendars(asOwner, other); err != nil || len(cals) != 1 || cals[0].MailboxID != other.MailboxID {
			t.Errorf("%s ve calendarios ajenos: %v %+v", name, err, cals)
		}
	}
	got, err := e.repo.GetEvent(asOwner, ana, "calendar", "secreto.ics")
	if err != nil || got.Summary != "Secreto de Ana" {
		t.Fatalf("lo de Ana no se toco: %v %+v", err, got)
	}
	if err := e.repo.DeleteCalendar(asOwner, cris, "calendar"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.GetEvent(asOwner, ana, "calendar", "secreto.ics"); err != nil {
		t.Fatalf("borrar el calendario de otro buzon con el mismo nombre toco el de Ana: %v", err)
	}
}

func TestLaBaseRechazaEventosQueElServicioNoDeberiaEscribir(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana")
	cris := sameTenant(ana, "cris")
	e.cleanup(t, ana, cris)
	calAna := e.calendar(t, ana, "calendar")
	e.calendar(t, cris, "calendar")

	insert := func(q string, args ...any) error {
		_, err := e.owner.Exec(e.ctx, q, args...)
		return err
	}
	const cols = `INSERT INTO mail_dav.events (id, tenant_id, mailbox_id, calendar_id, resource_name, uid, ical, etag, first_start, last_end) VALUES ($1, $2, $3, $4, $5, $6, 'x', $7, $8, $9)`
	etag, start, end := strings.Repeat("a", 64), time.Now().UTC(), time.Now().UTC().Add(time.Hour)
	for name, err := range map[string]error{
		"evento en el calendario de otro buzon":   insert(cols, uuid.New(), ana.TenantID, cris.MailboxID, calAna.ID, "a.ics", "u1", etag, start, end),
		"evento en el calendario de otra empresa": insert(cols, uuid.New(), uuid.New(), ana.MailboxID, calAna.ID, "a.ics", "u2", etag, start, end),
		"nombre de recurso con ruta":              insert(cols, uuid.New(), ana.TenantID, ana.MailboxID, calAna.ID, "../a.ics", "u3", etag, start, end),
		"recurso sin .ics":                        insert(cols, uuid.New(), ana.TenantID, ana.MailboxID, calAna.ID, "a.vcf", "u4", etag, start, end),
		"etag que no es sha256":                   insert(cols, uuid.New(), ana.TenantID, ana.MailboxID, calAna.ID, "b.ics", "u5", "no-es-hex", start, end),
		"fin anterior al inicio":                  insert(cols, uuid.New(), ana.TenantID, ana.MailboxID, calAna.ID, "c.ics", "u6", etag, end, start),
		"calendario con nombre invalido":          insert(`INSERT INTO mail_dav.calendars (id, tenant_id, mailbox_id, slug, display_name) VALUES ($1, $2, $3, 'Mayus/x', 'x')`, uuid.New(), ana.TenantID, ana.MailboxID),
		"evento de mas de 4 MiB":                  insert(`INSERT INTO mail_dav.events (id, tenant_id, mailbox_id, calendar_id, resource_name, uid, ical, etag, first_start) VALUES ($1, $2, $3, $4, 'g.ics', 'g', repeat('a', 4194305), $5, now())`, uuid.New(), ana.TenantID, ana.MailboxID, calAna.ID, etag),
		"suelo de cambios sobre la secuencia":     insert(`UPDATE mail_dav.calendars SET changes_floor = sync_seq + 1 WHERE id = $1`, calAna.ID),
	} {
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || (pgErr.Code != "23514" && pgErr.Code != "23503") {
			t.Errorf("%s: debia rechazarlo una restriccion, fue %v", name, err)
		}
	}
	for name, q := range map[string]string{
		"crear una tabla":    `CREATE TABLE mail_dav.intrusa2 (id int)`,
		"borrar una tabla":   `DROP TABLE mail_dav.events`,
		"truncar":            `TRUNCATE mail_dav.events`,
		"quitar la politica": `DROP POLICY mailbox_isolation ON mail_dav.events`,
		"apagar RLS":         `ALTER TABLE mail_dav.events DISABLE ROW LEVEL SECURITY`,
	} {
		_, err := e.service.Exec(e.ctx, q)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "42501" && pgErr.Code != "42P01") {
			t.Errorf("%s: el rol de servicio no debia poder, fue %v", name, err)
		}
	}
}

// storedCalendarRows cuenta lo que la base guarda de un buzon en CalDAV, leido como dueno de las tablas.
func (e *env) storedCalendarRows(t *testing.T, p domain.Principal) int {
	t.Helper()
	var n int
	err := e.owner.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM mail_dav.calendars WHERE mailbox_id = $1)
		      + (SELECT count(*) FROM mail_dav.events WHERE mailbox_id = $1)
		      + (SELECT count(*) FROM mail_dav.calendar_changes WHERE mailbox_id = $1)`, p.MailboxID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBorrarLosCalendariosDeUnBuzonSoloTocaLosSuyos(t *testing.T) {
	e := setup(t)
	for name, ctx := range map[string]context.Context{
		"rol de servicio con politicas de fila": e.ctx,
		"dueno de las tablas, solo los filtros": db.WithPool(context.Background(), e.owner),
	} {
		t.Run(name, func(t *testing.T) {
			ana := newPrincipal("ana")
			cris := sameTenant(ana, "cris")
			bea := newPrincipal("bea")
			recreated := domain.Principal{TenantID: ana.TenantID, MailboxID: uuid.New(), Username: ana.Username}
			all := []domain.Principal{ana, cris, bea, recreated}
			e.cleanup(t, all...)
			for i, p := range all {
				uid := string(rune('a' + i))
				e.calendar(t, p, "calendar")
				e.calendar(t, p, "trabajo")
				e.putEvent(t, p, "calendar", evt(t, uid+".ics", uid, "Evento "+uid, "20260921T100000Z"), domain.Precondition{})
				e.putEvent(t, p, "trabajo", evt(t, uid+"-t.ics", uid+"-t", "Trabajo "+uid, "20260921T100000Z"), domain.Precondition{})
			}
			kept := map[uuid.UUID]int{}
			for _, p := range all[1:] {
				kept[p.MailboxID] = e.storedCalendarRows(t, p)
			}
			if n, err := e.repo.DeleteMailboxCalendars(ctx, domain.Principal{TenantID: bea.TenantID, MailboxID: cris.MailboxID}); err != nil || n != 0 {
				t.Fatalf("empresa distinta: %d %v", n, err)
			}
			if got := e.storedCalendarRows(t, cris); got != kept[cris.MailboxID] {
				t.Fatalf("Cris perdio datos por un borrado de otra empresa: %d", got)
			}
			if n, err := e.repo.DeleteMailboxCalendars(ctx, ana); err != nil || n != 2 {
				t.Fatalf("DeleteMailboxCalendars: %d %v", n, err)
			}
			if got := e.storedCalendarRows(t, ana); got != 0 {
				t.Fatalf("quedan %d filas de Ana entre calendarios, eventos y cambios", got)
			}
			for name, p := range map[string]domain.Principal{"otro buzon de la empresa": cris, "otra empresa": bea, "buzon recreado con el mismo nombre": recreated} {
				if got := e.storedCalendarRows(t, p); got != kept[p.MailboxID] {
					t.Errorf("%s: tenia %d filas y ahora %d", name, kept[p.MailboxID], got)
				}
			}
			if n, err := e.repo.DeleteMailboxCalendars(ctx, ana); err != nil || n != 0 {
				t.Fatalf("repetido: %d %v", n, err)
			}
		})
	}
}
