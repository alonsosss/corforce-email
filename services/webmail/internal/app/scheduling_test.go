package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// fakeScheduling hace de la planificacion de mail-dav: anota lo que recibe y devuelve lo que prepare la prueba.
type fakeScheduling struct {
	mb          domain.MailboxRef
	method      string
	ical        string
	addresses   []string
	response    string
	from        string
	itip        domain.ITIPMessage
	invitation  domain.Invitation
	answer      domain.InvitationAnswer
	applied     domain.InvitationApplied
	page        domain.PublicBookingPage
	confirm     domain.BookingConfirmation
	booked      int
	bookReq     domain.BookingRequest
	ownerName   string
	err         error
	occurrences []string
}

func (f *fakeScheduling) UpdateOccurrence(_ context.Context, mb domain.MailboxRef, id, rid string, in domain.EventInput, _ string) (domain.Event, error) {
	f.mb, f.occurrences = mb, append(f.occurrences, "put:"+id+"@"+rid)
	return domain.Event{ID: id, EventInput: in}, f.err
}
func (f *fakeScheduling) DeleteOccurrence(_ context.Context, mb domain.MailboxRef, id, rid, _ string) (domain.Event, error) {
	f.mb, f.occurrences = mb, append(f.occurrences, "delete:"+id+"@"+rid)
	return domain.Event{ID: id}, f.err
}
func (f *fakeScheduling) EventInvitation(_ context.Context, mb domain.MailboxRef, _, method string) (domain.ITIPMessage, error) {
	f.mb, f.method = mb, method
	out := f.itip
	out.Method = method
	return out, f.err
}
func (f *fakeScheduling) InspectInvitation(_ context.Context, mb domain.MailboxRef, ical string, addresses []string) (domain.Invitation, error) {
	f.mb, f.ical, f.addresses = mb, ical, addresses
	return f.invitation, f.err
}
func (f *fakeScheduling) RespondInvitation(_ context.Context, mb domain.MailboxRef, ical string, addresses []string, response string) (domain.InvitationAnswer, error) {
	f.mb, f.ical, f.addresses, f.response = mb, ical, addresses, response
	return f.answer, f.err
}
func (f *fakeScheduling) ApplyInvitation(_ context.Context, mb domain.MailboxRef, ical, from string, addresses []string) (domain.InvitationApplied, error) {
	f.mb, f.ical, f.from, f.addresses = mb, ical, from, addresses
	return f.applied, f.err
}
func (f *fakeScheduling) Availability(_ context.Context, mb domain.MailboxRef, addresses []string, _ domain.EventWindow) ([]domain.MailboxAvailability, error) {
	f.mb, f.addresses = mb, addresses
	return []domain.MailboxAvailability{{Address: addresses[0], Known: true}}, f.err
}
func (f *fakeScheduling) BookingSettings(_ context.Context, mb domain.MailboxRef) (domain.BookingPage, error) {
	f.mb = mb
	return domain.BookingPage{PublicID: "enlace-publico-de-prueba-01"}, f.err
}
func (f *fakeScheduling) SaveBookingSettings(_ context.Context, mb domain.MailboxRef, in domain.BookingSettings, ownerName string, _ bool) (domain.BookingPage, error) {
	f.mb, f.ownerName = mb, ownerName
	return domain.BookingPage{BookingSettings: in, PublicID: "enlace-publico-de-prueba-01"}, f.err
}
func (f *fakeScheduling) PublicBooking(context.Context, string, string, domain.EventWindow) (domain.PublicBookingPage, error) {
	return f.page, f.err
}
func (f *fakeScheduling) Book(_ context.Context, _, _ string, in domain.BookingRequest) (domain.BookingConfirmation, error) {
	f.booked++
	f.bookReq = in
	return f.confirm, f.err
}

type fakeInvitationComposer struct {
	mails []domain.InvitationMail
}

func (c *fakeInvitationComposer) ComposeInvitation(m domain.InvitationMail) ([]byte, error) {
	c.mails = append(c.mails, m)
	return []byte("invitacion"), nil
}

func newSchedulingHarness(t *testing.T) (*harness, *fakeScheduling, *fakeInvitationComposer) {
	t.Helper()
	h := newHarness(t)
	sched, composer := &fakeScheduling{}, &fakeInvitationComposer{}
	d := h.deps()
	d.Scheduling, d.Invitations = sched, composer
	svc, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h, sched, composer
}

