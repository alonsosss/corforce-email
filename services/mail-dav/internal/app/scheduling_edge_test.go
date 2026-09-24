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

func TestConfiguracionDePlanificacion(t *testing.T) {
	if err := (app.SchedulingConfig{}).Validate(); err != nil {
		t.Fatalf("apagada no se valida: %v", err)
	}
	if err := schedulingConfig.Validate(); err != nil {
		t.Fatalf("la de las pruebas: %v", err)
	}
	for name, change := range map[string]func(*app.SchedulingConfig){
		"sin tramos por evento":          func(c *app.SchedulingConfig) { c.MaxBusyPerEvent = 0 },
		"sin eventos por consulta":       func(c *app.SchedulingConfig) { c.BusyRefreshMax = 0 },
		"sin direcciones":                func(c *app.SchedulingConfig) { c.MaxAvailabilityAddresses = 0 },
		"demasiadas direcciones":         func(c *app.SchedulingConfig) { c.MaxAvailabilityAddresses = domain.MaxBusyMailboxes + 1 },
		"sin tope diario":                func(c *app.SchedulingConfig) { c.BookingMaxDaily = 0 },
		"sin tope por visitante":         func(c *app.SchedulingConfig) { c.BookingMaxPerVisitor = 0 },
		"sin huecos":                     func(c *app.SchedulingConfig) { c.BookingMaxSlots = 0 },
		"sin mirar atras":                func(c *app.SchedulingConfig) { c.BusyLookback = 0 },
		"sin ventana de citas":           func(c *app.SchedulingConfig) { c.BookingMaxWindow = 0 },
		"citas mas alla de la ocupacion": func(c *app.SchedulingConfig) { c.BookingMaxWindow = domain.MaxBusyWindow + time.Hour },
	} {
		c := schedulingConfig
		change(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: se acepto %+v", name, c)
		}
	}
}

