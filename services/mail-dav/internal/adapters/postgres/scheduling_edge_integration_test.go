//go:build integration

package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// La direccion registrada de un buzon es una sola: registrar otra reemplaza la anterior, y una direccion no puede
// quedar en dos buzones de la misma empresa.
func TestLaDireccionDeUnBuzonSeReemplaza(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana-dir")
	cris := sameTenant(ana, "cris-dir")
	e.cleanupScheduling(t, ana, cris)
	if err := e.repo.RegisterAddress(e.ctx, cris, "antigua-dir@it.test"); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.RegisterAddress(e.ctx, cris, cris.Username); err != nil {
		t.Fatal(err)
	}
	ids, err := e.repo.ResolveMailboxes(e.ctx, ana, []string{"antigua-dir@it.test", cris.Username})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[cris.Username] != cris.MailboxID {
		t.Fatalf("direcciones: %v", ids)
	}
	if ids, err := e.repo.ResolveMailboxes(e.ctx, ana, nil); err != nil || len(ids) != 0 {
		t.Fatalf("sin direcciones: %v %v", ids, err)
	}
	e.asService(t, &ana, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.mailbox_addresses WHERE mailbox_id = $1`, cris.MailboxID); n != 0 {
			t.Fatalf("ana ve %d direcciones de cris sin pasar por la funcion", n)
		}
	})
}

// Los eventos por id y por UID son los del propio buzon: pedir los de otro no devuelve nada.
func TestEventosPorIdentificadorSoloDelBuzon(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana-uid")
	cris := sameTenant(ana, "cris-uid")
	e.cleanupScheduling(t, ana, cris)
	e.calendar(t, ana, "calendar")
	e.calendar(t, cris, "calendar")
	ev := busyEvent(t, "u.ics", "uid-compartido", "Mio", "20261001T100000Z", busyPlan)
	if _, err := e.putEvent(t, cris, "calendar", ev, domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	got, err := e.repo.EventByUID(e.ctx, cris, "calendar", "uid-compartido")
	if err != nil || got.ResourceName != "u.ics" {
		t.Fatalf("por UID: %+v %v", got, err)
	}
	if _, err := e.repo.EventByUID(e.ctx, ana, "calendar", "uid-compartido"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("el UID de otro buzon: %v", err)
	}
	if _, err := e.repo.EventByUID(e.ctx, cris, "calendar", "no-existe"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UID inexistente: %v", err)
	}
	list, err := e.repo.EventsByID(e.ctx, ana, []uuid.UUID{got.ID})
	if err != nil || len(list) != 0 {
		t.Fatalf("ana lee por id un evento de cris: %v %v", list, err)
	}
	if list, err := e.repo.EventsByID(e.ctx, cris, nil); err != nil || len(list) != 0 {
		t.Fatalf("sin ids: %v %v", list, err)
	}
	// La ocupacion de cris no se reemplaza desde otro buzon.
	if ok, err := e.repo.ReplaceBusy(e.ctx, ana, got.ID, got.ETag, nil, nil); err != nil || ok {
		t.Fatalf("ana reemplaza la ocupacion de cris: %v %v", ok, err)
	}
}

// El tope por visitante y el margen entre citas se aplican en la misma transaccion que la reserva; las reservas y
// las paginas de un buzon no se ven desde otro.
func TestReservasConTopePorVisitanteYMargen(t *testing.T) {
	e := setup(t)
	ana := newPrincipal("ana-marg")
	cris := sameTenant(ana, "cris-marg")
	e.cleanupScheduling(t, ana, cris)
	e.calendar(t, ana, "calendar")
	page, err := e.repo.SaveBookingPage(e.ctx, ana, bookingPageFor(ana, true))
	if err != nil {
		t.Fatal(err)
	}
	lim := domain.BookingLimits{Daily: 20, PerVisitor: 1, Buffer: 15 * time.Minute, Write: writeLimits(maxEvents, maxChanges)}
	reserve := func(start, visitor string) error {
		ev := busyEvent(t, uuid.NewString()+".ics", uuid.NewString(), "Demo", start, busyPlan)
		ev.TenantID, ev.MailboxID = ana.TenantID, ana.MailboxID
		begin := *at(start)
		return e.repo.ReserveBooking(e.ctx, ana, "calendar", ev, domain.Booking{PageID: page.ID, EventResource: ev.ResourceName,
			Start: begin, End: begin.Add(time.Hour), VisitorHash: domain.ETagOf(visitor)}, lim)
	}
	if err := reserve("20261001T090000Z", "luis"); err != nil {
		t.Fatal(err)
	}
	if err := reserve("20261001T140000Z", "luis"); !errors.Is(err, domain.ErrBookingLimit) {
		t.Fatalf("tope por visitante: %v", err)
	}
	// El evento anterior acaba a las 10:00: con 15 minutos de margen, 10:10 esta ocupado y 10:15 no.
	if err := reserve("20261001T101000Z", "eva"); !errors.Is(err, domain.ErrSlotUnavailable) {
		t.Fatalf("dentro del margen: %v", err)
	}
	if err := reserve("20261001T101500Z", "eva"); err != nil {
		t.Fatalf("fuera del margen: %v", err)
	}
	e.asService(t, &cris, func(tx pgx.Tx) {
		for name, q := range map[string]string{
			"reservas": `SELECT count(*) FROM mail_dav.bookings WHERE mailbox_id = $1`,
			"paginas":  `SELECT count(*) FROM mail_dav.booking_pages WHERE mailbox_id = $1`,
		} {
			if n := count(t, tx, q, ana.MailboxID); n != 0 {
				t.Fatalf("cris ve %d %s de ana", n, name)
			}
		}
	})
	e.asService(t, &ana, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.bookings WHERE mailbox_id = $1`, ana.MailboxID); n != 2 {
			t.Fatalf("ana ve %d reservas propias", n)
		}
	})

	// Un enlace publico es unico en la empresa: otro buzon no puede tomar el de ana.
	e.calendar(t, cris, "calendar")
	stolen := bookingPageFor(cris, true)
	stolen.PublicID = page.PublicID
	if _, err := e.repo.SaveBookingPage(e.ctx, cris, stolen); err == nil {
		t.Fatal("dos paginas con el mismo enlace")
	}
	// Guardar otra vez la pagina de ana la actualiza, no crea otra.
	page.Title = "Demo nueva"
	if _, err := e.repo.SaveBookingPage(e.ctx, ana, page); err != nil {
		t.Fatal(err)
	}
	e.asService(t, &ana, func(tx pgx.Tx) {
		if n := count(t, tx, `SELECT count(*) FROM mail_dav.booking_pages WHERE mailbox_id = $1`, ana.MailboxID); n != 1 {
			t.Fatalf("ana tiene %d paginas", n)
		}
	})
	got, err := e.repo.BookingPage(e.ctx, ana)
	if err != nil || got.Title != "Demo nueva" {
		t.Fatalf("pagina actualizada: %+v %v", got, err)
	}
}
