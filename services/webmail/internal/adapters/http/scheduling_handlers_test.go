package http

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

type stubScheduling struct {
	booked int
	rid    string
}

func (s *stubScheduling) UpdateOccurrence(_ context.Context, _ domain.MailboxRef, id, rid string, in domain.EventInput, _ string) (domain.Event, error) {
	s.rid = rid
	return domain.Event{ID: id, ETag: `"v2"`, EventInput: in}, nil
}
func (s *stubScheduling) DeleteOccurrence(_ context.Context, _ domain.MailboxRef, id, rid, _ string) (domain.Event, error) {
	s.rid = rid
	return domain.Event{ID: id, ETag: `"v3"`}, nil
}
func (s *stubScheduling) EventInvitation(context.Context, domain.MailboxRef, string, string) (domain.ITIPMessage, error) {
	return domain.ITIPMessage{}, nil
}
func (s *stubScheduling) InspectInvitation(context.Context, domain.MailboxRef, string, []string) (domain.Invitation, error) {
	return domain.Invitation{}, nil
}
func (s *stubScheduling) RespondInvitation(context.Context, domain.MailboxRef, string, []string, string) (domain.InvitationAnswer, error) {
	return domain.InvitationAnswer{}, nil
}
func (s *stubScheduling) ApplyInvitation(context.Context, domain.MailboxRef, string, string, []string) (domain.InvitationApplied, error) {
	return domain.InvitationApplied{}, nil
}
func (s *stubScheduling) Availability(_ context.Context, _ domain.MailboxRef, addresses []string, _ domain.EventWindow) ([]domain.MailboxAvailability, error) {
	return []domain.MailboxAvailability{{Address: addresses[0], Known: true, Busy: []domain.BusyInterval{{Start: "2026-10-01T15:00:00Z", End: "2026-10-01T16:00:00Z"}}}}, nil
}
func (s *stubScheduling) BookingSettings(context.Context, domain.MailboxRef) (domain.BookingPage, error) {
	return domain.BookingPage{}, &domain.ServiceRejection{Kind: domain.RejectNotFound, Code: "NOT_FOUND", Message: "no existe"}
}
func (s *stubScheduling) SaveBookingSettings(_ context.Context, _ domain.MailboxRef, in domain.BookingSettings, _ string, _ bool) (domain.BookingPage, error) {
	return domain.BookingPage{BookingSettings: in, PublicID: "enlace-publico-de-prueba-01"}, nil
}
func (s *stubScheduling) PublicBooking(context.Context, string, string, domain.EventWindow) (domain.PublicBookingPage, error) {
	return domain.PublicBookingPage{Title: "Demo", OwnerName: "Ventas", OwnerAddress: "ventas@empresa.pe",
		Slots: []domain.BusyInterval{{Start: "2026-10-01T15:00:00Z", End: "2026-10-01T15:30:00Z"}}}, nil
}
func (s *stubScheduling) Book(context.Context, string, string, domain.BookingRequest) (domain.BookingConfirmation, error) {
	s.booked++
	return domain.BookingConfirmation{Title: "Demo", Start: "2026-10-01T15:00:00Z", End: "2026-10-01T15:30:00Z",
		Owner:      domain.Party{Email: "ventas@empresa.pe"},
		Invitation: domain.ITIPMessage{Method: "REQUEST", ICal: "BEGIN:VCALENDAR", Recipients: []string{"luis@cliente.pe"}}}, nil
}

type stubInvitations struct{}

func (stubInvitations) ComposeInvitation(domain.InvitationMail) ([]byte, error) {
	return []byte("x"), nil
}

