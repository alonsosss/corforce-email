package unsubscribe

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func TestIsPublicAddr(t *testing.T) {
	blocked := []string{
		"10.0.0.1", "172.16.5.4", "192.168.1.1", "127.0.0.1", "0.0.0.0", "169.254.169.254", "100.64.0.1",
		"192.0.2.10", "198.18.0.1", "203.0.113.5", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "::ffff:10.0.0.1", "::ffff:127.0.0.1",
		"64:ff9b::a00:1", "2002:a00:1::1", "2001::1", "2001:db8::1", "fec0::1",
	}
	for _, s := range blocked {
		if IsPublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s no es publica", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !IsPublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s es publica", s)
		}
	}
	if IsPublicAddr(netip.Addr{}) {
		t.Error("una direccion vacia no es publica")
	}
}

// fakeDNS responde lo que diga la prueba en cada consulta.
type fakeDNS struct {
	mu      sync.Mutex
	answers [][]string
	calls   int
	err     error
}

func (d *fakeDNS) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	answer := d.answers[min(d.calls, len(d.answers)-1)]
	d.calls++
	var out []netip.Addr
	for _, a := range answer {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

// harness monta un servidor TLS de pruebas que se hace pasar por "example.com" (su certificado lo
// cubre). La regla de direcciones admite solo loopback, que en las pruebas hace de "publica": asi se
// ve que se conecta a la IP comprobada y no al nombre.
type harness struct {
	srv     *httptest.Server
	dns     *fakeDNS
	dialed  []string
	hits    atomic.Int32
	client  *Client
	handler http.HandlerFunc
}

func newHarness(t *testing.T, timeout time.Duration) *harness {
	t.Helper()
	h := &harness{dns: &fakeDNS{answers: [][]string{{"127.0.0.1"}}}}
	h.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		if h.handler != nil {
			h.handler(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(h.srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(h.srv.Certificate())
	var mu sync.Mutex
	h.client = newClient(timeout, options{
		resolver: h.dns,
		rootCAs:  pool,
		allowed:  func(a netip.Addr) bool { return a.IsLoopback() },
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			h.dialed = append(h.dialed, address)
			mu.Unlock()
			if address != "127.0.0.1:443" {
				return nil, errors.New("la prueba solo conecta con 127.0.0.1:443")
			}
			var d net.Dialer
			return d.DialContext(ctx, network, h.srv.Listener.Addr().String())
		},
	})
	return h
}

func TestOneClickEnviaElPOSTDeRFC8058ALaIPComprobada(t *testing.T) {
	h := newHarness(t, 5*time.Second)
	var got struct {
		method, body, ctype, cookie string
	}
	h.handler = func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.body, got.ctype, got.cookie = r.Method, string(b), r.Header.Get("Content-Type"), r.Header.Get("Cookie")
		w.WriteHeader(http.StatusAccepted)
	}
	if err := h.client.OneClick(context.Background(), "https://example.com/u/token-del-destinatario"); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.body != "List-Unsubscribe=One-Click" || got.ctype != "application/x-www-form-urlencoded" || got.cookie != "" {
		t.Fatalf("peticion: %+v", got)
	}
	if len(h.dialed) != 1 || h.dialed[0] != "127.0.0.1:443" {
		t.Fatalf("debe conectar a la IP resuelta, no al nombre: %v", h.dialed)
	}
}

func TestOneClickRechazaURLsYDestinosNoPublicosSinConectar(t *testing.T) {
	h := newHarness(t, 5*time.Second)
	h.client.allowed = IsPublicAddr
	for _, target := range []string{
		"http://example.com/u", "https://example.com:8443/u", "https://user:pw@example.com/u", "ftp://example.com/u",
		"https://127.0.0.1/u", "https://[::1]/u", "https://169.254.169.254/latest/meta-data", "https://10.0.0.1/u",
		"https://[::ffff:192.168.0.1]/u", "https://example.com/u\r\nX: y",
	} {
		if err := h.client.OneClick(context.Background(), target); !errors.Is(err, domain.ErrUnsubscribeRefused) {
			t.Errorf("%s: %v", target, err)
		}
	}
	if len(h.dialed) != 0 || h.hits.Load() != 0 {
		t.Fatalf("no se conecta con nada que no pase la comprobacion: %v", h.dialed)
	}
}

func TestOneClickRechazaUnNombreQueResuelveAUnaIPInterna(t *testing.T) {
	h := newHarness(t, 5*time.Second)
	h.dns.answers = [][]string{{"127.0.0.1", "10.1.2.3"}}
	if err := h.client.OneClick(context.Background(), "https://example.com/u"); !errors.Is(err, domain.ErrUnsubscribeRefused) {
		t.Fatalf("basta una direccion interna entre las resueltas: %v", err)
	}
	h.dns.answers = [][]string{{"::ffff:10.0.0.1"}}
	if err := h.client.OneClick(context.Background(), "https://example.com/u"); !errors.Is(err, domain.ErrUnsubscribeRefused) {
		t.Fatalf("IPv4 mapeada en IPv6: %v", err)
	}
	if len(h.dialed) != 0 {
		t.Fatalf("dialed: %v", h.dialed)
	}
}

func TestOneClickConUnDNSQueCambiaVuelveAComprobarEnCadaConexion(t *testing.T) {
	h := newHarness(t, 5*time.Second)
	h.dns.answers = [][]string{{"127.0.0.1"}, {"10.0.0.7"}}
	if err := h.client.OneClick(context.Background(), "https://example.com/u"); err != nil {
		t.Fatal(err)
	}
	if err := h.client.OneClick(context.Background(), "https://example.com/u"); !errors.Is(err, domain.ErrUnsubscribeRefused) {
		t.Fatalf("la segunda resolucion apunta dentro: %v", err)
	}
	if h.dns.calls != 2 || len(h.dialed) != 1 || h.hits.Load() != 1 {
		t.Fatalf("resoluciones=%d conexiones=%v visitas=%d", h.dns.calls, h.dialed, h.hits.Load())
	}
}

func TestOneClickNoSigueRedirecciones(t *testing.T) {
	var internal atomic.Int32
	inner := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { internal.Add(1) }))
	defer inner.Close()
	h := newHarness(t, 5*time.Second)
	h.handler = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL+"/admin", http.StatusFound)
	}
	if err := h.client.OneClick(context.Background(), "https://example.com/u"); err != nil {
		t.Fatalf("una 3xx confirma la recepcion: %v", err)
	}
	if internal.Load() != 0 || h.hits.Load() != 1 {
		t.Fatalf("la redireccion no se visita: %d", internal.Load())
	}
}

