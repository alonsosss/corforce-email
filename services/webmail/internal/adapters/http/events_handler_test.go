package http

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// fakeWatcher entrega por un canal lo que la prueba dispare.
type fakeWatcher struct {
	mu        sync.Mutex
	err       error
	usernames []string
	ch        chan domain.MailboxChange
}

func (f *fakeWatcher) Watch(ctx context.Context, username string) (<-chan domain.MailboxChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.usernames = append(f.usernames, username)
	return f.ch, nil
}

// touchStore cuenta las renovaciones de inactividad: el flujo de avisos no debe hacer ninguna.
type touchStore struct {
	memStore
	touches atomic.Int32
}

func (s *touchStore) Touch(context.Context, string, time.Duration) error {
	s.touches.Add(1)
	return nil
}

type eventsServer struct {
	srv     *httptest.Server
	store   *touchStore
	watcher *fakeWatcher
	cookie  *http.Cookie
}

func newEventsServer(t *testing.T, watcher *fakeWatcher, tune func(*Config)) *eventsServer {
	t.Helper()
	store := &touchStore{memStore: memStore{m: map[string]domain.Session{}}}
	deps := testDeps(store, &stubMailbox{}, nopSender{}, &stubVacations{}, &stubAddressBook{}, newStubSettings(), &stubDAV{})
	if watcher != nil {
		deps.Watcher = watcher
	}
	svc, err := app.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CookieSecure: true, SessionIdle: 30 * time.Minute, SessionMax: 12 * time.Hour,
		MFAChallengeTTL: 5 * time.Minute, IPRateLimiter: unlimited{}, MailboxRateLimiter: unlimited{}, ImageProxyRateLimiter: unlimited{},
		AllowedOrigins: []string{allowedOrigin}, MaxMessageBytes: 4096, OperationTimeout: 5 * time.Second, TransferTimeout: 5 * time.Second,
	}
	if tune != nil {
		tune(&cfg)
	}
	h, err := NewHandler(svc, cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	routes := h.Routes()
	srv := httptest.NewServer(routes)
	t.Cleanup(srv.Close)
	return &eventsServer{srv: srv, store: store, watcher: watcher, cookie: login(t, routes)}
}

func (e *eventsServer) open(t *testing.T, header map[string]string) (*http.Response, *bufio.Scanner) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+BasePath+"/events", nil)
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, bufio.NewScanner(resp.Body)
}

// next lee lineas hasta encontrar una que empiece por prefix; falla si pasa el plazo.
func next(t *testing.T, sc *bufio.Scanner, prefix string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), prefix) {
				done <- sc.Text()
				return
			}
		}
		done <- "<fin del flujo>"
	}()
	select {
	case line := <-done:
		return line
	case <-time.After(3 * time.Second):
		t.Fatalf("sin %q", prefix)
		return ""
	}
}

func TestElFlujoDeAvisosExigeSesionYNoEsDeOtroOrigen(t *testing.T) {
	w := &fakeWatcher{ch: make(chan domain.MailboxChange, 1)}
	e := newEventsServer(t, w, nil)
	e.cookie = nil
	resp, _ := e.open(t, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sin cookie: %d", resp.StatusCode)
	}
	e2 := newEventsServer(t, w, nil)
	resp, _ = e2.open(t, map[string]string{"Origin": "https://malo.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("otro origen: %d", resp.StatusCode)
	}
	resp, _ = e2.open(t, map[string]string{"Origin": allowedOrigin})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("origen permitido: %d", resp.StatusCode)
	}
	if len(w.usernames) != 1 {
		t.Fatalf("solo el permitido vigila: %v", w.usernames)
	}
}

func TestUnAvisoLlegaComoEventoConSusCabecerasParaQueElBordeNoLoAcumule(t *testing.T) {
	w := &fakeWatcher{ch: make(chan domain.MailboxChange, 1)}
	e := newEventsServer(t, w, nil)
	resp, sc := e.open(t, nil)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
		resp.Header.Get("X-Accel-Buffering") != "no" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cabeceras: %d %v", resp.StatusCode, resp.Header)
	}
	next(t, sc, "event: ready")
	if w.usernames[0] != testUser {
		t.Fatalf("el buzon sale de la sesion: %v", w.usernames)
	}
	w.ch <- domain.MailboxChange{Messages: 12}
	next(t, sc, "event: mailbox")
	if data := next(t, sc, "data: "); !strings.Contains(data, `"folder":"INBOX"`) || !strings.Contains(data, `"messages":12`) {
		t.Fatalf("datos: %s", data)
	}
}

func TestElFlujoNoRenuevaLaInactividadDeLaSesion(t *testing.T) {
	w := &fakeWatcher{ch: make(chan domain.MailboxChange, 1)}
	e := newEventsServer(t, w, func(c *Config) { c.EventsSessionCheck = 20 * time.Millisecond })
	before := e.store.touches.Load()
	_, sc := e.open(t, nil)
	next(t, sc, "event: ready")
	time.Sleep(120 * time.Millisecond)
	if got := e.store.touches.Load(); got != before {
		t.Fatalf("abrir y mantener el flujo renovo la inactividad %d veces", got-before)
	}
}

func TestSiLaSesionCaeElFlujoLoAvisaYSeCierra(t *testing.T) {
	w := &fakeWatcher{ch: make(chan domain.MailboxChange, 1)}
	e := newEventsServer(t, w, func(c *Config) { c.EventsSessionCheck = 20 * time.Millisecond })
	_, sc := e.open(t, nil)
	next(t, sc, "event: ready")
	e.store.mu.Lock()
	e.store.m = map[string]domain.Session{}
	e.store.mu.Unlock()
	next(t, sc, "event: session-expired")
	if line := next(t, sc, "never"); line != "<fin del flujo>" {
		t.Fatalf("tras avisar el flujo se cierra: %q", line)
	}
}

func TestElFlujoLatePorSiSolo(t *testing.T) {
	w := &fakeWatcher{ch: make(chan domain.MailboxChange, 1)}
	e := newEventsServer(t, w, func(c *Config) { c.EventsHeartbeat = 20 * time.Millisecond })
	_, sc := e.open(t, nil)
	next(t, sc, ": ping")
}

func TestElFlujoSeCierraSoloAlLlegarASuTopeDeVidaYPideReconectar(t *testing.T) {
	w := &fakeWatcher{ch: make(chan domain.MailboxChange, 1)}
	e := newEventsServer(t, w, func(c *Config) { c.EventsMaxLifetime = 60 * time.Millisecond })
	_, sc := e.open(t, nil)
	next(t, sc, "event: ready")
	next(t, sc, "event: reconnect")
	if line := next(t, sc, "never"); line != "<fin del flujo>" {
		t.Fatalf("%q", line)
	}
}

func TestLosTopesYLosAvisosDesactivadosTienenSuRespuesta(t *testing.T) {
	w := &fakeWatcher{err: domain.ErrTooManyStreams}
	e := newEventsServer(t, w, nil)
	if resp, _ := e.open(t, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("tope: %d", resp.StatusCode)
	}
	off := newEventsServer(t, nil, nil)
	if resp, _ := off.open(t, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("desactivados: %d", resp.StatusCode)
	}
}
