//go:build integration

package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var busyPlan = domain.BusyPlan{From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 9, 1, 0, 0, 0, 0, time.UTC), MaxIntervals: 100}

// busyEvent es evt con su ocupacion materializada (plan) o, con plan vacio, pendiente.
func busyEvent(t *testing.T, res, uid, summary, start string, plan domain.BusyPlan, extra ...string) domain.Event {
	t.Helper()
	raw := evt(t, res, uid, summary, start, extra...).ICal
	ev, err := domain.NewEventAt(res, raw, calendarLimits, plan)
	if err != nil {
		t.Fatal(err)
	}
	ev.ID = uuid.New()
	return ev
}

func (e *env) cleanupScheduling(t *testing.T, ps ...domain.Principal) {
	t.Helper()
	e.cleanup(t, ps...)
	t.Cleanup(func() {
		for _, p := range ps {
			_, _ = e.owner.Exec(e.ctx, `DELETE FROM mail_dav.booking_pages WHERE mailbox_id = $1`, p.MailboxID)
			_, _ = e.owner.Exec(e.ctx, `DELETE FROM mail_dav.mailbox_addresses WHERE mailbox_id = $1`, p.MailboxID)
		}
	})
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// La disponibilidad cruza buzones de la misma empresa solo por las funciones de la migracion, que devuelven inicio y
// fin y nada mas; otra empresa no aparece, ni pidiendola por su id; y la sesion no puede preguntar por otra empresa.
func TestLaDisponibilidadSoloDevuelveIntervalosDeLaMismaEmpresa(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana-disp")
	cris := sameTenant(ana, "cris-disp")
	bea := newPrincipal("bea-disp")
	e.cleanupScheduling(t, ana, cris, bea)
	for _, p := range []domain.Principal{ana, cris, bea} {
		e.calendar(t, p, "calendar")
		if err := e.repo.RegisterAddress(e.ctx, p, p.Username); err != nil {
			t.Fatalf("registrar %s: %v", p.Username, err)
		}
	}
	if _, err := e.putEvent(t, cris, "calendar", busyEvent(t, "a.ics", "a", "Despido de Juan", "20261001T100000Z", busyPlan), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.putEvent(t, cris, "calendar", busyEvent(t, "b.ics", "b", "Semanal", "20261002T100000Z", busyPlan, "RRULE:FREQ=WEEKLY;COUNT=3"), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.putEvent(t, bea, "calendar", busyEvent(t, "c.ics", "c", "Otra empresa", "20261001T100000Z", busyPlan), domain.Precondition{}); err != nil {
		t.Fatal(err)
	}

	ids, err := e.repo.ResolveMailboxes(e.ctx, ana, []string{cris.Username, bea.Username, "nadie@it.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[cris.Username] != cris.MailboxID {
		t.Fatalf("direcciones resueltas: %v", ids)
	}
	from, to := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	busy, err := e.repo.BusyIntervals(e.ctx, ana, []uuid.UUID{cris.MailboxID, bea.MailboxID}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(busy[cris.MailboxID]) != 4 || len(busy[bea.MailboxID]) != 0 {
		t.Fatalf("ocupacion: %+v", busy)
	}

	e.asService(t, &ana, func(tx pgx.Tx) {
		// Lo que devuelve la funcion son tres columnas: nada del evento.
		rows, err := tx.Query(e.ctx, `SELECT * FROM mail_dav.busy_intervals($1, $2, $3, $4)`, ana.TenantID, []uuid.UUID{cris.MailboxID}, from, to)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range rows.FieldDescriptions() {
			names = append(names, f.Name)
		}
		rows.Close()
		if len(names) != 3 || names[0] != "mailbox_id" || names[1] != "starts_at" || names[2] != "ends_at" {
			t.Fatalf("columnas de busy_intervals: %v", names)
		}
		// La tabla de ocupacion no se ve fuera del propio buzon.
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.event_busy WHERE mailbox_id = $1`, cris.MailboxID); n != 0 {
			t.Fatalf("ana ve %d filas de ocupacion de cris sin pasar por la funcion", n)
		}
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.events WHERE mailbox_id = $1`, cris.MailboxID); n != 0 {
			t.Fatalf("ana ve %d eventos de cris", n)
		}
	})
	for name, q := range map[string]string{
		"ocupacion":  `SELECT count(*) FROM mail_dav.busy_intervals($1, ARRAY[$2::uuid], now(), now() + interval '1 day')`,
		"direccion":  `SELECT count(*) FROM mail_dav.resolve_busy_mailboxes($1, ARRAY[$2::text])`,
		"pendientes": `SELECT count(*) FROM mail_dav.busy_pending_events($1, ARRAY[$2::uuid], now(), 10)`,
		"citas":      `SELECT count(mail_dav.booking_page_owner($1, $2::text))`,
	} {
		e.asService(t, &ana, func(tx pgx.Tx) {
			var n int
			arg := any(bea.MailboxID.String())
			err := tx.QueryRow(e.ctx, q, bea.TenantID, arg).Scan(&n)
			if pgCode(err) != "42501" {
				t.Errorf("%s: preguntar por otra empresa debe fallar con 42501: %v", name, err)
			}
		})
	}
	e.asService(t, &ana, func(tx pgx.Tx) {
		_, err := tx.Exec(e.ctx, `SELECT * FROM mail_dav.busy_intervals($1, ARRAY[$2::uuid], now(), now() + interval '63 days')`, ana.TenantID, cris.MailboxID)
		if pgCode(err) != "22023" {
			t.Fatalf("una ventana de mas de 62 dias: %v", err)
		}
	})
	e.asService(t, nil, func(tx pgx.Tx) {
		_, err := tx.Exec(e.ctx, `SELECT * FROM mail_dav.busy_intervals($1, ARRAY[$2::uuid], now(), now() + interval '1 day')`, ana.TenantID, cris.MailboxID)
		if pgCode(err) != "42501" {
			t.Fatalf("sin sesion la funcion no responde: %v", err)
		}
	})
	e.asService(t, &ana, func(tx pgx.Tx) {
		_, err := tx.Exec(e.ctx, `SELECT mail_dav.register_mailbox_address($1, $2, 'x@it.test')`, ana.TenantID, cris.MailboxID)
		if pgCode(err) != "42501" {
			t.Fatalf("un buzon no registra la direccion de otro: %v", err)
		}
	})

	// Borrar el evento se lleva su ocupacion.
	if err := e.repo.DeleteEvent(e.ctx, cris, "calendar", "a.ics", domain.Precondition{}, maxChanges); err != nil {
		t.Fatal(err)
	}
	busy, _ = e.repo.BusyIntervals(e.ctx, ana, []uuid.UUID{cris.MailboxID}, from, to)
	if len(busy[cris.MailboxID]) != 3 {
		t.Fatalf("tras borrar: %+v", busy)
	}
}

func TestOcupacionPendienteSeCompletaSoloConElEtagVigente(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana-pend")
	e.cleanupScheduling(t, ana)
	e.calendar(t, ana, "calendar")
	ev := busyEvent(t, "p.ics", "p", "Pendiente", "20261001T100000Z", domain.BusyPlan{})
	if _, err := e.putEvent(t, ana, "calendar", ev, domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	pending, err := e.repo.PendingBusy(e.ctx, ana, []uuid.UUID{ana.MailboxID}, until, 10)
	if err != nil || len(pending[ana.MailboxID]) != 1 {
		t.Fatalf("pendientes: %v %v", pending, err)
	}
	id := pending[ana.MailboxID][0]
	stored, err := e.repo.EventsByID(e.ctx, ana, []uuid.UUID{id})
	if err != nil || len(stored) != 1 {
		t.Fatalf("leer por id: %v %v", stored, err)
	}
	iv := []domain.Interval{{Start: *at("20261001T100000Z"), End: *at("20261001T110000Z")}}
	if ok, err := e.repo.ReplaceBusy(e.ctx, ana, id, "0000000000000000000000000000000000000000000000000000000000000000", iv, nil); err != nil || ok {
		t.Fatalf("un etag viejo no reemplaza: %v %v", ok, err)
	}
	if ok, err := e.repo.ReplaceBusy(e.ctx, ana, id, stored[0].ETag, iv, nil); err != nil || !ok {
		t.Fatalf("reemplazar: %v %v", ok, err)
	}
	if pending, _ = e.repo.PendingBusy(e.ctx, ana, []uuid.UUID{ana.MailboxID}, until, 10); len(pending) != 0 {
		t.Fatalf("sigue pendiente: %v", pending)
	}
	busy, _ := e.repo.BusyIntervals(e.ctx, ana, []uuid.UUID{ana.MailboxID}, *at("20260928T000000Z"), *at("20261028T000000Z"))
	if len(busy[ana.MailboxID]) != 1 {
		t.Fatalf("ocupacion: %+v", busy)
	}
	// Un evento de antes de la migracion (sin busy_until explicito) queda pendiente.
	if _, err := e.owner.Exec(e.ctx, `UPDATE mail_dav.events SET busy_until = DEFAULT WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if pending, _ = e.repo.PendingBusy(e.ctx, ana, []uuid.UUID{ana.MailboxID}, until, 10); len(pending[ana.MailboxID]) != 1 {
		t.Fatalf("la marca por defecto es pendiente: %v", pending)
	}
}

func bookingPageFor(p domain.Principal, active bool) domain.BookingPage {
	s := domain.BookingSettings{Title: "Demo", DurationMinutes: 30, MaxAdvanceDays: 14, DailyLimit: 2, TimeZone: "UTC", Active: active}
	s.Weekly[time.Thursday] = []domain.DayWindow{{Start: 9 * 60, End: 12 * 60}}
	return domain.BookingPage{BookingSettings: s, ID: uuid.New(), PublicID: "PUBLICO-" + uuid.NewString()[:20], OwnerAddress: p.Username, OwnerName: "Ana"}
}

func TestCitasEnLaBase(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana-citas")
	bea := newPrincipal("bea-citas")
	e.cleanupScheduling(t, ana, bea)
	e.calendar(t, ana, "calendar")
	page, err := e.repo.SaveBookingPage(e.ctx, ana, bookingPageFor(ana, true))
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.repo.BookingPage(e.ctx, ana)
	if err != nil || len(got.Weekly[time.Thursday]) != 1 || got.Weekly[time.Thursday][0].End != 12*60 || got.PublicID != page.PublicID {
		t.Fatalf("leer la pagina: %+v %v", got, err)
	}
	if _, err := e.repo.BookingPage(e.ctx, bea); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("otra empresa no ve la pagina: %v", err)
	}
	tenantOnly := domain.Principal{TenantID: ana.TenantID}
	owner, err := e.repo.BookingPageOwner(e.ctx, tenantOnly, page.PublicID)
	if err != nil || owner != ana.MailboxID {
		t.Fatalf("dueno: %v %v", owner, err)
	}
	if owner, _ := e.repo.BookingPageOwner(e.ctx, domain.Principal{TenantID: bea.TenantID}, page.PublicID); owner != uuid.Nil {
		t.Fatal("el enlace de una empresa no se resuelve en otra")
	}
	off := page
	off.Active = false
	if _, err := e.repo.SaveBookingPage(e.ctx, ana, off); err != nil {
		t.Fatal(err)
	}
	if owner, _ := e.repo.BookingPageOwner(e.ctx, tenantOnly, page.PublicID); owner != uuid.Nil {
		t.Fatal("una pagina inactiva no tiene dueno publico")
	}
	if _, err := e.repo.SaveBookingPage(e.ctx, ana, page); err != nil {
		t.Fatal(err)
	}

	// Diez reservas del mismo hueco a la vez: una sola gana.
	start := *at("20261001T090000Z")
	lim := domain.BookingLimits{Daily: 20, PerVisitor: 20, Write: writeLimits(maxEvents, maxChanges)}
	var wg sync.WaitGroup
	results := make([]error, 10)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := busyEvent(t, uuid.NewString()+".ics", uuid.NewString(), "Demo", "20261001T090000Z", busyPlan)
			ev.TenantID, ev.MailboxID = ana.TenantID, ana.MailboxID
			results[i] = e.repo.ReserveBooking(e.ctx, ana, "calendar", ev, domain.Booking{PageID: page.ID, EventResource: ev.ResourceName,
				Start: start, End: start.Add(30 * time.Minute), VisitorHash: domain.ETagOf(uuid.NewString())}, lim)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, domain.ErrSlotUnavailable):
			t.Fatalf("error inesperado: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("ganaron %d reservas del mismo hueco", won)
	}
	// El tope diario cuenta en la base.
	lim.Daily = 1
	ev := busyEvent(t, "otra.ics", "otra", "Demo", "20261001T100000Z", busyPlan)
	ev.TenantID, ev.MailboxID = ana.TenantID, ana.MailboxID
	if err := e.repo.ReserveBooking(e.ctx, ana, "calendar", ev, domain.Booking{PageID: page.ID, EventResource: ev.ResourceName,
		Start: *at("20261001T100000Z"), End: *at("20261001T103000Z"), VisitorHash: domain.ETagOf("v")}, lim); !errors.Is(err, domain.ErrBookingLimit) {
		t.Fatalf("tope diario: %v", err)
	}
	if err := e.repo.DeleteMailboxScheduling(e.ctx, ana); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.BookingPage(e.ctx, ana); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("la baja retira la pagina: %v", err)
	}
}
