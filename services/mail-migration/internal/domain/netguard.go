package domain

import "net/netip"

// Rangos que un servidor de origen nunca puede tener: el ejecutor sale a Internet a donde el
// usuario diga, y una direccion interna convertiria la migracion en una forma de sondear la red
// de la plataforma (SSRF). Es la misma lista que aplica el ejecutor antes de conectar; los dos
// modulos son independientes a proposito y deben mantenerse iguales.
var blockedRanges = mustPrefixes(
	// IPv4
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	// IPv6: solo 2000::/3 es unicast global; de ahi se excluyen los bloques de protocolos,
	// documentacion y traduccion que embeben o alcanzan direcciones IPv4 o internas.
	"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
)

var globalUnicastV6 = netip.MustParsePrefix("2000::/3")

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// IsPublicAddr dice si la direccion es alcanzable en Internet y no es interna, de bucle local,
// enlace local, de metadatos de nube, multicast ni reservada. Una IPv4 dentro de IPv6 se juzga como
// IPv4.
func IsPublicAddr(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if addr.Is6() && !globalUnicastV6.Contains(addr) {
		return false
	}
	for _, p := range blockedRanges {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}