func TestOneClickRespuestasQueNoConfirmanYPlazos(t *testing.T) {
	h := newHarness(t, 300*time.Millisecond)
	h.handler = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	err := h.client.OneClick(context.Background(), "https://example.com/u/secreto")
	if !errors.Is(err, domain.ErrUnsubscribeFailed) {
		t.Fatalf("500: %v", err)
	}
	h.handler = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1<<20)))
	}
	if err := h.client.OneClick(context.Background(), "https://example.com/u"); err != nil {
		t.Fatalf("un cuerpo grande se descarta acotado: %v", err)
	}
	h.handler = func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Second)
		w.WriteHeader(http.StatusOK)
	}
	err = h.client.OneClick(context.Background(), "https://example.com/u/secreto")
	if !errors.Is(err, domain.ErrUnsubscribeFailed) || strings.Contains(err.Error(), "secreto") {
		t.Fatalf("plazo: %v", err)
	}
	h.dns.err = errors.New("NXDOMAIN")
	if err := h.client.OneClick(context.Background(), "https://example.com/u"); !errors.Is(err, domain.ErrUnsubscribeFailed) {
		t.Fatalf("sin DNS: %v", err)
	}
}

func TestControlConnCompruebaLaDireccionDeCadaConexion(t *testing.T) {
	c := New(0)
	if c.timeout != defaultTimeout {
		t.Fatalf("plazo por defecto: %v", c.timeout)
	}
	if err := c.controlConn("tcp4", "10.0.0.1:443", nil); !errors.Is(err, domain.ErrUnsubscribeRefused) {
		t.Fatalf("privada: %v", err)
	}
	if err := c.controlConn("tcp6", "[fd00::1]:443", nil); !errors.Is(err, domain.ErrUnsubscribeRefused) {
		t.Fatalf("ULA: %v", err)
	}
	if err := c.controlConn("tcp4", "no-es-una-direccion", nil); !errors.Is(err, domain.ErrUnsubscribeRefused) {
		t.Fatalf("ilegible: %v", err)
	}
	if err := c.controlConn("tcp4", "8.8.8.8:443", nil); err != nil {
		t.Fatalf("publica: %v", err)
	}
}

func TestElDialerPorDefectoNoConectaConLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c := newClient(time.Second, options{allowed: IsPublicAddr})
	d := &net.Dialer{Timeout: time.Second, Control: c.controlConn}
	if _, err := d.DialContext(context.Background(), "tcp", ln.Addr().String()); !errors.Is(err, domain.ErrUnsubscribeRefused) {
		t.Fatalf("el control del dialer debe cortar la conexion: %v", err)
	}
}
