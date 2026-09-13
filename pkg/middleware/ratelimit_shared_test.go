package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"go.uber.org/zap"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeStore reproduce la semantica del script de Redis (ventana anclada en la primera
// peticion, caducidad del almacen) sobre el reloj de la prueba.
type fakeStore struct {
	mu     sync.Mutex
	clock  *fakeClock
	counts map[string]int64
	ends   map[string]time.Time
	err    error
	calls  int
	keys   []string
}

func newFakeStore(clock *fakeClock) *fakeStore {
	return &fakeStore{clock: clock, counts: map[string]int64{}, ends: map[string]time.Time{}}
}

func (s *fakeStore) Hit(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.keys = append(s.keys, key)
	if s.err != nil {
		return 0, 0, s.err
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	now := s.clock.Now()
	if end, ok := s.ends[key]; !ok || !now.Before(end) {
		s.counts[key] = 0
		s.ends[key] = now.Add(window)
	}
	s.counts[key]++
	return s.counts[key], s.ends[key].Sub(now), nil
}

func (s *fakeStore) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *fakeStore) snapshot() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]string(nil), s.keys...)
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func degradedCount(t *testing.T, limiter string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := rateLimitDegraded.WithLabelValues(limiter).Write(m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// Dos replicas del gateway con el mismo almacen reparten un unico cupo: el cliente que
// alterna entre ellas no obtiene el doble.
func TestSharedDosReplicasCompartenCupo(t *testing.T) {
	clock := newFakeClock()
	store := newFakeStore(clock)
	a := newSharedRateLimiter(store, "test:replicas", 3, time.Minute, zap.NewNop(), clock.Now).Limit(okHandler)
	b := newSharedRateLimiter(store, "test:replicas", 3, time.Minute, zap.NewNop(), clock.Now).Limit(okHandler)

	for i, h := range []http.Handler{a, b, a} {
		if code := serve(h, peticionDe("", "203.0.113.7")).Code; code != http.StatusOK {
			t.Fatalf("peticion %d dentro del cupo: %d", i+1, code)
		}
	}
	if code := serve(b, peticionDe("", "203.0.113.7")).Code; code != http.StatusTooManyRequests {
		t.Fatalf("la replica B concedio un cupo propio: %d", code)
	}
	if code := serve(a, peticionDe("", "198.51.100.4")).Code; code != http.StatusOK {
		t.Fatalf("otra IP no comparte cupo con la primera: %d", code)
	}
}

// Retry-After dice cuanto falta para que la ventana se cierre, redondeado hacia arriba, y
// es igual decida la memoria o el almacen.
func TestRetryAfterHastaElFinDeLaVentana(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(*fakeClock) *RateLimiter
	}{
		{"memoria", func(c *fakeClock) *RateLimiter { return newRateLimiter(2, time.Minute, c.Now) }},
		{"almacen", func(c *fakeClock) *RateLimiter {
			return newSharedRateLimiter(newFakeStore(c), "test:retry", 2, time.Minute, zap.NewNop(), c.Now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			h := tc.make(clock).Limit(okHandler)
			serve(h, peticionDe("", "203.0.113.7"))
			clock.Advance(20*time.Second + 300*time.Millisecond)
			serve(h, peticionDe("", "203.0.113.7"))
			w := serve(h, peticionDe("", "203.0.113.7"))
			if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "40" {
				t.Fatalf("status %d, Retry-After %q; se esperaba 429 y 40", w.Code, w.Header().Get("Retry-After"))
			}
			clock.Advance(39*time.Second + 500*time.Millisecond)
			if w := serve(h, peticionDe("", "203.0.113.7")); w.Header().Get("Retry-After") != "1" {
				t.Fatalf("a medio segundo del cierre, Retry-After %q; se esperaba 1", w.Header().Get("Retry-After"))
			}
			clock.Advance(time.Second)
			if code := serve(h, peticionDe("", "203.0.113.7")).Code; code != http.StatusOK {
				t.Fatalf("la ventana siguiente no se abrio: %d", code)
			}
		})
	}
}

// Con el almacen caido se sigue limitando en memoria: ni se deja pasar sin limite ni se
// rechaza a todo el mundo por no poder contar. Tras un fallo no se vuelve a esperar al
// almacen hasta que pasa la pausa, y cuando responde vuelve a decidir el.
func TestSharedDegradaAMemoriaConElAlmacenCaido(t *testing.T) {
	clock := newFakeClock()
	store := newFakeStore(clock)
	store.fail(errors.New("dial tcp: connection refused"))
	const name = "test:degradado"
	before := degradedCount(t, name)
	h := newSharedRateLimiter(store, name, 2, time.Minute, zap.NewNop(), clock.Now).Limit(okHandler)

	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, serve(h, peticionDe("", "203.0.113.7")).Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("con el almacen caido: %v; se esperaba 200, 200, 429", codes)
	}
	if got := degradedCount(t, name) - before; got != 3 {
		t.Fatalf("rate_limit_degraded_total subio %v; se esperaba 3", got)
	}
	if calls, _ := store.snapshot(); calls != 1 {
		t.Fatalf("durante la pausa se consulto el almacen %d veces; se esperaba 1", calls)
	}

	store.fail(nil)
	clock.Advance(sharedCooldown)
	if code := serve(h, peticionDe("", "198.51.100.4")).Code; code != http.StatusOK {
		t.Fatalf("tras la pausa: %d", code)
	}
	if calls, _ := store.snapshot(); calls != 2 {
		t.Fatalf("tras la pausa no se volvio al almacen (%d consultas)", calls)
	}
	if got := degradedCount(t, name) - before; got != 3 {
		t.Fatalf("con el almacen de vuelta se siguio contando degradacion: %v", got)
	}
}

// La memoria cuenta aunque decida el almacen: si este cae, la replica sigue desde lo que
// ya vio y no regala un cupo nuevo.
func TestSharedConservaLoContadoAlCaerElAlmacen(t *testing.T) {
	clock := newFakeClock()
	store := newFakeStore(clock)
	h := newSharedRateLimiter(store, "test:continuidad", 3, time.Minute, zap.NewNop(), clock.Now).Limit(okHandler)

	serve(h, peticionDe("", "203.0.113.7"))
	serve(h, peticionDe("", "203.0.113.7"))
	store.fail(errors.New("i/o timeout"))
	if code := serve(h, peticionDe("", "203.0.113.7")).Code; code != http.StatusOK {
		t.Fatalf("tercera peticion dentro del cupo: %d", code)
	}
	if code := serve(h, peticionDe("", "203.0.113.7")).Code; code != http.StatusTooManyRequests {
		t.Fatalf("al caer el almacen la memoria concedio un cupo nuevo: %d", code)
	}
}

// Un cliente que corta la peticion no es un almacen caido.
func TestSharedCancelacionDelClienteNoDegrada(t *testing.T) {
	clock := newFakeClock()
	store := newFakeStore(clock)
	const name = "test:cancelacion"
	before := degradedCount(t, name)
	h := newSharedRateLimiter(store, name, 5, time.Minute, zap.NewNop(), clock.Now).Limit(okHandler)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := serve(h, peticionDe("", "203.0.113.7").WithContext(ctx)).Code; code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got := degradedCount(t, name) - before; got != 0 {
		t.Fatalf("la cancelacion del cliente se conto como degradacion")
	}
}

// En el gateway la clave sale de la IP que resolvio CaptureClientIP: un cliente de
// internet no puede elegirla con X-Real-IP, X-Forwarded-For ni cabeceras internas, y solo
// el proxy de borde de confianza aporta la IP del visitante.
func TestClaveNoLaEligenLasCabecerasDelCliente(t *testing.T) {
	clock := newFakeClock()
	store := newFakeStore(clock)
	rl := newSharedRateLimiter(store, "gateway:auth", 3, time.Minute, zap.NewNop(), clock.Now)
	h := StripInternalHeaders(CaptureClientIP(TrustedProxyCIDRs(""))(rl.Limit(okHandler)))

	codes := []int{}
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		r.RemoteAddr = "203.0.113.9:4711"
		r.Header.Set("X-Real-IP", "10.9.9."+string(rune('1'+i)))
		r.Header.Set("X-Forwarded-For", "192.0.2."+string(rune('1'+i))+", 10.0.0.1")
		r.Header.Set("X-User-ID", "otro-usuario")
		codes = append(codes, serve(h, r).Code)
	}
	if codes[3] != http.StatusTooManyRequests || codes[4] != http.StatusTooManyRequests {
		t.Fatalf("rotando cabeceras el cliente escapo del limite: %v", codes)
	}
	_, keys := store.snapshot()
	for _, k := range keys {
		if k != "rl:gateway:auth:ip:203.0.113.9" {
			t.Fatalf("la clave dependio de las cabeceras del cliente: %q", k)
		}
	}

	for _, tc := range []struct{ remote, realIP, want string }{
		{"10.0.0.5:1000", "198.51.100.7", "rl:gateway:auth:ip:198.51.100.7"},
		{"10.0.0.5:1000", "no-es-una-ip", "rl:gateway:auth:ip:10.0.0.5"},
		{"[2001:db8::1]:1000", "198.51.100.7", "rl:gateway:auth:ip6:2001:db8::/64"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Real-IP", tc.realIP)
		serve(h, r)
		_, keys := store.snapshot()
		if got := keys[len(keys)-1]; got != tc.want {
			t.Fatalf("desde %s con X-Real-IP %q la clave fue %q; se esperaba %q", tc.remote, tc.realIP, got, tc.want)
		}
	}
}

// La clave por usuario no deja el identificador en claro, y lo que no es una IP tampoco
// llega tal cual al almacen.
func TestClaveSinIdentificadoresEnClaro(t *testing.T) {
	clock := newFakeClock()
	store := newFakeStore(clock)
	rl := newSharedRateLimiter(store, "test:claves", 10, time.Minute, zap.NewNop(), clock.Now)

	serve(rl.LimitPerUser(okHandler), peticionDe("0b7e2f7c-5d7a-4a57-9a39-6c2f7e0c1a11", "203.0.113.7"))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Real-IP", "texto elegido:por el cliente")
	serve(rl.Limit(okHandler), r)

	_, keys := store.snapshot()
	if len(keys) != 2 {
		t.Fatalf("claves: %v", keys)
	}
	if !strings.HasPrefix(keys[0], "rl:test:claves:u:") || strings.Contains(keys[0], "0b7e2f7c") {
		t.Fatalf("clave por usuario %q", keys[0])
	}
	if !strings.HasPrefix(keys[1], "rl:test:claves:id:") || strings.Contains(keys[1], "cliente") {
		t.Fatalf("clave de una identidad que no es IP %q", keys[1])
	}
}

func TestNombreDeLimitadorInvalido(t *testing.T) {
	for _, name := range []string{"", "Gateway", "gateway auth", "gateway:", "rl:{x}"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("el nombre %q se acepto", name)
				}
			}()
			NewSharedRateLimiter(newFakeStore(newFakeClock()), name, 1, time.Minute, nil)
		}()
	}
}