func newSchedulingEnv(t *testing.T) (*testEnv, *stubScheduling) {
	t.Helper()
	env := &testEnv{mb: &stubMailbox{}, vac: &stubVacations{}, book: &stubAddressBook{}, settings: newStubSettings(), dav: &stubDAV{}}
	sched := &stubScheduling{}
	deps := testDeps(&memStore{m: map[string]domain.Session{}}, env.mb, nopSender{}, env.vac, env.book, env.settings, env.dav)
	deps.Scheduling, deps.Invitations = sched, stubInvitations{}
	svc, err := app.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(svc, Config{
		CookieSecure: true, SessionIdle: 30 * time.Minute, SessionMax: 12 * time.Hour,
		MFAChallengeTTL: 5 * time.Minute, IPRateLimiter: unlimited{}, MailboxRateLimiter: unlimited{}, ImageProxyRateLimiter: unlimited{},
		AllowedOrigins: []string{allowedOrigin}, MaxMessageBytes: 4096, OperationTimeout: 5 * time.Second, TransferTimeout: 5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	env.h = h.Routes()
	return env, sched
}

const publicTenant = "11111111-1111-4111-8111-111111111111"

func TestPaginaPublicaDeCitasSinSesion(t *testing.T) {
	env, sched := newSchedulingEnv(t)
	base := PublicBookingPath + "/" + testCell + "/" + publicTenant + "/enlace-publico-de-prueba-01"
	rec := do(env.h, http.MethodGet, base+"?start=2026-10-01T00:00:00Z&end=2026-10-08T00:00:00Z", nil, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"slots":[{"start":"2026-10-01T15:00:00Z"`) {
		t.Fatalf("pagina: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "ventas@empresa.pe") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("la pagina publica no expone al dueno y no se cachea: %s", rec.Body)
	}
	if rec = do(env.h, http.MethodGet, PublicBookingPath+"/zz-99/"+publicTenant+"/enlace-publico-de-prueba-01?start=2026-10-01T00:00:00Z&end=2026-10-08T00:00:00Z", nil, nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("otra celda: %d %s", rec.Code, rec.Body)
	}
	reserva := `{"start":"2026-10-01T15:00:00Z","name":"Luis","email":"luis@cliente.pe","note":"hola"}`
	if rec = do(env.h, http.MethodPost, base, body(reserva), map[string]string{"Content-Type": "application/json", "Origin": "https://otro.example"}, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("otro origen: %d", rec.Code)
	}
	rec = do(env.h, http.MethodPost, base, body(reserva), jsonOrigin, nil)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"confirmation_sent":true`) || sched.booked != 1 {
		t.Fatalf("reserva: %d %s", rec.Code, rec.Body)
	}
	trap := `{"start":"2026-10-01T15:00:00Z","name":"x","email":"x@y.pe","website":"http://spam"}`
	if rec = do(env.h, http.MethodPost, base, body(trap), jsonOrigin, nil); rec.Code != http.StatusCreated || sched.booked != 1 {
		t.Fatalf("trampa: %d %d", rec.Code, sched.booked)
	}
	if rec = do(env.h, http.MethodPost, base, body(strings.Repeat("x", maxBookingBody+1)), jsonOrigin, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("cuerpo excesivo: %d", rec.Code)
	}
}

func TestRutasDePlanificacionConSesion(t *testing.T) {
	env, sched := newSchedulingEnv(t)
	if rec := do(env.h, http.MethodGet, BasePath+"/availability?addresses=a@empresa.pe&start=2026-10-01T00:00:00Z&end=2026-10-08T00:00:00Z", nil, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin sesion: %d", rec.Code)
	}
	cookie := login(t, env.h)
	rec := do(env.h, http.MethodGet, BasePath+"/availability?addresses=a@empresa.pe&start=2026-10-01T00:00:00Z&end=2026-10-08T00:00:00Z", nil, nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"busy":[{"start":"2026-10-01T15:00:00Z","end":"2026-10-01T16:00:00Z"}]`) {
		t.Fatalf("disponibilidad: %d %s", rec.Code, rec.Body)
	}
	rid := url.PathEscape("2026-10-02T15:00:00Z")
	rec = do(env.h, http.MethodDelete, BasePath+"/calendar/events/e1/occurrences/"+rid, nil, jsonOrigin, cookie)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"v3"` || sched.rid != "2026-10-02T15:00:00Z" {
		t.Fatalf("borrar aparicion: %d %s %q", rec.Code, rec.Body, sched.rid)
	}
	rec = do(env.h, http.MethodGet, BasePath+"/booking", nil, nil, cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("sin pagina de citas: %d %s", rec.Code, rec.Body)
	}
	rec = do(env.h, http.MethodPut, BasePath+"/booking", body(`{"title":"Demo","weekly":{"MO":[{"start":"09:00","end":"12:00"}]}}`), jsonOrigin, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cell":"`+testCell+`"`) || !strings.Contains(rec.Body.String(), `"public_id":"enlace-publico-de-prueba-01"`) {
		t.Fatalf("guardar pagina: %d %s", rec.Code, rec.Body)
	}
}
