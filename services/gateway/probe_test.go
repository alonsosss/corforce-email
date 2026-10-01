package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

type probeBus struct {
	mu     sync.Mutex
	events []events.Event
}

func (b *probeBus) Publish(_ string, evt events.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, evt)
	return nil
}
func (b *probeBus) PublishPersistent(subject string, evt events.Event) error {
	return b.Publish(subject, evt)
}
func (b *probeBus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}

type probeHarness struct {
	guard *probeGuard
	bus   *probeBus
	clock time.Time
	srv   http.Handler
}

// newProbeHarness monta el guardia como en main.go: externo en /api/v1 (tambien ve los 404 de
// rutas inexistentes) e interno dentro de un grupo que simula la autenticacion.
func newProbeHarness(mode string) *probeHarness {
	h := &probeHarness{bus: &probeBus{}, clock: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	now := func() time.Time { return h.clock }
	cfg := probeSettings{Mode: mode, TokenThreshold: 5, IPThreshold: 8, Window: 5 * time.Minute, BlockFor: 15 * time.Minute}
	h.guard = &probeGuard{cfg: cfg, memory: newMemoryProbeStore(now), bus: h.bus, logger: zap.NewNop(), now: now}
	h.guard.store = h.guard.memory

	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(h.guard.middleware)
		r.Post("/auth/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
		r.Group(func(r chi.Router) {
			// Simula jwtAuth: un bearer "ok-<user>" identifica al usuario de la empresa t1.
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					tok := bearerToken(req)
					if tok == "" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					ctx := middleware.WithIdentity(req.Context(), tok, "t1")
					next.ServeHTTP(w, req.WithContext(ctx))
				})
			})
			r.Use(h.guard.identify)
			r.Get("/contacts", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
			r.Get("/contacts/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
			r.Delete("/contacts", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
		})
	})
	h.srv = r
	return h
}

func (h *probeHarness) do(method, path, token, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req = req.WithContext(context.WithValue(req.Context(), middleware.CtxClientIP, ip))
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

func TestSondeoConTokenBloqueaAlUmbralYAvisaUnaVez(t *testing.T) {
	h := newProbeHarness(probeModeEnforce)
	before := testutil.ToFloat64(probeBlocks.WithLabelValues("token", probeModeEnforce))

	// Uso normal: 200 no cuenta, por muchos que sean.
	for i := 0; i < 50; i++ {
		if rec := h.do(http.MethodGet, "/api/v1/contacts", "ok-ana", "203.0.113.7"); rec.Code != http.StatusOK {
			t.Fatalf("peticion %d: %d", i, rec.Code)
		}
	}
	// Sondeo: rutas inexistentes (404 de chi, fuera de los grupos), ids que no existen y un 403.
	paths := []string{"/api/v1/admin", "/api/v1/contacts/nope", "/api/v1/.env", "/api/v1/contacts/x"}
	for _, p := range paths {
		if rec := h.do(http.MethodGet, p, "ok-ana", "203.0.113.7"); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("%s: bloqueado antes del umbral", p)
		}
	}
	rec := h.do(http.MethodDelete, "/api/v1/contacts", "ok-ana", "203.0.113.7")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("el quinto fallo se sirve (403), el bloqueo empieza despues: %d", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/v1/contacts", "ok-ana", "203.0.113.7")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !containsCode(rec, "PROBE_DETECTED") {
		t.Fatalf("tras el umbral, 429 PROBE_DETECTED con Retry-After: %d %s %q", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
	}
	if testutil.ToFloat64(probeBlocks.WithLabelValues("token", probeModeEnforce))-before != 1 {
		t.Fatal("un bloqueo por identidad y ventana")
	}
	// El evento lleva la empresa y el usuario verificados por el grupo autenticado.
	waitFor(t, func() bool { return h.bus.count() == 1 })
	evt := h.bus.events[0]
	if evt.Type != "user.probe_detected" || evt.TenantID != "t1" || evt.UserID != "ok-ana" {
		t.Fatalf("evento: %+v", evt)
	}
	// Otro usuario desde la misma IP sigue entrando: el bloqueo es del token, no de la oficina.
	if rec := h.do(http.MethodGet, "/api/v1/contacts", "ok-luis", "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("otro token en la misma IP: %d", rec.Code)
	}
	// Pasado el bloqueo, vuelve a entrar.
	h.clock = h.clock.Add(16 * time.Minute)
	if rec := h.do(http.MethodGet, "/api/v1/contacts", "ok-ana", "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("tras el bloqueo: %d", rec.Code)
	}
}

func TestRenovarElTokenNoEsquivaElConteoPorIP(t *testing.T) {
	h := newProbeHarness(probeModeEnforce)
	// Ocho fallos con ocho tokens distintos (renovados) desde la misma IP: ninguno llega al umbral
	// de token (5) pero la IP llega al suyo (8).
	for i := 0; i < 8; i++ {
		tok := "ok-" + string(rune('a'+i))
		if rec := h.do(http.MethodGet, "/api/v1/nada", tok, "198.51.100.9"); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("fallo %d bloqueado antes de tiempo", i)
		}
	}
	if rec := h.do(http.MethodGet, "/api/v1/contacts", "ok-z", "198.51.100.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("la IP queda bloqueada aunque cambie el token: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/v1/contacts", "ok-z", "198.51.100.10"); rec.Code != http.StatusOK {
		t.Fatalf("otra IP no se ve afectada: %d", rec.Code)
	}
}

