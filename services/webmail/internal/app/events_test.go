package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type watcherFalso struct {
	err      error
	username string
	ch       chan domain.MailboxChange
}

func (w *watcherFalso) Watch(_ context.Context, username string) (<-chan domain.MailboxChange, error) {
	w.username = username
	return w.ch, w.err
}

func servicioConWatcher(t *testing.T, w *watcherFalso) (*Service, domain.Session, string) {
	t.Helper()
	h := newHarness(t)
	token, sess := h.login(t)
	if w == nil {
		return h.svc, sess, token
	}
	svc, err := New(Deps{
		Auth: h.auth, Sessions: h.store, Mail: h.mail, Sender: h.sender, Directory: h.directory, Vacations: h.directory,
		AddressBook: h.directory, Ledger: h.ledger, Composer: h.composer, Sanitizer: h.sanitizer, Scanner: h.scanner,
		PartURL: func(string, uint32, string) string { return "" }, Clock: h.clock.Now, Logger: h.svc.logger, Config: h.svc.cfg,
		Watcher: w,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, sess, token
}

func TestSinVigilanteLosAvisosEstanDesactivados(t *testing.T) {
	svc, sess, _ := servicioConWatcher(t, nil)
	if _, err := svc.WatchInbox(context.Background(), sess); !errors.Is(err, domain.ErrEventsDisabled) {
		t.Fatalf("%v", err)
	}
}

func TestLaVigilanciaEsDelBuzonDeLaSesionYSusErroresSeTraducen(t *testing.T) {
	w := &watcherFalso{ch: make(chan domain.MailboxChange)}
	svc, sess, _ := servicioConWatcher(t, w)
	if _, err := svc.WatchInbox(context.Background(), sess); err != nil || w.username != testUser {
		t.Fatalf("%v %q", err, w.username)
	}
	w.err = domain.ErrTooManyStreams
	if _, err := svc.WatchInbox(context.Background(), sess); !errors.Is(err, domain.ErrTooManyStreams) {
		t.Fatalf("un tope llega tal cual: %v", err)
	}
	w.err = errors.New("dovecot caido")
	if _, err := svc.WatchInbox(context.Background(), sess); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("un fallo es una dependencia no disponible: %v", err)
	}
}

func TestComprobarLaSesionSinRenovarNoTocaLaInactividad(t *testing.T) {
	h := newHarness(t)
	token, _ := h.login(t)
	before := len(h.store.touches)
	if _, err := h.svc.PeekSession(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if len(h.store.touches) != before {
		t.Fatalf("PeekSession renovo la inactividad")
	}
	if _, err := h.svc.Authenticate(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if len(h.store.touches) != before+1 {
		t.Fatalf("Authenticate si la renueva: %d", len(h.store.touches)-before)
	}
}
