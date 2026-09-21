package main

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	raw, ok := f[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	out := make([]netip.Addr, 0, len(raw))
	for _, r := range raw {
		out = append(out, netip.MustParseAddr(r))
	}
	return out, nil
}

func TestGuardaSSRFDireccionesLiterales(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255",
		"192.168.1.10", "100.64.0.1", "100.127.255.255", "169.254.169.254", "169.254.0.1", "0.0.0.0", "0.1.2.3",
		"224.0.0.1", "239.255.255.250", "255.255.255.255", "240.0.0.1", "192.0.2.5", "198.18.0.1", "198.51.100.7", "203.0.113.9",
		"::1", "::", "fe80::1", "fd00::1", "fc00::1", "fd00:ec2::254", "ff02::1",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:192.168.0.1",
		"::10.0.0.1", "64:ff9b::a00:1", "2002:a00:1::1", "2001::1", "2001:db8::1", "[::1]",
	}
	g := NewSourceGuard(fakeResolver{}, false)
	for _, host := range blocked {
		t.Run(host, func(t *testing.T) {
			if _, err := g.Resolve(context.Background(), host); !errors.Is(err, errBlockedAddress) {
				t.Fatalf("%s debe estar bloqueada, error: %v", host, err)
			}
		})
	}

	allowed := map[string]string{
		"93.184.216.34":                      "93.184.216.34",
		"8.8.8.8":                            "8.8.8.8",
		"172.32.0.1":                         "172.32.0.1",
		"100.128.0.1":                        "100.128.0.1",
		"2606:2800:220:1:248:1893:25c8:1946": "2606:2800:220:1:248:1893:25c8:1946",
		"[2606:4700:4700::1111]":             "2606:4700:4700::1111",
		"::ffff:8.8.8.8":                     "8.8.8.8",
	}
	for host, want := range allowed {
		t.Run(host, func(t *testing.T) {
			got, err := g.Resolve(context.Background(), host)
			if err != nil {
				t.Fatalf("%s debe permitirse: %v", host, err)
			}
			if got.IP.String() != want || !got.IsLiteral || got.VerifyName != want {
				t.Fatalf("destino %+v, quiero IP %s", got, want)
			}
		})
	}
}

func TestGuardaSSRFFormasNumericasAmbiguas(t *testing.T) {
	g := NewSourceGuard(fakeResolver{}, false)
	for _, host := range []string{"2130706433", "0x7f000001", "0177.0.0.1", "127.1", "1.2.3", "10.1", "0x7f.1", "fe80::1%eth0", "[fe80::1%25eth0]", "::1]", ""} {
		if _, err := g.Resolve(context.Background(), host); err == nil {
			t.Errorf("%q no debe aceptarse como host", host)
		}
	}
}

func TestGuardaSSRFNombres(t *testing.T) {
	resolver := fakeResolver{
		"imap.publico.example":    {"93.184.216.34"},
		"doble.example":           {"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"},
		"v6.example":              {"2606:2800:220:1:248:1893:25c8:1946"},
		"interno.example":         {"10.0.0.5"},
		"mezcla.example":          {"93.184.216.34", "10.0.0.5"},
		"mezcla-v6.example":       {"93.184.216.34", "fd00::5"},
		"metadata.example":        {"169.254.169.254"},
		"metadata-v6.example":     {"fd00:ec2::254"},
		"loopback.example":        {"127.0.0.1"},
		"mapeada.example":         {"::ffff:10.0.0.1"},
		"cgnat.example":           {"100.64.0.10"},
		"localhost":               {"127.0.0.1", "::1"},
		"foo.localhost":           {"127.0.0.1"},
		"imap.mayusculas.example": {"8.8.4.4"},
	}
	g := NewSourceGuard(resolver, false)

	ok := map[string]string{
		"imap.publico.example":    "93.184.216.34",
		"doble.example":           "93.184.216.34",
		"v6.example":              "2606:2800:220:1:248:1893:25c8:1946",
		"IMAP.Mayusculas.Example": "8.8.4.4",
		"imap.publico.example.":   "93.184.216.34",
	}
	for host, ip := range ok {
		got, err := g.Resolve(context.Background(), host)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if got.IP.String() != ip || got.IsLiteral {
			t.Fatalf("%s: destino %+v, quiero %s", host, got, ip)
		}
		if got.VerifyName == "" || got.VerifyName != normalizedHost(host) {
			t.Fatalf("%s: el certificado se verifica contra %q", host, got.VerifyName)
		}
	}

	for _, host := range []string{"interno.example", "mezcla.example", "mezcla-v6.example", "metadata.example", "metadata-v6.example", "loopback.example", "mapeada.example", "cgnat.example", "localhost", "foo.localhost"} {
		if _, err := g.Resolve(context.Background(), host); !errors.Is(err, errBlockedAddress) {
			t.Errorf("%s debe bloquearse, error: %v", host, err)
		}
	}
	if _, err := g.Resolve(context.Background(), "no-existe.example"); !errors.Is(err, errUnresolvable) {
		t.Errorf("un nombre que no resuelve: %v", err)
	}
	for _, host := range []string{"-malo.example", "con espacio.example", "a..b.example", "host;rm.example", "http://x.example", "user@host.example", "--host2=x"} {
		if _, err := g.Resolve(context.Background(), host); !errors.Is(err, errInvalidHost) {
			t.Errorf("%q debe ser un host invalido, error: %v", host, err)
		}
	}
}

func normalizedHost(h string) string {
	for len(h) > 0 && h[len(h)-1] == '.' {
		h = h[:len(h)-1]
	}
	out := []byte(h)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + 32
		}
	}
	return string(out)
}

func TestGuardaSSRFPrefiereIPv4YFijaLaIP(t *testing.T) {
	g := NewSourceGuard(fakeResolver{"dual.example": {"2606:2800:220:1:248:1893:25c8:1946", "93.184.216.34"}}, false)
	got, err := g.Resolve(context.Background(), "dual.example")
	if err != nil || got.IP.String() != "93.184.216.34" {
		t.Fatalf("destino %+v, error %v", got, err)
	}
}

func TestGuardaSSRFModoPruebaAdmiteLoPrivadoPeroNoLoDemas(t *testing.T) {
	g := NewSourceGuard(fakeResolver{"dovecot": {"172.22.1.250"}, "meta": {"169.254.169.254"}}, true)
	for _, host := range []string{"10.0.0.1", "127.0.0.1", "192.168.1.1", "100.64.0.1", "::1", "dovecot", "172.22.1.250"} {
		if _, err := g.Resolve(context.Background(), host); err != nil {
			t.Errorf("%s debe admitirse en modo de prueba: %v", host, err)
		}
	}
	for _, host := range []string{"169.254.169.254", "fe80::1", "fd00:ec2::254", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "meta", "255.255.255.255"} {
		if _, err := g.Resolve(context.Background(), host); !errors.Is(err, errBlockedAddress) {
			t.Errorf("%s no debe admitirse ni en modo de prueba, error: %v", host, err)
		}
	}
}
