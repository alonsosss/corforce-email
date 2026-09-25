package remoteimage

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/egress"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n" + "resto-del-fichero")

// hostDNS resuelve cada nombre a las direcciones que diga la prueba.
type hostDNS map[string][]string

func (d hostDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	answer, ok := d[host]
	if !ok {
		return nil, errors.New("NXDOMAIN")
	}
	out := make([]netip.Addr, len(answer))
	for i, a := range answer {
		out[i] = netip.MustParseAddr(a)
	}
	return out, nil
}

// harness monta un servidor http y otro https de pruebas. Los nombres de la prueba resuelven a
// 127.0.0.1, que la regla de direcciones admite como si fuera publica; 127.0.0.1:80 y :443 se llevan a
// los servidores de pruebas. Cada servidor responde segun el Host pedido.
type harness struct {
	mu      sync.Mutex
	dialed  []string
	seen    []*http.Request
	routes  map[string]http.HandlerFunc
	client  *Client
	httpSrv *httptest.Server
	tlsSrv  *httptest.Server
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{routes: map[string]http.HandlerFunc{}}
	serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.seen = append(h.seen, r)
		route := h.routes[r.Host+r.URL.Path]
		h.mu.Unlock()
		if route == nil {
			http.NotFound(w, r)
			return
		}
		route(w, r)
	})
	h.httpSrv = httptest.NewServer(serve)
	h.tlsSrv = httptest.NewTLSServer(serve)
	t.Cleanup(h.httpSrv.Close)
	t.Cleanup(h.tlsSrv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(h.tlsSrv.Certificate())
	dns := hostDNS{"example.com": {"127.0.0.1"}, "cdn.example.com": {"127.0.0.1"}, "interno.example.com": {"10.0.0.8"}}
	var err error
	h.client, err = newClient(cfg, options{rootCAs: pool, egress: egress.Options{
		Resolver: dns,
		Allowed:  func(a netip.Addr) bool { return a.IsLoopback() },
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			h.mu.Lock()
			h.dialed = append(h.dialed, address)
			h.mu.Unlock()
			target := map[string]string{
				"127.0.0.1:80":  h.httpSrv.Listener.Addr().String(),
				"127.0.0.1:443": h.tlsSrv.Listener.Addr().String(),
			}[address]
			if target == "" {
				return nil, errors.New("la prueba solo conecta con 127.0.0.1:80 y :443")
			}
			var d net.Dialer
			return d.DialContext(ctx, network, target)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) route(hostPath string, fn http.HandlerFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routes[hostPath] = fn
}

func servePNG(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(pngBytes)
}

func defaultConfig() Config { return Config{Timeout: 2 * time.Second, MaxBytes: 1 << 10} }

func TestDescargaPorHTTPYHTTPSSinDatosDelLector(t *testing.T) {
	h := newHarness(t, defaultConfig())
	h.route("example.com/a.png", servePNG)
	for _, u := range []string{"http://example.com/a.png", "https://example.com/a.png#frag"} {
		data, err := h.client.Fetch(context.Background(), u)
		if err != nil || string(data) != string(pngBytes) {
			t.Fatalf("%s: %q %v", u, data, err)
		}
	}
	for _, r := range h.seen {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" ||
			r.Header.Get("User-Agent") != userAgent || r.Header.Get("X-Forwarded-For") != "" {
			t.Fatalf("cabeceras enviadas: %v", r.Header)
		}
	}
	if len(h.dialed) != 2 || h.dialed[0] != "127.0.0.1:80" || h.dialed[1] != "127.0.0.1:443" {
		t.Fatalf("conecta a la IP resuelta y al puerto del esquema: %v", h.dialed)
	}
}

func TestSigueHastaTresRedireccionesValidandoCadaSalto(t *testing.T) {
	h := newHarness(t, defaultConfig())
	h.route("example.com/r1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://cdn.example.com/r2", http.StatusFound)
	})
	h.route("cdn.example.com/r2", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/r3", http.StatusMovedPermanently)
	})
	h.route("example.com/r3", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final.png", http.StatusFound)
	})
	h.route("example.com/final.png", servePNG)
	if _, err := h.client.Fetch(context.Background(), "http://example.com/r1"); err != nil {
		t.Fatalf("tres saltos: %v", err)
	}
	for _, r := range h.seen {
		if r.Header.Get("Referer") != "" {
			t.Fatalf("un salto lleva Referer: %s %s", r.Host, r.Header.Get("Referer"))
		}
	}

	h.route("example.com/r0", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/r1", http.StatusFound)
	})
	if _, err := h.client.Fetch(context.Background(), "http://example.com/r0"); !errors.Is(err, domain.ErrRemoteImageUnavailable) {
		t.Fatalf("cuatro saltos: %v", err)
	}
}

