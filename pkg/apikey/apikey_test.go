package apikey

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const token = "cfm_abcdefgh2345_secreto"

type accessControl struct {
	calls    atomic.Int32
	status   atomic.Int32
	body     string
	lastIP   string
	lastTok  string
	mu       sync.Mutex
	tokenHdr string
}

func (a *accessControl) server(t *testing.T) *httptest.Server {
	t.Helper()
	a.status.Store(http.StatusOK)
	if a.body == "" {
		a.body = `{"data":{"id":"k1","tenant_id":"t1","prefix":"abcdefgh2345","scopes":[{"module":"transactional","resource":"messages","action":"create"}]}}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.calls.Add(1)
		var req resolveRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		a.mu.Lock()
		a.lastIP, a.lastTok, a.tokenHdr = req.ClientIP, req.Token, r.Header.Get("X-Gateway-Token")
		a.mu.Unlock()
		if r.URL.Path != resolvePath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(int(a.status.Load()))
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fakeRevocations struct {
	revoked map[string]bool
	err     error
}

func (f *fakeRevocations) IsRevoked(_ context.Context, id string) (bool, error) {
	return f.revoked[id], f.err
}

func TestResuelveYCachea(t *testing.T) {
	ac := &accessControl{}
	srv := ac.server(t)
	revs := &fakeRevocations{revoked: map[string]bool{}}
	r := NewResolver(srv.URL, "token-interno", time.Minute, revs)
	p, err := r.Resolve(context.Background(), token, "198.51.100.7")
	if err != nil || p.ID != "k1" || p.TenantID != "t1" || !p.Allows("transactional", "messages", "create") || p.Allows("transactional", "messages", "read") {
		t.Fatalf("resuelta: %+v %v", p, err)
	}
	if ac.lastIP != "198.51.100.7" || ac.lastTok != token || ac.tokenHdr != "token-interno" {
		t.Fatalf("peticion: %q %q %q", ac.lastIP, ac.lastTok, ac.tokenHdr)
	}
	if _, err := r.Resolve(context.Background(), token, ""); err != nil || ac.calls.Load() != 1 {
		t.Fatalf("la segunda sale de la cache: %d %v", ac.calls.Load(), err)
	}

	// La marca de revocacion vence a la cache sin esperar a su caducidad.
	revs.revoked["k1"] = true
	if _, err := r.Resolve(context.Background(), token, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revocada: %v", err)
	}
	if !r.IsRevoked(context.Background(), "k1") {
		t.Fatal("IsRevoked lee la marca")
	}

	// Con Redis caido se confia en la cache, que es corta.
	revs2 := &fakeRevocations{err: errors.New("redis caido")}
	r2 := NewResolver(srv.URL, "", time.Minute, revs2)
	if _, err := r2.Resolve(context.Background(), token, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Resolve(context.Background(), token, ""); err != nil {
		t.Fatalf("sin Redis: %v", err)
	}
}

func TestCacheVenceYRespetaLaCaducidad(t *testing.T) {
	ac := &accessControl{}
	expires := time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339)
	ac.body = `{"data":{"id":"k1","tenant_id":"t1","scopes":[{"module":"transactional","resource":"messages","action":"create"}],"expires_at":"` + expires + `"}}`
	srv := ac.server(t)
	r := NewResolver(srv.URL, "", time.Minute, nil)
	now := time.Now()
	r.now = func() time.Time { return now }
	if _, err := r.Resolve(context.Background(), token, ""); err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return now.Add(20 * time.Second) }
	ac.status.Store(http.StatusUnauthorized)
	if _, err := r.Resolve(context.Background(), token, ""); !errors.Is(err, ErrInvalid) || ac.calls.Load() != 2 {
		t.Fatalf("pasada su caducidad se vuelve a preguntar: %v %d", err, ac.calls.Load())
	}
}

func TestRespuestasDeAccessControl(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusUnauthorized, `{"error":{"code":"API_KEY_INVALID"}}`, ErrInvalid},
		{http.StatusServiceUnavailable, `{}`, ErrUnavailable},
		{http.StatusOK, `no es json`, ErrUnavailable},
		{http.StatusOK, `{"data":{"id":"k1","tenant_id":"t1","scopes":[]}}`, ErrUnavailable},
		{http.StatusOK, `{"data":{"id":"k1","tenant_id":"t1","scopes":[{"module":"Mal","resource":"x","action":"y"}]}}`, ErrUnavailable},
	}
	for _, tc := range cases {
		ac := &accessControl{body: tc.body}
		srv := ac.server(t)
		ac.status.Store(int32(tc.status))
		if _, err := NewResolver(srv.URL, "", 0, nil).Resolve(context.Background(), token, ""); !errors.Is(err, tc.want) {
			t.Errorf("%d %s: %v", tc.status, tc.body, err)
		}
	}
	if _, err := NewResolver("http://127.0.0.1:1", "", 0, nil).Resolve(context.Background(), token, ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("inalcanzable: %v", err)
	}
}

// Un negativo se cachea poco: frena una rafaga de la misma clave mala sin tapar una recien creada.
func TestNegativoCorto(t *testing.T) {
	ac := &accessControl{}
	srv := ac.server(t)
	ac.status.Store(http.StatusUnauthorized)
	r := NewResolver(srv.URL, "", time.Minute, nil)
	now := time.Now()
	r.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, err := r.Resolve(context.Background(), token, ""); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if ac.calls.Load() != 1 {
		t.Fatalf("la rafaga pregunta una vez: %d", ac.calls.Load())
	}
	ac.status.Store(http.StatusOK)
	r.now = func() time.Time { return now.Add(negativeTTL + time.Second) }
	if _, err := r.Resolve(context.Background(), token, ""); err != nil {
		t.Fatalf("pasado el negativo: %v", err)
	}
	r.Forget(token)
	if _, err := r.Resolve(context.Background(), token, ""); err != nil || ac.calls.Load() != 3 {
		t.Fatalf("olvidada se vuelve a preguntar: %v %d", err, ac.calls.Load())
	}
}

func TestFormaDelToken(t *testing.T) {
	for _, bad := range []string{"", "eyJhbGciOi.jwt", "cfm_" + string(make([]byte, maxTokenLen))} {
		if LooksLikeToken(bad) {
			t.Errorf("%q no es una clave", bad)
		}
		if _, err := NewResolver("http://127.0.0.1:1", "", 0, nil).Resolve(context.Background(), bad, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if !LooksLikeToken(token) {
		t.Fatal("forma valida")
	}
	if r := NewResolver("http://x", "", time.Hour, nil); r.ttl != MaxCacheTTL {
		t.Fatalf("la cache nunca pasa de MaxCacheTTL: %v", r.ttl)
	}
}
