package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

const dnsTimeout = 8 * time.Second

var (
	errInvalidHost    = errors.New("nombre de host no valido")
	errUnresolvable   = errors.New("el host de origen no resuelve")
	errBlockedAddress = errors.New("direccion de origen no permitida")
)

// Resolver es lo que la guarda necesita del DNS; net.DefaultResolver lo cumple y las pruebas lo sustituyen.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// SourceTarget es el destino ya validado de la conexion al origen: imapsync se conecta a IP, no al
// nombre, para que una segunda resolucion (DNS rebinding) no pueda llevarlo a otra parte, y el
// certificado se verifica contra VerifyName, el nombre que dio el usuario.
type SourceTarget struct {
	IP         netip.Addr
	VerifyName string
	IsLiteral  bool
}

// SourceGuard impide que el ejecutor se use como puente hacia la red interna, el metadata del
// proveedor o el propio host. Con allowPrivate (solo development y test) admite lo privado, pero
// nunca lo no enrutable, multicast ni los enlaces locales.
type SourceGuard struct {
	resolver     Resolver
	allowPrivate bool
}

func NewSourceGuard(resolver Resolver, allowPrivate bool) *SourceGuard {
	return &SourceGuard{resolver: resolver, allowPrivate: allowPrivate}
}

func (g *SourceGuard) Resolve(ctx context.Context, host string) (SourceTarget, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if addr, ok, err := parseLiteral(host); err != nil {
		return SourceTarget{}, err
	} else if ok {
		if err := g.check(addr); err != nil {
			return SourceTarget{}, err
		}
		return SourceTarget{IP: addr.Unmap(), VerifyName: addr.Unmap().String(), IsLiteral: true}, nil
	}
	if !validHostname(host) {
		return SourceTarget{}, errInvalidHost
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		if !g.allowPrivate {
			return SourceTarget{}, fmt.Errorf("%w: localhost", errBlockedAddress)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	addrs, err := g.resolver.LookupNetIP(ctx, "ip", lower)
	if err != nil || len(addrs) == 0 {
		return SourceTarget{}, errUnresolvable
	}
	var v4, v6 []netip.Addr
	for _, a := range addrs {
		if err := g.check(a); err != nil {
			return SourceTarget{}, err
		}
		a = a.Unmap()
		if a.Is4() {
			v4 = append(v4, a)
		} else {
			v6 = append(v6, a)
		}
	}
	pick := append(v4, v6...)[0]
	return SourceTarget{IP: pick, VerifyName: lower}, nil
}

func (g *SourceGuard) check(addr netip.Addr) error {
	if class := classifyAddr(addr); class != "" {
		if g.allowPrivate && privateClasses[class] {
			return nil
		}
		return fmt.Errorf("%w: %s", errBlockedAddress, class)
	}
	return nil
}

// privateClasses son las clases que un despliegue de prueba puede habilitar. Las demas (no
// especificada, multicast, reservada, enlace local con el metadata del proveedor) nunca.
var privateClasses = map[string]bool{"privada": true, "loopback": true, "cgnat": true}

type namedPrefix struct {
	prefix netip.Prefix
	class  string
}

func mustPrefixes(class string, cidrs ...string) []namedPrefix {
	out := make([]namedPrefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, namedPrefix{netip.MustParsePrefix(c), class})
	}
	return out
}

var blockedV4 = concat(
	mustPrefixes("no especificada", "0.0.0.0/8"),
	mustPrefixes("privada", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"),
	mustPrefixes("cgnat", "100.64.0.0/10"),
	mustPrefixes("loopback", "127.0.0.0/8"),
	mustPrefixes("enlace local", "169.254.0.0/16"),
	mustPrefixes("reservada", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"),
	mustPrefixes("multicast", "224.0.0.0/4"),
)

// IPv6 se admite solo dentro de 2000::/3 (unicast global) y sin los rangos que incrustan una IPv4
// (6to4, Teredo, NAT64) o son de documentacion o pruebas.
var (
	globalV6  = netip.MustParsePrefix("2000::/3")
	blockedV6 = concat(
		mustPrefixes("reservada", "2001::/32", "2001:2::/48", "2001:10::/28", "2001:20::/28", "2001:db8::/32", "2002::/16", "3fff::/20"),
	)
	specialV6 = concat(
		mustPrefixes("metadata", "fd00:ec2::/32"),
		mustPrefixes("no especificada", "::/128"),
		mustPrefixes("loopback", "::1/128"),
		mustPrefixes("enlace local", "fe80::/10"),
		mustPrefixes("privada", "fc00::/7"),
		mustPrefixes("multicast", "ff00::/8"),
	)
)

func concat(lists ...[]namedPrefix) []namedPrefix {
	var out []namedPrefix
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// classifyAddr devuelve "" si la direccion es un destino publico permitido y el nombre de la clase
// que la bloquea si no.
func classifyAddr(addr netip.Addr) string {
	if !addr.IsValid() || addr.Zone() != "" {
		return "no valida"
	}
	addr = addr.Unmap()
	if addr.Is4() {
		for _, np := range blockedV4 {
			if np.prefix.Contains(addr) {
				return np.class
			}
		}
		if addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			return "reservada"
		}
		return ""
	}
	for _, np := range specialV6 {
		if np.prefix.Contains(addr) {
			return np.class
		}
	}
	if !globalV6.Contains(addr) {
		return "reservada"
	}
	for _, np := range blockedV6 {
		if np.prefix.Contains(addr) {
			return np.class
		}
	}
	return ""
}

// parseLiteral reconoce una IP escrita como host, con o sin corchetes. Cualquier cosa que parezca
// una IP pero no lo sea (formas octales, hexadecimales o enteras que inet_aton aceptaria) se
// rechaza en vez de dejarla llegar a un resolvedor que la interprete distinto.
func parseLiteral(host string) (netip.Addr, bool, error) {
	h := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if h == "" {
		return netip.Addr{}, false, errInvalidHost
	}
	if addr, err := netip.ParseAddr(h); err == nil {
		if addr.Zone() != "" {
			return netip.Addr{}, false, errInvalidHost
		}
		return addr, true, nil
	}
	if strings.ContainsAny(h, ":[]") {
		return netip.Addr{}, false, errInvalidHost
	}
	if looksNumeric(h) {
		return netip.Addr{}, false, errInvalidHost
	}
	return netip.Addr{}, false, nil
}

// looksNumeric detecta hosts formados solo por digitos, x y puntos: 2130706433, 0x7f.1, 0177.0.0.1.
func looksNumeric(h string) bool {
	last := h[strings.LastIndexByte(h, '.')+1:]
	if last == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(last), "0x") {
		return true
	}
	for _, r := range last {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validHostname(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
			if !ok {
				return false
			}
		}
	}
	return true
}

// defaultResolver usa el DNS del sistema, sin proxies ni configuracion propia.
func defaultResolver() Resolver { return &net.Resolver{PreferGo: true} }
