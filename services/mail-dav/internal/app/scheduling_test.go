package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

var schedNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) // lunes

var schedulingConfig = app.SchedulingConfig{
	BusyHorizon: 400 * 24 * time.Hour, BusyLookback: 7 * 24 * time.Hour, MaxBusyPerEvent: 500, BusyRefreshMax: 100,
	MaxAvailabilityAddresses: 10, BookingMaxDaily: 50, BookingMaxPerVisitor: 2, BookingMaxSlots: 100, BookingMaxWindow: 31 * 24 * time.Hour,
}

type schedEnv struct {
	uc    *app.UseCase
	plain *app.UseCase
	store *apptest.Store
	sched *apptest.Scheduling
}

// newSchedulingEnv arma un caso de uso con planificacion y otro sin ella sobre el mismo almacen: el segundo guarda
// eventos como los guardaba el servicio antes de la migracion (sin ocupacion materializada).
func newSchedulingEnv(t *testing.T) schedEnv {
	t.Helper()
	store := apptest.NewStore()
	store.Now = func() time.Time { return schedNow }
	sched := apptest.NewScheduling(store)
	cfg := testConfig
	cfg.Calendar.MaxEventsPerMailbox = 50
	cfg.Scheduling = schedulingConfig
	now := func() time.Time { return schedNow }
	uc, err := app.New(app.Deps{Auth: apptest.NewAuth(ana, bea, cris), Tenant: apptest.Binder{}, Store: store, Calendars: store, Scheduling: sched, Config: cfg, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	plainCfg := cfg
	plainCfg.Scheduling = app.SchedulingConfig{}
	plain, err := app.New(app.Deps{Auth: apptest.NewAuth(ana, bea, cris), Tenant: apptest.Binder{}, Store: store, Calendars: store, Config: plainCfg, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return schedEnv{uc: uc, plain: plain, store: store, sched: sched}
}

func meeting(title string, start time.Time, minutes int) domain.EventFields {
	return domain.EventFields{Title: title, Start: start, End: start.Add(time.Duration(minutes) * time.Minute)}
}

func TestDisponibilidadDelEquipo(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	at := schedNow.Add(48 * time.Hour)
	if _, err := env.uc.CreateEvent(ctx, cris.Principal, meeting("Negociacion secreta", at, 60)); err != nil {
		t.Fatal(err)
	}
	// Un evento guardado sin planificacion (como los anteriores a la migracion) se completa al consultar.
	if _, err := env.plain.CreateEvent(ctx, cris.Principal, meeting("Antiguo", at.Add(3*time.Hour), 30)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.uc.PutEvent(ctx, cris.Principal, app.DefaultCalendarSlug, "libre.ics",
		event("libre", "Libre", at.Add(5*time.Hour).Format("20060102T150405Z"), at.Add(6*time.Hour).Format("20060102T150405Z"), "TRANSP:TRANSPARENT"),
		domain.Precondition{}); err != nil {
		t.Fatal(err)
	}
	// bea es de otra empresa: aunque tenga eventos, para ana no existe.
	if _, err := env.uc.CreateEvent(ctx, bea.Principal, meeting("Otra empresa", at, 60)); err != nil {
		t.Fatal(err)
	}
	list, err := env.uc.Availability(ctx, ana.Principal, []string{"Cris@acme.test", "bea@beta.test", "nadie@acme.test"}, schedNow, schedNow.Add(7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || !list[0].Known || list[1].Known || list[2].Known {
		t.Fatalf("disponibilidad: %+v", list)
	}
	busy := list[0].Busy
	if len(busy) != 2 || !busy[0].Start.Equal(at) || !busy[1].Start.Equal(at.Add(3*time.Hour)) {
		t.Fatalf("ocupacion de cris: %+v", busy)
	}
	var fe *domain.FieldError
	for name, call := range map[string]func() error{
		"sin direcciones": func() error { _, err := env.uc.Availability(ctx, ana.Principal, nil, schedNow, schedNow.Add(time.Hour)); return err },
		"ventana larga": func() error {
			_, err := env.uc.Availability(ctx, ana.Principal, []string{"cris@acme.test"}, schedNow, schedNow.Add(63*24*time.Hour))
			return err
		},
		"muy atras": func() error {
			_, err := env.uc.Availability(ctx, ana.Principal, []string{"cris@acme.test"}, schedNow.Add(-30*24*time.Hour), schedNow)
			return err
		},
		"direccion invalida": func() error {
			_, err := env.uc.Availability(ctx, ana.Principal, []string{"no es"}, schedNow, schedNow.Add(time.Hour))
			return err
		},
	} {
		if err := call(); !errors.As(err, &fe) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := env.plain.Availability(ctx, ana.Principal, []string{"cris@acme.test"}, schedNow, schedNow.Add(time.Hour)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("sin planificacion: %v", err)
	}
}

// Ida y vuelta entre dos buzones: ana invita a cris, cris acepta, la respuesta vuelve a ana y la cancelacion de ana
// quita el evento del calendario de cris.
func TestInvitacionesEntreBuzones(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	f := meeting("Revision", schedNow.Add(72*time.Hour), 60)
	f.Organizer = &domain.Party{Email: "ana@acme.test", Name: "Ana"}
	f.Attendees = []domain.Attendee{{Email: "cris@acme.test", Name: "Cris"}}
	ev, err := env.uc.CreateEvent(ctx, ana.Principal, f)
	if err != nil {
		t.Fatal(err)
	}
	req, _, err := env.uc.EventInvitation(ctx, ana.Principal, ev.ID, domain.MethodRequest)
	if err != nil || len(req.Recipients) != 1 {
		t.Fatalf("REQUEST: %+v %v", req, err)
	}

	crisAddrs := []string{"cris@acme.test"}
	state, err := env.uc.InspectInvitation(ctx, cris.Principal, req.ICal, crisAddrs)
	if err != nil || state.Attendee != "cris@acme.test" || state.EventID != "" || state.Invitation.Summary != "Revision" {
		t.Fatalf("inspeccion: %+v %v", state, err)
	}
	ans, err := env.uc.RespondInvitation(ctx, cris.Principal, req.ICal, crisAddrs, "ACCEPTED")
	if err != nil || ans.EventID == "" || ans.Organizer.Email != "ana@acme.test" {
		t.Fatalf("aceptar: %+v %v", ans, err)
	}
	state, _ = env.uc.InspectInvitation(ctx, cris.Principal, req.ICal, crisAddrs)
	if state.EventID != ans.EventID || state.PartStat != domain.PartStatAccepted {
		t.Fatalf("despues de aceptar: %+v", state)
	}
	// Responder otra vez reemplaza la copia, no crea otra.
	again, err := env.uc.RespondInvitation(ctx, cris.Principal, req.ICal, crisAddrs, "TENTATIVE")
	if err != nil || again.EventID != ans.EventID {
		t.Fatalf("tentativo: %+v %v", again, err)
	}

	// La respuesta solo cuenta si la envia el propio invitado.
	if _, err := env.uc.ApplyInvitation(ctx, ana.Principal, again.Reply, "mallory@acme.test", []string{"ana@acme.test"}); err == nil {
		t.Fatal("una respuesta falsificada se aplico")
	}
	// Un buzon que no organiza el evento no aplica respuestas.
	if _, err := env.uc.ApplyInvitation(ctx, cris.Principal, again.Reply, "cris@acme.test", crisAddrs); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("aplicar en el invitado: %v", err)
	}
	applied, err := env.uc.ApplyInvitation(ctx, ana.Principal, again.Reply, "cris@acme.test", []string{"ana@acme.test"})
	if err != nil || !applied.Changed {
		t.Fatalf("aplicar: %+v %v", applied, err)
	}
	got, _ := env.uc.EventByID(ctx, ana.Principal, ev.ID)
	if len(got.Fields.Attendees) != 1 || got.Fields.Attendees[0].PartStat != domain.PartStatTentative {
		t.Fatalf("ana no ve la respuesta: %+v", got.Fields.Attendees)
	}

	cancel, _, err := env.uc.EventInvitation(ctx, ana.Principal, ev.ID, domain.MethodCancel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.uc.ApplyInvitation(ctx, cris.Principal, cancel.ICal, "otro@acme.test", crisAddrs); err == nil {
		t.Fatal("una cancelacion de otro remitente se aplico")
	}
	applied, err = env.uc.ApplyInvitation(ctx, cris.Principal, cancel.ICal, "ana@acme.test", crisAddrs)
	if err != nil || !applied.Changed {
		t.Fatalf("cancelar: %+v %v", applied, err)
	}
	if _, err := env.uc.EventByID(ctx, cris.Principal, ans.EventID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("la cancelacion no quito el evento: %v", err)
	}

	// Rechazar una invitacion nueva no guarda nada.
	ans, err = env.uc.RespondInvitation(ctx, cris.Principal, req.ICal, crisAddrs, "DECLINED")
	if err != nil || ans.EventID != "" || !strings.Contains(ans.Reply, "PARTSTAT=DECLINED") {
		t.Fatalf("rechazar: %+v %v", ans, err)
	}
	if state, _ := env.uc.InspectInvitation(ctx, cris.Principal, req.ICal, crisAddrs); state.EventID != "" {
		t.Fatalf("un rechazo quedo en el calendario: %+v", state)
	}
}

func TestEditarUnaAparicionDesdeLaAPI(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	f := meeting("Diario", schedNow.Add(24*time.Hour), 30)
	f.TimeZone = "America/Lima"
	f.Recurrence = &domain.Recurrence{Freq: "daily", Count: 5}
	ev, err := env.uc.CreateEvent(ctx, ana.Principal, f)
	if err != nil {
		t.Fatal(err)
	}
	third := f.Start.Add(48 * time.Hour)
	upd, err := env.uc.UpdateOccurrence(ctx, ana.Principal, ev.ID, third, meeting("Movida", third.Add(time.Hour), 30), domain.Precondition{IfMatch: []string{ev.ETag}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.uc.DeleteOccurrence(ctx, ana.Principal, ev.ID, third.Add(24*time.Hour), domain.Precondition{IfMatch: []string{ev.ETag}}); !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("un etag viejo: %v", err)
	}
	if _, err := env.uc.DeleteOccurrence(ctx, ana.Principal, ev.ID, third.Add(24*time.Hour), domain.Precondition{IfMatch: []string{upd.ETag}}); err != nil {
		t.Fatal(err)
	}
	occs, err := env.uc.EventOccurrences(ctx, ana.Principal, schedNow, schedNow.Add(10*24*time.Hour), 100)
	if err != nil || len(occs) != 4 || occs[2].Title != "Movida" || !occs[2].RecurrenceID.Equal(third) {
		t.Fatalf("apariciones: %+v %v", occs, err)
	}
	// La ocupacion materializada sigue a la serie editada.
	list, _ := env.uc.Availability(ctx, cris.Principal, []string{"ana@acme.test"}, schedNow, schedNow.Add(10*24*time.Hour))
	if len(list) != 1 || !list[0].Known || len(list[0].Busy) != 4 || !list[0].Busy[2].Start.Equal(third.Add(time.Hour)) {
		t.Fatalf("ocupacion: %+v", list)
	}
}

func bookingSettings() domain.BookingSettings {
	s := domain.BookingSettings{Title: "Demo", DurationMinutes: 30, BufferMinutes: 0, MinNoticeMinutes: 60, MaxAdvanceDays: 14, DailyLimit: 3, TimeZone: "UTC", Active: true}
	for d := time.Monday; d <= time.Friday; d++ {
		s.Weekly[d] = []domain.DayWindow{{Start: 9 * 60, End: 12 * 60}}
	}
	return s
}

func TestPaginaDeCitas(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	if _, err := env.uc.SaveBookingSettings(ctx, domain.Principal{TenantID: ana.Principal.TenantID, MailboxID: ana.Principal.MailboxID}, bookingSettings(), "Ana", false); err == nil {
		t.Fatal("sin la direccion del buzon no hay dueno")
	}
	page, err := env.uc.SaveBookingSettings(ctx, ana.Principal, bookingSettings(), "Ana", false)
	if err != nil {
		t.Fatal(err)
	}
	if !domain.ValidPublicID(page.PublicID) || page.OwnerAddress != "ana@acme.test" {
		t.Fatalf("pagina: %+v", page)
	}
	tenant := ana.Principal.TenantID
	res, err := env.uc.PublicBookingSlots(ctx, tenant, page.PublicID, schedNow, schedNow.Add(3*24*time.Hour))
	if err != nil || len(res.Slots) != 18 {
		t.Fatalf("huecos (martes a jueves de 9 a 12): %d %v", len(res.Slots), err)
	}
	for name, call := range map[string]func() error{
		"otra empresa": func() error {
			_, err := env.uc.PublicBookingSlots(ctx, bea.Principal.TenantID, page.PublicID, schedNow, schedNow.Add(time.Hour))
			return err
		},
		"enlace desconocido": func() error {
			_, err := env.uc.PublicBookingSlots(ctx, tenant, "AAAAAAAAAAAAAAAAAAAAAAAA", schedNow, schedNow.Add(time.Hour))
			return err
		},
		"enlace mal formado": func() error {
			_, err := env.uc.PublicBookingSlots(ctx, tenant, "../x", schedNow, schedNow.Add(time.Hour))
			return err
		},
		"empresa vacia": func() error {
			_, err := env.uc.PublicBookingSlots(ctx, uuid.Nil, page.PublicID, schedNow, schedNow.Add(time.Hour))
			return err
		},
	} {
		if err := call(); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}

	slot := res.Slots[0]
	booked, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: slot.Start, Name: "Luis", Email: "luis@cliente.test", Note: "Quiero ver precios"})
	if err != nil {
		t.Fatal(err)
	}
	if booked.Invitation.Method != domain.MethodRequest || len(booked.Invitation.Recipients) != 1 || booked.Invitation.Recipients[0] != "luis@cliente.test" ||
		strings.Contains(booked.Invitation.ICal, "precios") || booked.Page.OwnerAddress != "ana@acme.test" {
		t.Fatalf("reserva: %+v", booked)
	}
	ev, err := env.uc.EventByID(ctx, ana.Principal, booked.Event.ID)
	if err != nil || !strings.Contains(ev.Fields.Description, "precios") || len(ev.Fields.Attendees) != 2 {
		t.Fatalf("evento del dueno: %+v %v", ev, err)
	}
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: slot.Start, Name: "Otro", Email: "otro@cliente.test"}); !errors.Is(err, domain.ErrSlotUnavailable) {
		t.Fatalf("el mismo hueco dos veces: %v", err)
	}
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: slot.Start.Add(10 * time.Minute), Name: "Otro", Email: "otro@cliente.test"}); !errors.Is(err, domain.ErrSlotUnavailable) {
		t.Fatalf("una hora que no es un hueco: %v", err)
	}
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: res.Slots[1].Start, Name: "Ana", Email: "ana@acme.test"}); err == nil {
		t.Fatal("el dueno no se reserva a si mismo")
	}
	res, _ = env.uc.PublicBookingSlots(ctx, tenant, page.PublicID, schedNow, schedNow.Add(3*24*time.Hour))
	if len(res.Slots) != 17 {
		t.Fatalf("el hueco reservado sigue libre: %d", len(res.Slots))
	}
	// Tope por visitante (2) y diario de la pagina (3).
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: res.Slots[0].Start, Name: "Luis", Email: "LUIS@cliente.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: res.Slots[1].Start, Name: "Luis", Email: "luis@cliente.test"}); !errors.Is(err, domain.ErrBookingLimit) {
		t.Fatalf("tope por visitante: %v", err)
	}
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: res.Slots[1].Start, Name: "Eva", Email: "eva@cliente.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: res.Slots[2].Start, Name: "Leo", Email: "leo@cliente.test"}); !errors.Is(err, domain.ErrBookingLimit) {
		t.Fatalf("tope diario: %v", err)
	}

	// Cambiar el enlace deja el anterior sin servicio; desactivar la pagina la cierra.
	renewed, err := env.uc.SaveBookingSettings(ctx, ana.Principal, bookingSettings(), "Ana", true)
	if err != nil || renewed.PublicID == page.PublicID {
		t.Fatalf("nuevo enlace: %+v %v", renewed, err)
	}
	if _, err := env.uc.PublicBookingSlots(ctx, tenant, page.PublicID, schedNow, schedNow.Add(time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("enlace anterior: %v", err)
	}
	off := bookingSettings()
	off.Active = false
	if _, err := env.uc.SaveBookingSettings(ctx, ana.Principal, off, "Ana", false); err != nil {
		t.Fatal(err)
	}
	if _, err := env.uc.PublicBookingSlots(ctx, tenant, renewed.PublicID, schedNow, schedNow.Add(time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pagina inactiva: %v", err)
	}

	// La baja del buzon retira su pagina.
	if _, err := env.uc.PurgeMailbox(ctx, ana.Principal.TenantID, ana.Principal.MailboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.uc.BookingSettings(ctx, ana.Principal); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pagina tras la baja: %v", err)
	}
}