func TestUnEventoConInvitadosLoOrganizaElBuzonYEnviaLaInvitacion(t *testing.T) {
	h, sched, composer := newSchedulingHarness(t)
	sess := schedSession()
	ctx := context.Background()
	sched.itip = domain.ITIPMessage{ICal: "BEGIN:VCALENDAR", Recipients: []string{"bea@cliente.pe", "no es correo"}}
	in := domain.EventInput{Title: "Revision", Start: "2026-10-01T15:00:00Z", End: "2026-10-01T16:00:00Z", TimeZone: "America/Lima",
		Organizer: &domain.Party{Email: "mallory@evil.test"}, Attendees: []domain.Attendee{{Email: "bea@cliente.pe"}}}
	saved, err := h.svc.CreateEvent(ctx, sess, in, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.dav.event.Organizer; got == nil || got.Email != testUser || got.Name != "Ana Perez" {
		t.Fatalf("el organizador lo pone el servicio: %+v", got)
	}
	if h.dav.mb.Address != testUser {
		t.Fatalf("la direccion del buzon viaja a mail-dav: %+v", h.dav.mb)
	}
	if saved.Delivery == nil || !saved.Delivery.Sent || saved.Delivery.Recipients != 1 || sched.method != domain.MethodRequest {
		t.Fatalf("entrega: %+v", saved.Delivery)
	}
	call := h.sender.calls[len(h.sender.calls)-1]
	if call.username != testUser || call.from != testUser || len(call.rcpts) != 1 || call.rcpts[0] != "bea@cliente.pe" {
		t.Fatalf("envio: %+v", call)
	}
	mail := composer.mails[0]
	if mail.Method != domain.MethodRequest || mail.Subject != "Invitacion: Revision" || !strings.Contains(mail.Text, "2026-10-01 10:00 - 11:00 (America/Lima)") {
		t.Fatalf("correo: %+v", mail)
	}

	// Sin notificar no sale nada; sin invitados no hay organizador ni invitacion.
	sent := len(h.sender.calls)
	if saved, err = h.svc.UpdateEvent(ctx, sess, "e1", in, "", false); err != nil || saved.Delivery != nil || len(h.sender.calls) != sent {
		t.Fatalf("sin notificar: %+v %v", saved.Delivery, err)
	}
	in.Attendees = nil
	if saved, err = h.svc.UpdateEvent(ctx, sess, "e1", in, "", true); err != nil || saved.Delivery != nil || h.dav.event.Organizer != nil {
		t.Fatalf("sin invitados: %+v %+v %v", saved.Delivery, h.dav.event.Organizer, err)
	}
	// Una copia de un evento que organiza otro no envia invitaciones.
	h.dav.event.Organizer = &domain.Party{Email: "jefe@otra.pe"}
	h.dav.event.Attendees = []domain.Attendee{{Email: testUser}}
	if !strings.EqualFold(testUser, h.dav.event.Attendees[0].Email) || h.svc.organizes(sess, h.dav.event) {
		t.Fatal("no organiza lo que organiza otro")
	}

	// Un fallo del envio no deshace el evento: se ve en la entrega.
	h.sender.err = errors.New("postfix caido")
	in.Attendees = []domain.Attendee{{Email: "bea@cliente.pe"}}
	if saved, err = h.svc.CreateEvent(ctx, sess, in, true); err != nil || saved.Delivery == nil || saved.Delivery.Sent {
		t.Fatalf("envio fallido: %+v %v", saved.Delivery, err)
	}
}

func TestBorrarUnaReunionEnviaLaCancelacion(t *testing.T) {
	h, sched, composer := newSchedulingHarness(t)
	sess := schedSession()
	h.dav.event = domain.Event{ID: "e1", EventInput: domain.EventInput{Title: "Comite", Organizer: &domain.Party{Email: testUser},
		Attendees: []domain.Attendee{{Email: "bea@cliente.pe"}}}}
	sched.itip = domain.ITIPMessage{ICal: "BEGIN:VCALENDAR", Recipients: []string{"bea@cliente.pe"}}
	d, err := h.svc.DeleteEvent(context.Background(), sess, "e1", true)
	if err != nil || d == nil || !d.Sent || sched.method != domain.MethodCancel || composer.mails[0].Subject != "Cancelada: Comite" {
		t.Fatalf("cancelacion: %+v %v", d, err)
	}
	if d, err := h.svc.DeleteEvent(context.Background(), sess, "e1", false); err != nil || d != nil {
		t.Fatalf("sin notificar: %+v %v", d, err)
	}
}

func invitationMessage(h *harness, from string) {
	h.mb.raw = &domain.RawMessage{
		Envelope: domain.Envelope{UID: 9, From: []domain.Address{{Email: from}}},
		Parts:    []domain.Part{{ID: "2", ContentType: "application/ics"}, {ID: "3", ContentType: "text/calendar"}},
	}
	h.mb.parts = map[string]storedPart{
		"2": {part: domain.Part{ID: "2"}, data: []byte("ADJUNTO")},
		"3": {part: domain.Part{ID: "3"}, data: []byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\n")},
	}
}

func TestResponderUnaInvitacionEnviaElReplyAlOrganizador(t *testing.T) {
	h, sched, composer := newSchedulingHarness(t)
	sess := schedSession()
	invitationMessage(h, "jefe@otra.pe")
	h.directory.ids = []string{"ventas@empresa.pe"}
	start, end := "2026-10-01T15:00:00Z", "2026-10-01T16:00:00Z"
	sched.invitation = domain.Invitation{Method: domain.MethodRequest, Title: "Plan", Start: &start, End: &end}
	sched.answer = domain.InvitationAnswer{Reply: "BEGIN:VCALENDAR\r\nMETHOD:REPLY", Organizer: domain.Party{Email: "jefe@otra.pe", Name: "Jefe"},
		Attendee: "ventas@empresa.pe", EventID: "ev-1"}
	res, err := h.svc.RespondInvitation(context.Background(), sess, "INBOX", 9, "accepted")
	if err != nil || !res.ReplySent || res.EventID != "ev-1" {
		t.Fatalf("respuesta: %+v %v", res, err)
	}
	if sched.ical != "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\n" || sched.response != domain.PartStatAccepted {
		t.Fatalf("se lee la parte text/calendar: %q %q", sched.ical, sched.response)
	}
	if len(sched.addresses) != 2 || sched.addresses[0] != testUser || sched.addresses[1] != "ventas@empresa.pe" {
		t.Fatalf("direcciones del buzon: %v", sched.addresses)
	}
	mail := composer.mails[0]
	call := h.sender.calls[0]
	if mail.Method != domain.MethodReply || mail.From.Email != "ventas@empresa.pe" || mail.To[0].Email != "jefe@otra.pe" ||
		mail.Subject != "Aceptada: Plan" || call.username != testUser || call.from != "ventas@empresa.pe" {
		t.Fatalf("REPLY: %+v %+v", mail, call)
	}
	if _, err := h.svc.RespondInvitation(context.Background(), sess, "INBOX", 9, "quizas"); err == nil {
		t.Fatal("respuesta no admitida")
	}
	h.mb.raw.Parts = nil
	if _, err := h.svc.Invitation(context.Background(), sess, "INBOX", 9); !errors.Is(err, domain.ErrPartNotFound) {
		t.Fatalf("un mensaje sin invitacion: %v", err)
	}
}

func TestAplicarUnaRespuestaUsaElRemitenteDelMensaje(t *testing.T) {
	h, sched, _ := newSchedulingHarness(t)
	sess := schedSession()
	invitationMessage(h, "bea@cliente.pe")
	sched.applied = domain.InvitationApplied{Method: domain.MethodReply, Changed: true}
	res, err := h.svc.ApplyInvitation(context.Background(), sess, "INBOX", 9)
	if err != nil || !res.Changed || sched.from != "bea@cliente.pe" || len(h.sender.calls) != 0 {
		t.Fatalf("aplicar: %+v %v %q", res, err, sched.from)
	}
}

func TestDisponibilidadYAparicionesValidanLaEntrada(t *testing.T) {
	h, sched, _ := newSchedulingHarness(t)
	sess := schedSession()
	ctx := context.Background()
	w, _ := domain.NewEventWindow("2026-10-01T00:00:00Z", "2026-10-08T00:00:00Z")
	if _, err := h.svc.Availability(ctx, sess, []string{"Bea@Empresa.pe, carlos@empresa.pe", "bea@empresa.pe"}, w); err != nil ||
		len(sched.addresses) != 2 || sched.addresses[0] != "bea@empresa.pe" {
		t.Fatalf("direcciones: %v %v", sched.addresses, err)
	}
	var verr *domain.ValidationError
	if _, err := h.svc.Availability(ctx, sess, []string{"no es"}, w); !errors.As(err, &verr) {
		t.Fatalf("direccion invalida: %v", err)
	}
	if _, err := h.svc.UpdateOccurrence(ctx, sess, "e1", "ayer", domain.EventInput{}, "", true); !errors.As(err, &verr) {
		t.Fatalf("aparicion invalida: %v", err)
	}
	if _, err := h.svc.DeleteOccurrence(ctx, sess, "e1", "2026-10-02T15:00:00Z", "", true); err != nil || sched.occurrences[0] != "delete:e1@2026-10-02T15:00:00Z" {
		t.Fatalf("borrar aparicion: %v %v", sched.occurrences, err)
	}
	page, err := h.svc.SaveBookingSettings(ctx, sess, domain.BookingSettings{Title: "Demo"}, false)
	if err != nil || page.Cell != testCell || page.TenantID != sess.TenantID || sched.ownerName != "Ana Perez" {
		t.Fatalf("pagina de citas: %+v %v", page, err)
	}
}

func publicTenant(sess domain.Session) string { return sess.TenantID }

func schedSession() domain.Session {
	s := davSession()
	s.DisplayName = "Ana Perez"
	return s
}

const publicPage = "enlace-publico-de-prueba-01"

func TestPaginaPublicaDeCitas(t *testing.T) {
	h, sched, composer := newSchedulingHarness(t)
	sess := schedSession()
	ctx := context.Background()
	tenant := publicTenant(sess)
	w, _ := domain.NewEventWindow("2026-10-01T00:00:00Z", "2026-10-08T00:00:00Z")
	sched.page = domain.PublicBookingPage{Title: "Demo", OwnerName: "Ana Perez", OwnerAddress: testUser}
	p, err := h.svc.PublicBooking(ctx, testCell, tenant, publicPage, w)
	if err != nil || p.OwnerAddress != "" || p.Title != "Demo" {
		t.Fatalf("la pagina publica no lleva la direccion del dueno: %+v %v", p, err)
	}
	for name, target := range map[string][3]string{
		"otra celda":   {"zz-99", tenant, publicPage},
		"sin empresa":  {testCell, "no-uuid", publicPage},
		"enlace corto": {testCell, tenant, "corto"},
	} {
		if _, err := h.svc.PublicBooking(ctx, target[0], target[1], target[2], w); !errors.Is(err, domain.ErrResourceNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// La trampa para robots: responde como si nada y no reserva.
	req := domain.BookingRequest{Start: "2026-10-01T15:00:00Z", Name: "Luis", Email: "luis@cliente.pe", Note: "compra ya", Website: "http://spam"}
	if res, err := h.svc.Book(ctx, testCell, tenant, publicPage, req); err != nil || sched.booked != 0 || res.ConfirmationSent {
		t.Fatalf("trampa: %+v %v", res, err)
	}
	req.Website = ""
	// Un dueno que no es de esta celda no reserva nada.
	h.directory.ids = []string{"otro@empresa.pe"}
	if _, err := h.svc.Book(ctx, testCell, tenant, publicPage, req); !errors.Is(err, domain.ErrResourceNotFound) || sched.booked != 0 {
		t.Fatalf("dueno de otra celda: %v", err)
	}
	h.directory.ids = []string{testUser}
	sched.confirm = domain.BookingConfirmation{EventID: "ev", Title: "Demo", Start: "2026-10-01T15:00:00Z", End: "2026-10-01T15:30:00Z", TimeZone: "America/Lima",
		Owner: domain.Party{Email: testUser, Name: "Ana Perez"}, Invitation: domain.ITIPMessage{Method: domain.MethodRequest, ICal: "BEGIN:VCALENDAR", Recipients: []string{"luis@cliente.pe"}}}
	res, err := h.svc.Book(ctx, testCell, tenant, publicPage, req)
	if err != nil || !res.ConfirmationSent || sched.booked != 1 || res.OwnerName != "Ana Perez" {
		t.Fatalf("reserva: %+v %v", res, err)
	}
	mail := composer.mails[len(composer.mails)-1]
	call := h.sender.calls[len(h.sender.calls)-1]
	if call.username != testUser || call.from != testUser || len(call.rcpts) != 2 || mail.Cc[0].Email != testUser ||
		strings.Contains(mail.Text, "compra ya") || strings.Contains(mail.Text, "Luis") || mail.Subject != "Cita confirmada: Demo" {
		t.Fatalf("confirmacion: %+v %+v", mail, call)
	}
	if _, err := h.svc.Book(ctx, testCell, tenant, publicPage, domain.BookingRequest{Start: "manana"}); err == nil {
		t.Fatal("una hora ilegible")
	}
	sched.err = &domain.ServiceRejection{Kind: domain.RejectConflict, Code: "SLOT_UNAVAILABLE", Message: "ocupado"}
	var rejection *domain.ServiceRejection
	if _, err := h.svc.Book(ctx, testCell, tenant, publicPage, req); !errors.As(err, &rejection) || rejection.Kind != domain.RejectConflict {
		t.Fatalf("hueco ocupado: %v", err)
	}
}
