package imap

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// fakeSource cuenta cuantas vigilancias se abren y deja a la prueba disparar avisos y fallos.
type fakeSource struct {
	mu      sync.Mutex
	opened  atomic.Int32
	notify  map[string]func(uint32)
	fail    chan error
	stopped map[string]chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{notify: map[string]func(uint32){}, fail: make(chan error, 4), stopped: map[string]chan struct{}{}}
}

func (f *fakeSource) run(ctx context.Context, username string, notify func(uint32)) error {
	f.opened.Add(1)
	f.mu.Lock()
	f.notify[username] = notify
	stopped := make(chan struct{})
	f.stopped[username] = stopped
	f.mu.Unlock()
	defer close(stopped)
	select {
	case <-ctx.Done():
		return nil
	case err := <-f.fail:
		return err
	}
}

func (f *fakeSource) stoppedOf(username string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped[username]
}

func (f *fakeSource) fire(t *testing.T, username string, messages uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := f.notify[username]
		f.mu.Unlock()
		if n != nil {
			n(messages)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("la vigilancia no arranco")
}

func testWatcher(src *fakeSource, cfg WatchConfig) *Watcher {
	w := newWatcher(src.run, cfg, zap.NewNop())
	w.grace, w.retryMin = 40*time.Millisecond, 10*time.Millisecond
	return w
}

func recv(t *testing.T, ch <-chan domain.MailboxChange) domain.MailboxChange {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("canal cerrado")
		}
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("sin aviso")
		return domain.MailboxChange{}
	}
}

func TestVariosSuscritosDelMismoBuzonComparteUnaSolaSesionImap(t *testing.T) {
	src := newFakeSource()
	w := testWatcher(src, WatchConfig{MaxPerMailbox: 5, MaxMailboxes: 10})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := w.Watch(ctx, "ana@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.Watch(ctx, "ana@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	src.fire(t, "ana@acme.test", 7)
	if got := recv(t, a); got.Messages != 7 {
		t.Fatalf("a: %+v", got)
	}
	if got := recv(t, b); got.Messages != 7 {
		t.Fatalf("b: %+v", got)
	}
	if n := src.opened.Load(); n != 1 {
		t.Fatalf("una sesion IMAP por buzon, no por suscrito: %d", n)
	}
}

func TestLosAvisosQueLlegaJuntosSeFundenEnUnoYUnLectorLentoNoBloquea(t *testing.T) {
	src := newFakeSource()
	w := testWatcher(src, WatchConfig{MaxPerMailbox: 5, MaxMailboxes: 10})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := w.Watch(ctx, "ana@acme.test")
	src.fire(t, "ana@acme.test", 1)
	src.fire(t, "ana@acme.test", 2)
	src.fire(t, "ana@acme.test", 3)
	if got := recv(t, ch); got.Messages != 1 {
		t.Fatalf("el primero espera y los demas se descartan: %+v", got)
	}
	select {
	case extra := <-ch:
		t.Fatalf("no debe haber mas avisos pendientes: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLosTopesPorBuzonYPorProcesoSeRechazanConSuError(t *testing.T) {
	src := newFakeSource()
	w := testWatcher(src, WatchConfig{MaxPerMailbox: 2, MaxMailboxes: 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := w.Watch(ctx, "ana@acme.test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Watch(ctx, "ana@acme.test"); !errors.Is(err, domain.ErrTooManyStreams) {
		t.Fatalf("tercera de un buzon: %v", err)
	}
	if _, err := w.Watch(ctx, "bea@acme.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Watch(ctx, "carla@acme.test"); !errors.Is(err, domain.ErrTooManyStreams) {
		t.Fatalf("tercer buzon del proceso: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := src.opened.Load(); n != 2 {
		t.Fatalf("un rechazo no abre sesiones: %d", n)
	}
}

func TestSinNadieSuscritoLaSesionSeCierraTrasElMargenYUnaReconexionRapidaLaConserva(t *testing.T) {
	src := newFakeSource()
	w := testWatcher(src, WatchConfig{MaxPerMailbox: 5, MaxMailboxes: 10})
	ctx1, cancel1 := context.WithCancel(context.Background())
	if _, err := w.Watch(ctx1, "ana@acme.test"); err != nil {
		t.Fatal(err)
	}
	cancel1()
	// Reconecta dentro del margen: sigue la misma sesion.
	time.Sleep(10 * time.Millisecond)
	ctx2, cancel2 := context.WithCancel(context.Background())
	if _, err := w.Watch(ctx2, "ana@acme.test"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if n := src.opened.Load(); n != 1 {
		t.Fatalf("reconexion rapida: sesiones abiertas %d", n)
	}
	select {
	case <-src.stoppedOf("ana@acme.test"):
		t.Fatal("con un suscrito la vigilancia no se detiene")
	default:
	}
	cancel2()
	select {
	case <-src.stoppedOf("ana@acme.test"):
	case <-time.After(2 * time.Second):
		t.Fatal("sin suscritos la vigilancia debe cerrarse tras el margen")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.watches) != 0 {
		t.Fatalf("queda registrada: %d", len(w.watches))
	}
}

func TestSiLaSesionImapCaeSeReabreYLosSuscritosSiguenRecibiendo(t *testing.T) {
	src := newFakeSource()
	w := testWatcher(src, WatchConfig{MaxPerMailbox: 5, MaxMailboxes: 10})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := w.Watch(ctx, "ana@acme.test")
	src.fire(t, "ana@acme.test", 1)
	recv(t, ch)

	src.fail <- errors.New("conexion perdida")
	deadline := time.Now().Add(2 * time.Second)
	for src.opened.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if src.opened.Load() < 2 {
		t.Fatal("no reconecto")
	}
	src.fire(t, "ana@acme.test", 9)
	if got := recv(t, ch); got.Messages != 9 {
		t.Fatalf("tras reconectar: %+v", got)
	}
}

func TestCancelarElContextoCierraElCanalDelSuscrito(t *testing.T) {
	src := newFakeSource()
	w := testWatcher(src, WatchConfig{MaxPerMailbox: 5, MaxMailboxes: 10})
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := w.Watch(ctx, "ana@acme.test")
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("no debe haber avisos")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el canal debe cerrarse")
	}
}