func TestRedireccionesAlInteriorOAOtroPuertoSeRechazan(t *testing.T) {
	h := newHarness(t, defaultConfig())
	for path, target := range map[string]string{
		"/interno": "http://interno.example.com/x.png",
		"/ip":      "http://10.0.0.1/x.png",
		"/puerto":  "http://example.com:8080/x.png",
		"/esquema": "ftp://example.com/x.png",
		"/usuario": "http://user:pw@example.com/x.png",
	} {
		target := target
		h.route("example.com"+path, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		})
		if _, err := h.client.Fetch(context.Background(), "http://example.com"+path); !errors.Is(err, domain.ErrRemoteImageRefused) {
			t.Errorf("%s -> %s: %v", path, target, err)
		}
	}
	for _, d := range h.dialed {
		if d != "127.0.0.1:80" {
			t.Fatalf("solo se conecta con destinos admitidos: %v", h.dialed)
		}
	}
}

func TestURLsYDestinosNoPublicosNoConectan(t *testing.T) {
	h := newHarness(t, defaultConfig())
	for _, u := range []string{
		"http://interno.example.com/x.png", "http://127.0.0.2:8080/x.png", "https://example.com:8443/x.png",
		"http://user:pw@example.com/x.png", "ftp://example.com/x.png", "http://example.com/x\r\n.png", "data:image/png;base64,AA",
	} {
		if _, err := h.client.Fetch(context.Background(), u); !errors.Is(err, domain.ErrRemoteImageRefused) {
			t.Errorf("%s: %v", u, err)
		}
	}
	if len(h.dialed) != 0 {
		t.Fatalf("no se conecta con nada: %v", h.dialed)
	}
	if _, err := h.client.Fetch(context.Background(), "http://noexiste.example.com/x.png"); !errors.Is(err, domain.ErrRemoteImageUnavailable) {
		t.Fatalf("sin DNS: %v", err)
	}
}

func TestTopeDeBytesEstadoYPlazo(t *testing.T) {
	h := newHarness(t, Config{Timeout: 300 * time.Millisecond, MaxBytes: 64})
	h.route("example.com/grande-declarada", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 65)))
	})
	h.route("example.com/grande-troceada", func(w http.ResponseWriter, _ *http.Request) {
		for range 10 {
			_, _ = w.Write([]byte(strings.Repeat("x", 10)))
			w.(http.Flusher).Flush()
		}
	})
	h.route("example.com/exacta", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 64)))
	})
	h.route("example.com/error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	h.route("example.com/lenta", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Second)
		servePNG(w, nil)
	})
	for _, path := range []string{"/grande-declarada", "/grande-troceada"} {
		if _, err := h.client.Fetch(context.Background(), "http://example.com"+path); !errors.Is(err, domain.ErrRemoteImageTooLarge) {
			t.Errorf("%s: %v", path, err)
		}
	}
	if data, err := h.client.Fetch(context.Background(), "http://example.com/exacta"); err != nil || len(data) != 64 {
		t.Fatalf("en el tope: %d %v", len(data), err)
	}
	for _, path := range []string{"/error", "/lenta"} {
		_, err := h.client.Fetch(context.Background(), "http://example.com"+path+"?token=secreto")
		if !errors.Is(err, domain.ErrRemoteImageUnavailable) || strings.Contains(err.Error(), "secreto") {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestConfiguracionInvalida(t *testing.T) {
	for _, cfg := range []Config{{}, {Timeout: time.Second}, {MaxBytes: 1}} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%+v", cfg)
		}
	}
}
