package middleware

import (
	"strings"
	"testing"
)

func TestIPIdentityAgrupaIPv6PorPrefijo(t *testing.T) {
	cases := []struct {
		name, a, b string
		same       bool
	}{
		{"IPv4 por direccion", "203.0.113.7", "203.0.113.7", true},
		{"IPv4 distintas", "203.0.113.7", "203.0.113.8", false},
		{"IPv4 mapeada en IPv6 cuenta como IPv4", "::ffff:203.0.113.7", "203.0.113.7", true},
		{"mismo /64", "2001:db8:1:2::1", "2001:db8:1:2:ffff:ffff:ffff:ffff", true},
		{"otro /64", "2001:db8:1:2::1", "2001:db8:1:3::1", false},
	}
	for _, c := range cases {
		ka, kb := ipIdentity(c.a), ipIdentity(c.b)
		if (ka == kb) != c.same {
			t.Errorf("%s: %q y %q dan %q y %q", c.name, c.a, c.b, ka, kb)
		}
	}
	if k := ipIdentity("2001:db8:1:2::1"); k != "ip6:2001:db8:1:2::/64" {
		t.Errorf("clave IPv6 = %q", k)
	}
}

// Lo que no es una IP llega resumido: una clave no lleva texto elegido por el cliente.
func TestIPIdentityResumeLoQueNoEsUnaIP(t *testing.T) {
	raw := "cabecera-arbitraria\r\nmas texto"
	k := ipIdentity(raw)
	if !strings.HasPrefix(k, "id:") || strings.Contains(k, "cabecera") {
		t.Fatalf("clave = %q", k)
	}
}
