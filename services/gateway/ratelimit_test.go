package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// memStore es un almacen compartido en proceso con la semantica del script de Redis.
type memStore struct {
	mu     sync.Mutex
	counts map[string]int64
	ends   map[string]time.Time
	keys   map[string]bool
}

func (s *memStore) Hit(_ context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if end, ok := s.ends[key]; !ok || !now.Before(end) {
		s.counts[key] = 0
		s.ends[key] = now.Add(window)
	}
	s.counts[key]++
	s.keys[key] = true
	return s.counts[key], s.ends[key].Sub(now), nil
}

// Dos replicas del gateway con el mismo almacen: el inicio de sesion de la plataforma en
// una y el del webmail en la otra consumen el mismo cupo estricto de la IP, que el cliente
// no reinicia rotando X-Real-IP ni X-Forwarded-For.
func TestLimitesCompartidosEntreReplicas(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer upstream.Close()

	store := &memStore{counts: map[string]int64{}, ends: map[string]time.Time{}, keys: map[string]bool{}}
	replica := func() http.Handler {
		api, auth := newRateLimiters(store, 600, 3, zap.NewNop())
		r := chi.NewRouter()
		r.Use(middleware.StripInternalHeaders)
		r.Use(middleware.CaptureClientIP(middleware.TrustedProxyCIDRs("")))
		r.Route("/api/v1", func(r chi.Router) {
			r.Use(api.Limit)
			r.With(auth.Limit).Post("/auth/login", func(w http.ResponseWriter, _ *http.Request) {})
			mountSelfAuthenticated(r,
				[]selfAuthSpec{{Prefix: "webmail", Service: "webmail", StrictLimit: []methodPathSpec{{Method: "POST", Path: "/session"}}}},
				func(string) string { return upstream.URL }, auth.Limit, "token-interno")
		})
		return r
	}
	a, b := replica(), replica()

	do := func(h http.Handler, path string, i int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = "203.0.113.9:5000"
		req.Header.Set("X-Real-IP", "10.1.1."+strconv.Itoa(i+1))
		req.Header.Set("X-Forwarded-For", "192.0.2."+strconv.Itoa(i+1))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for i, step := range []struct {
		h    http.Handler
		path string
	}{{a, "/api/v1/auth/login"}, {b, "/api/v1/webmail/session"}, {a, "/api/v1/auth/login"}} {
		if rec := do(step.h, step.path, i); rec.Code != http.StatusOK {
			t.Fatalf("intento %d en %s: %d", i+1, step.path, rec.Code)
		}
	}
	rec := do(b, "/api/v1/webmail/session", 3)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("la segunda replica concedio un cupo propio: %d", rec.Code)
	}
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 60 {
		t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
	}

	want := map[string]bool{"rl:gateway:api:ip:203.0.113.9": true, "rl:gateway:auth:ip:203.0.113.9": true}
	for k := range store.keys {
		if !want[k] {
			t.Fatalf("clave inesperada %q: la eligio una cabecera del cliente", k)
		}
	}
}
