package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
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

func TestControlCompruebaLaDireccionYElPuertoDeCadaConexion(t *testing.T) {
	g := New([]string{"443"}, time.Second, Options{})
	for _, addr := range []string{"10.0.0.1:443", "[fd00::1]:443", "no-es-una-direccion", "8.8.8.8:8443"} {
		if err := g.Control("tcp", addr, nil); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := g.Control("tcp4", "8.8.8.8:443", nil); err != nil {
		t.Fatalf("publica: %v", err)
	}
}

func TestElDialerPorDefectoNoConectaConLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	g := New([]string{port}, time.Second, Options{})
	if _, err := g.DialContext(context.Background(), "tcp", ln.Addr().String()); !errors.Is(err, ErrRefused) {
		t.Fatalf("una IP de loopback no se admite: %v", err)
	}
	d := &net.Dialer{Timeout: time.Second, Control: g.Control}
	if _, err := d.DialContext(context.Background(), "tcp", ln.Addr().String()); !errors.Is(err, ErrRefused) {
		t.Fatalf("el control del dialer debe cortar la conexion: %v", err)
	}
}

type fixedDNS []string

func (d fixedDNS) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, a := range d {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func TestDialContextRechazaPuertosYNombresConAlgunaDireccionInterna(t *testing.T) {
	var dialed []string
	g := New([]string{"80", "443"}, time.Second, Options{
		Resolver: fixedDNS{"8.8.8.8", "10.0.0.1"},
		Dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return nil, errors.New("sin red en la prueba")
		},
	})
	if _, err := g.DialContext(context.Background(), "tcp", "example.com:8080"); !errors.Is(err, ErrRefused) {
		t.Fatalf("puerto: %v", err)
	}
	if _, err := g.DialContext(context.Background(), "tcp", "example.com:443"); !errors.Is(err, ErrRefused) {
		t.Fatalf("una interna entre las resueltas: %v", err)
	}
	if len(dialed) != 0 {
		t.Fatalf("no se conecta con nada: %v", dialed)
	}
}
