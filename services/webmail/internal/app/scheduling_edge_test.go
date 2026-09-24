package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// Sin la planificacion cableada sus operaciones responden como un servicio que no esta, sin tocar nada.
func TestSinPlanificacionCableadaNadaResponde(t *testing.T) {
	h := newHarness(t)
	sess := schedSession()
	ctx := context.Background()
	tenant := publicTenant(sess)
	w, _ := domain.NewEventWindow("2026-10-01T00:00:00Z", "2026-10-08T00:00:00Z")
	for name, call := range map[string]func() error{
		"pagina": func() error { _, err := h.svc.BookingSettings(ctx, sess); return err },
		"guardar pagina": func() error {
			_, err := h.svc.SaveBookingSettings(ctx, sess, domain.BookingSettings{Title: "Demo"}, false)
			return err
		},
		"aparicion": func() error {
			_, err := h.svc.UpdateOccurrence(ctx, sess, "e1", "2026-10-02T15:00:00Z", domain.EventInput{}, "", true)
			return err
		},
		"pagina publica": func() error { _, err := h.svc.PublicBooking(ctx, testCell, tenant, publicPage, w); return err },
		"reservar": func() error {
			_, err := h.svc.Book(ctx, testCell, tenant, publicPage, domain.BookingRequest{Start: "2026-10-01T15:00:00Z", Name: "Luis", Email: "luis@cliente.pe"})
			return err
		},
	} {
		if err := call(); !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Editar una aparicion no cambia los invitados ni el organizador de la serie: lo que mande el cliente se descarta y,
// sin reunion, no sale ninguna invitacion.
func TestUnaAparicionNoCambiaLaReunion(t *testing.T) {
	h, sched, composer := newSchedulingHarness(t)
	sess := schedSession()
	ctx := context.Background()
	in := domain.EventInput{Title: "Solo hoy", Start: "2026-10-02T17:00:00Z", End: "2026-10-02T17:30:00Z",
		Organizer: &domain.Party{Email: "mallory@empresa.pe"}, Attendees: []domain.Attendee{{Email: "eva@empresa.pe"}}}
	saved, err := h.svc.UpdateOccurrence(ctx, sess, "e1", "2026-10-02T15:00:00Z", in, "", true)
	if err != nil || saved.Organizer != nil || len(saved.Attendees) != 0 || saved.Delivery != nil || len(composer.mails) != 0 {
		t.Fatalf("aparicion: %+v %v", saved, err)
	}
	if sched.occurrences[0] != "put:e1@2026-10-02T15:00:00Z" || sched.mb.Address != testUser {
		t.Fatalf("mail-dav recibio %v como %+v", sched.occurrences, sched.mb)
	}
	var verr *domain.ValidationError
	if _, err := h.svc.DeleteOccurrence(ctx, sess, "../e1", "2026-10-02T15:00:00Z", "", true); !errors.As(err, &verr) {
		t.Fatalf("identificador invalido: %v", err)
	}
	sched.err = &domain.ServiceRejection{Kind: domain.RejectPrecondition, Code: "PRECONDITION_FAILED", Message: "cambio"}
	var rejection *domain.ServiceRejection
	if _, err := h.svc.DeleteOccurrence(ctx, sess, "e1", "2026-10-02T15:00:00Z", "", true); !errors.As(err, &rejection) || rejection.Kind != domain.RejectPrecondition {
		t.Fatalf("etag viejo: %v", err)
	}
}

// Lo que falla por dentro al reservar sale como indisponible, sin detalles para el visitante; lo que es suyo
// (un hueco ocupado, un tope) sale tal cual. Un dueno sin direccion valida es una pagina que no existe.
func TestErroresDeLaPaginaPublica(t *testing.T) {
	h, sched, _ := newSchedulingHarness(t)
	sess := schedSession()
	ctx := context.Background()
	tenant := publicTenant(sess)
	req := domain.BookingRequest{Start: "2026-10-01T15:00:00Z", Name: "Luis", Email: "luis@cliente.pe"}

	sched.page = domain.PublicBookingPage{Title: "Demo", OwnerName: "Ana Perez", OwnerAddress: "no es"}
	if _, err := h.svc.Book(ctx, testCell, tenant, publicPage, req); !errors.Is(err, domain.ErrResourceNotFound) || sched.booked != 0 {
		t.Fatalf("dueno sin direccion: %v", err)
	}
	sched.page.OwnerAddress = testUser
	h.directory.err = errors.New("directorio caido")
	if _, err := h.svc.Book(ctx, testCell, tenant, publicPage, req); !errors.Is(err, domain.ErrUnavailable) || sched.booked != 0 {
		t.Fatalf("directorio caido: %v", err)
	}
	h.directory.err = nil
	h.directory.ids = []string{testUser}

	sched.err = &domain.ServiceRejection{Kind: domain.RejectUnavailable, Code: "SERVICE_UNAVAILABLE", Message: "mail-dav"}
	w, _ := domain.NewEventWindow("2026-10-01T00:00:00Z", "2026-10-08T00:00:00Z")
	if _, err := h.svc.PublicBooking(ctx, testCell, tenant, publicPage, w); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("mail-dav no disponible: %v", err)
	}
	sched.err = &domain.ServiceRejection{Kind: domain.RejectRateLimited, Code: "LIMIT_EXCEEDED", Message: "tope"}
	var rejection *domain.ServiceRejection
	if _, err := h.svc.PublicBooking(ctx, testCell, tenant, publicPage, w); !errors.As(err, &rejection) || rejection.Kind != domain.RejectRateLimited {
		t.Fatalf("tope de reservas: %v", err)
	}
	sched.err = &domain.ServiceRejection{Kind: domain.RejectNotFound, Code: "NOT_FOUND", Message: "no existe"}
	if _, err := h.svc.BookingSettings(ctx, sess); !errors.As(err, &rejection) || rejection.Kind != domain.RejectNotFound {
		t.Fatalf("sin pagina: %v", err)
	}
}