// Con la planificacion apagada ninguna de sus operaciones responde, y lo dice como indisponible (no como un fallo
// de la peticion).
func TestSinPlanificacionNadaResponde(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	uc, tenant := env.plain, ana.Principal.TenantID
	addrs := []string{"cris@acme.test"}
	for name, call := range map[string]func() error{
		"pagina": func() error { _, err := uc.BookingSettings(ctx, ana.Principal); return err },
		"guardar pagina": func() error {
			_, err := uc.SaveBookingSettings(ctx, ana.Principal, bookingSettings(), "Ana", false)
			return err
		},
		"huecos": func() error {
			_, err := uc.PublicBookingSlots(ctx, tenant, "AAAAAAAAAAAAAAAAAAAAAAAA", schedNow, schedNow.Add(time.Hour))
			return err
		},
		"reservar": func() error {
			_, err := uc.Book(ctx, tenant, "AAAAAAAAAAAAAAAAAAAAAAAA", domain.BookingRequest{Start: schedNow.Add(24 * time.Hour), Name: "Luis", Email: "luis@cliente.test"})
			return err
		},
		"inspeccionar": func() error { _, err := uc.InspectInvitation(ctx, cris.Principal, "x", addrs); return err },
		"responder": func() error {
			_, err := uc.RespondInvitation(ctx, cris.Principal, "x", addrs, "ACCEPTED")
			return err
		},
		"aplicar": func() error {
			_, err := uc.ApplyInvitation(ctx, cris.Principal, "x", "ana@acme.test", addrs)
			return err
		},
	} {
		if err := call(); !errors.Is(err, app.ErrSchedulingDisabled) || !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Una consulta completa como mucho BusyRefreshMax eventos pendientes; lo demas queda para la siguiente y la
// respuesta lo marca como parcial.
func TestDisponibilidadParcialSeCompletaPorTandas(t *testing.T) {
	store := apptest.NewStore()
	store.Now = func() time.Time { return schedNow }
	cfg := testConfig
	cfg.Calendar.MaxEventsPerMailbox = 50
	cfg.Scheduling = schedulingConfig
	cfg.Scheduling.BusyRefreshMax = 1
	now := func() time.Time { return schedNow }
	uc, err := app.New(app.Deps{Auth: apptest.NewAuth(ana, bea, cris), Tenant: apptest.Binder{}, Store: store, Calendars: store, Scheduling: apptest.NewScheduling(store), Config: cfg, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	plainCfg := cfg
	plainCfg.Scheduling = app.SchedulingConfig{}
	plain, err := app.New(app.Deps{Auth: apptest.NewAuth(ana, bea, cris), Tenant: apptest.Binder{}, Store: store, Calendars: store, Config: plainCfg, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := schedNow.Add(24 * time.Hour)
	if _, err := uc.CreateEvent(ctx, cris.Principal, meeting("Con ocupacion", at, 30)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := plain.CreateEvent(ctx, cris.Principal, meeting("Antiguo", at.Add(time.Duration(i)*time.Hour), 30)); err != nil {
			t.Fatal(err)
		}
	}
	window := func() app.MailboxAvailability {
		t.Helper()
		list, err := uc.Availability(ctx, ana.Principal, []string{"cris@acme.test", "CRIS@acme.test"}, schedNow, schedNow.Add(7*24*time.Hour))
		if err != nil || len(list) != 1 {
			t.Fatalf("disponibilidad: %+v %v", list, err)
		}
		return list[0]
	}
	for i, want := range []struct {
		partial bool
		busy    int
	}{{true, 2}, {true, 3}, {false, 4}, {false, 4}} {
		got := window()
		if got.Partial != want.partial || len(got.Busy) != want.busy {
			t.Fatalf("consulta %d: parcial %v con %d tramos, se esperaba %v con %d", i+1, got.Partial, len(got.Busy), want.partial, want.busy)
		}
	}
}

func TestLimitesDeLaDisponibilidad(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	many := make([]string, schedulingConfig.MaxAvailabilityAddresses+1)
	for i := range many {
		many[i] = "p" + string(rune('a'+i)) + "@acme.test"
	}
	var fe *domain.FieldError
	for name, c := range map[string]struct {
		addrs    []string
		from, to time.Time
		field    string
	}{
		"demasiadas direcciones": {many, schedNow, schedNow.Add(time.Hour), "addresses"},
		"fin antes del inicio":   {[]string{"cris@acme.test"}, schedNow, schedNow, "end"},
		"mas alla del horizonte": {[]string{"cris@acme.test"}, schedNow.Add(399 * 24 * time.Hour), schedNow.Add(401 * 24 * time.Hour), "end"},
	} {
		_, err := env.uc.Availability(ctx, ana.Principal, c.addrs, c.from, c.to)
		if !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Un buzon que nunca uso el calendario no esta registrado: se informa como desconocido, sin error.
	list, err := env.uc.Availability(ctx, ana.Principal, []string{"cris@acme.test"}, schedNow, schedNow.Add(time.Hour))
	if err != nil || len(list) != 1 || list[0].Known || list[0].Busy == nil {
		t.Fatalf("buzon sin registrar: %+v %v", list, err)
	}
}

// Una invitacion reenviada tras cambiar la hora lleva una SEQUENCE mayor; la respuesta a la version anterior ya no
// se aplica sobre la nueva.
func TestUnaInvitacionAntiguaNoPisaLaNueva(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	f := meeting("Plan", schedNow.Add(48*time.Hour), 60)
	f.Organizer = &domain.Party{Email: "ana@acme.test"}
	f.Attendees = []domain.Attendee{{Email: "cris@acme.test"}}
	ev, err := env.uc.CreateEvent(ctx, ana.Principal, f)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := env.uc.EventInvitation(ctx, ana.Principal, ev.ID, domain.MethodRequest)
	if err != nil {
		t.Fatal(err)
	}
	f.Start, f.End = f.Start.Add(time.Hour), f.End.Add(time.Hour)
	if _, err := env.uc.UpdateEvent(ctx, ana.Principal, ev.ID, f, domain.Precondition{IfMatch: []string{ev.ETag}}); err != nil {
		t.Fatal(err)
	}
	second, _, err := env.uc.EventInvitation(ctx, ana.Principal, ev.ID, domain.MethodRequest)
	if err != nil {
		t.Fatal(err)
	}
	if domain.SequenceOf(second.ICal) <= domain.SequenceOf(first.ICal) {
		t.Fatalf("la hora cambio y la secuencia no subio: %d -> %d", domain.SequenceOf(first.ICal), domain.SequenceOf(second.ICal))
	}
	addrs := []string{"cris@acme.test"}
	if _, err := env.uc.RespondInvitation(ctx, cris.Principal, second.ICal, addrs, "ACCEPTED"); err != nil {
		t.Fatal(err)
	}
	var fe *domain.FieldError
	if _, err := env.uc.RespondInvitation(ctx, cris.Principal, first.ICal, addrs, "ACCEPTED"); !errors.As(err, &fe) || fe.Field != "sequence" {
		t.Fatalf("una invitacion antigua: %v", err)
	}
	// Quien no esta invitado no puede responder.
	if _, err := env.uc.RespondInvitation(ctx, bea.Principal, second.ICal, []string{"bea@beta.test"}, "ACCEPTED"); err == nil {
		t.Fatal("respondio quien no esta invitado")
	}
	for name, call := range map[string]func() error{
		"identificador invalido": func() error {
			_, _, err := env.uc.EventInvitation(ctx, ana.Principal, "../x", domain.MethodRequest)
			return err
		},
		"evento inexistente": func() error {
			_, _, err := env.uc.EventInvitation(ctx, ana.Principal, uuid.NewString(), domain.MethodRequest)
			return err
		},
		"aplicar lo que no esta": func() error {
			_, err := env.uc.ApplyInvitation(ctx, bea.Principal, second.ICal, "ana@acme.test", []string{"bea@beta.test"})
			return err
		},
	} {
		if err := call(); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := env.uc.EventInvitation(ctx, ana.Principal, ev.ID, "PUBLISH"); err == nil {
		t.Fatal("un metodo desconocido")
	}
	// Una invitacion (REQUEST) no se aplica: se responde.
	if _, err := env.uc.ApplyInvitation(ctx, cris.Principal, second.ICal, "ana@acme.test", addrs); !errors.As(err, &fe) || fe.Field != "method" {
		t.Fatalf("aplicar un REQUEST: %v", err)
	}
}

// Un CANCEL con RECURRENCE-ID retira solo esa aparicion de la copia del invitado; uno de una aparicion que no
// existe no cambia nada.
func TestCancelarUnaAparicionDeLaSerie(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)
	f := meeting("Diaria", start, 30)
	f.Recurrence = &domain.Recurrence{Freq: "daily", Count: 3}
	f.Organizer = &domain.Party{Email: "ana@acme.test"}
	f.Attendees = []domain.Attendee{{Email: "cris@acme.test"}}
	ev, err := env.uc.CreateEvent(ctx, ana.Principal, f)
	if err != nil {
		t.Fatal(err)
	}
	req, _, err := env.uc.EventInvitation(ctx, ana.Principal, ev.ID, domain.MethodRequest)
	if err != nil {
		t.Fatal(err)
	}
	addrs := []string{"cris@acme.test"}
	ans, err := env.uc.RespondInvitation(ctx, cris.Principal, req.ICal, addrs, "ACCEPTED")
	if err != nil {
		t.Fatal(err)
	}
	state, err := env.uc.InspectInvitation(ctx, cris.Principal, req.ICal, addrs)
	if err != nil || !state.Invitation.Recurring {
		t.Fatalf("inspeccion: %+v %v", state, err)
	}
	cancelOf := func(rid time.Time) string {
		stamp := rid.UTC().Format("20060102T150405Z")
		return strings.Join([]string{
			"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Prueba//ES", "METHOD:CANCEL", "BEGIN:VEVENT",
			"UID:" + state.Invitation.UID, "DTSTAMP:20260928T120000Z", "RECURRENCE-ID:" + stamp,
			"DTSTART:" + stamp, "DTEND:" + rid.Add(30*time.Minute).UTC().Format("20060102T150405Z"), "SEQUENCE:1",
			"ORGANIZER:mailto:ana@acme.test", "ATTENDEE:mailto:cris@acme.test", "STATUS:CANCELLED",
			"END:VEVENT", "END:VCALENDAR", "",
		}, "\r\n")
	}
	applied, err := env.uc.ApplyInvitation(ctx, cris.Principal, cancelOf(start.Add(24*time.Hour)), "ana@acme.test", addrs)
	if err != nil || !applied.Changed || applied.EventID != ans.EventID {
		t.Fatalf("cancelar la segunda: %+v %v", applied, err)
	}
	occs, err := env.uc.EventOccurrences(ctx, cris.Principal, start.Add(-time.Hour), start.Add(5*24*time.Hour), 10)
	if err != nil || len(occs) != 2 || !occs[0].Start.Equal(start) || !occs[1].Start.Equal(start.Add(48*time.Hour)) {
		t.Fatalf("apariciones de cris: %+v %v", occs, err)
	}
	applied, err = env.uc.ApplyInvitation(ctx, cris.Principal, cancelOf(start.Add(time.Hour)), "ana@acme.test", addrs)
	if err != nil || applied.Changed {
		t.Fatalf("cancelar una aparicion que no existe: %+v %v", applied, err)
	}
	if _, err := env.uc.ApplyInvitation(ctx, cris.Principal, cancelOf(start), "no es", addrs); err == nil {
		t.Fatal("un remitente invalido")
	}
}

func TestEditarAparicionesDeLoQueNoExiste(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	at := schedNow.Add(24 * time.Hour)
	if _, err := env.uc.UpdateOccurrence(ctx, ana.Principal, "../x", at, meeting("x", at, 30), domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("identificador invalido: %v", err)
	}
	if _, err := env.uc.DeleteOccurrence(ctx, ana.Principal, uuid.NewString(), at, domain.Precondition{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("evento inexistente: %v", err)
	}
	var fe *domain.FieldError
	if _, err := env.uc.UpdateOccurrence(ctx, ana.Principal, uuid.NewString(), at, domain.EventFields{}, domain.Precondition{}); !errors.As(err, &fe) {
		t.Fatalf("campos invalidos: %v", err)
	}
	// Un evento sin repeticion no tiene apariciones que editar por separado.
	ev, err := env.uc.CreateEvent(ctx, ana.Principal, meeting("Unico", at, 30))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.uc.DeleteOccurrence(ctx, ana.Principal, ev.ID, at.Add(24*time.Hour), domain.Precondition{}); err == nil {
		t.Fatal("se borro una aparicion de un evento unico")
	}
}

func TestLimitesDeLaPaginaDeCitas(t *testing.T) {
	env := newSchedulingEnv(t)
	ctx := context.Background()
	var fe *domain.FieldError
	bad := bookingSettings()
	bad.DurationMinutes = 0
	if _, err := env.uc.SaveBookingSettings(ctx, ana.Principal, bad, "Ana", false); !errors.As(err, &fe) {
		t.Fatalf("duracion cero: %v", err)
	}
	if _, err := env.uc.SaveBookingSettings(ctx, ana.Principal, bookingSettings(), strings.Repeat("a", 500), false); !errors.As(err, &fe) {
		t.Fatalf("nombre largo: %v", err)
	}
	page, err := env.uc.SaveBookingSettings(ctx, ana.Principal, bookingSettings(), "Ana", false)
	if err != nil {
		t.Fatal(err)
	}
	// Guardar otra vez sin pedir un enlace nuevo lo conserva.
	again, err := env.uc.SaveBookingSettings(ctx, ana.Principal, bookingSettings(), "Ana Ruiz", false)
	if err != nil || again.PublicID != page.PublicID || again.OwnerName != "Ana Ruiz" {
		t.Fatalf("mismo enlace: %+v %v", again, err)
	}
	tenant := ana.Principal.TenantID
	for name, c := range map[string]struct{ from, to time.Time }{
		"fin antes del inicio": {schedNow.Add(time.Hour), schedNow},
		"ventana larga":        {schedNow, schedNow.Add(schedulingConfig.BookingMaxWindow + time.Hour)},
	} {
		if _, err := env.uc.PublicBookingSlots(ctx, tenant, page.PublicID, c.from, c.to); !errors.As(err, &fe) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Todo lo que cae despues de la antelacion maxima no ofrece huecos.
	far := schedNow.Add(20 * 24 * time.Hour)
	res, err := env.uc.PublicBookingSlots(ctx, tenant, page.PublicID, far, far.Add(24*time.Hour))
	if err != nil || len(res.Slots) != 0 || res.Slots == nil {
		t.Fatalf("mas alla de la antelacion: %+v %v", res, err)
	}
	// La peticion se valida antes de mirar la pagina.
	if _, err := env.uc.Book(ctx, tenant, "AAAAAAAAAAAAAAAAAAAAAAAA", domain.BookingRequest{Start: schedNow.Add(24 * time.Hour), Email: "luis@cliente.test"}); !errors.As(err, &fe) {
		t.Fatalf("sin nombre: %v", err)
	}
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: schedNow.Add(24 * time.Hour), Name: "Luis", Email: "no es"}); !errors.As(err, &fe) {
		t.Fatalf("correo invalido: %v", err)
	}
	// Un hueco dentro del aviso minimo no se reserva.
	if _, err := env.uc.Book(ctx, tenant, page.PublicID, domain.BookingRequest{Start: schedNow.Add(30 * time.Minute), Name: "Luis", Email: "luis@cliente.test"}); !errors.Is(err, domain.ErrSlotUnavailable) {
		t.Fatalf("dentro del aviso minimo: %v", err)
	}
}