func TestElInicioDeSesionNoCuentaYObserveNoBloquea(t *testing.T) {
	h := newProbeHarness(probeModeEnforce)
	for i := 0; i < 20; i++ {
		h.do(http.MethodPost, "/api/v1/auth/login", "", "203.0.113.7")
	}
	if rec := h.do(http.MethodGet, "/api/v1/contacts", "ok-ana", "203.0.113.7"); rec.Code != http.StatusOK {
		t.Fatalf("los 401 del login tienen su propio cupo y no son sondeo: %d", rec.Code)
	}

	o := newProbeHarness(probeModeObserve)
	before := testutil.ToFloat64(probeBlocks.WithLabelValues("token", probeModeObserve))
	for i := 0; i < 10; i++ {
		if rec := o.do(http.MethodGet, "/api/v1/nada", "ok-ana", "203.0.113.7"); rec.Code != http.StatusNotFound {
			t.Fatalf("en observe nunca se bloquea: %d", rec.Code)
		}
	}
	if testutil.ToFloat64(probeBlocks.WithLabelValues("token", probeModeObserve))-before != 1 {
		t.Fatal("en observe se anota el umbral una vez por ventana")
	}
}

func TestLaVentanaDeFallosSeReinicia(t *testing.T) {
	h := newProbeHarness(probeModeEnforce)
	for i := 0; i < 4; i++ {
		h.do(http.MethodGet, "/api/v1/nada", "ok-ana", "203.0.113.7")
	}
	h.clock = h.clock.Add(6 * time.Minute)
	for i := 0; i < 4; i++ {
		if rec := h.do(http.MethodGet, "/api/v1/nada", "ok-ana", "203.0.113.7"); rec.Code != http.StatusNotFound {
			t.Fatalf("cuatro fallos en otra ventana no suman con los de antes: %d", rec.Code)
		}
	}
}

func containsCode(rec *httptest.ResponseRecorder, code string) bool {
	return rec.Body != nil && len(rec.Body.String()) > 0 && stringsContains(rec.Body.String(), code)
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condicion no alcanzada a tiempo")
}

// TestElGuardiaNoRetieneUnFlujoSSE: el guardia envuelve toda respuesta de /api/v1 para leer su
// codigo; el proxy tiene que poder seguir vaciando el bufer, o los avisos del webmail no llegan
// hasta que el servicio cierra el flujo.
func TestElGuardiaNoRetieneUnFlujoSSE(t *testing.T) {
	liberar := make(chan struct{})
	defer close(liberar)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: ready\ndata: {}\n\n"))
		http.NewResponseController(w).Flush()
		select {
		case <-liberar:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()

	h := newProbeHarness(probeModeEnforce)
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(h.guard.middleware)
		r.Handle("/webmail/events", reverseProxy(upstream.URL, "token-interno"))
	})
	front := httptest.NewServer(r)
	defer front.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/api/v1/webmail/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("peticion: %v", err)
	}
	defer resp.Body.Close()
	linea := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		linea <- line
	}()
	select {
	case got := <-linea:
		if got != "event: ready\n" {
			t.Fatalf("primera linea del flujo = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el evento ready no atraveso el guardia mientras el flujo seguia abierto")
	}
}
