// Package egress conecta el webmail con servidores de terceros cuya direccion dicta un correo: la baja
// en un clic y el proxy de imagenes remotas. La URL la escribe el remitente, asi que se trata como
// hostil (SSRF): el nombre se resuelve aqui y se rechaza si alguna de sus direcciones no es publica,
// se conecta a la direccion ya comprobada (un DNS que cambia entre la comprobacion y la conexion no
// cuela una IP interna) y el control del dialer vuelve a comprobar la direccion de cada conexion.
// Solo los puertos que admite quien lo usa.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

// Errores del destino. ErrRefused es un destino que no se admite (puerto, direccion no publica);
// ErrUnresolved, un nombre que no resuelve. net/http los conserva en la cadena de su error.
var (
	ErrRefused    = errors.New("destino no admitido")
	ErrUnresolved = errors.New("no se pudo resolver el servidor")
)

// Resolver resuelve un nombre; en produccion es net.DefaultResolver.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc abre una conexion con una direccion ip:puerto.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Options son lo que las pruebas sustituyen: el DNS, la conexion y la regla de direcciones. En
// produccion van vacias: net.DefaultResolver, un net.Dialer con Control y IsPublicAddr.
type Options struct {
	Resolver Resolver
	Dial     DialFunc
	Allowed  func(netip.Addr) bool
}

// Guard decide a que se puede conectar.
type Guard struct {
	resolver Resolver
	allowed  func(netip.Addr) bool
	ports    map[string]bool
	dial     DialFunc
}

// New crea el guardian para los puertos dados; timeout acota cada conexion.
func New(ports []string, timeout time.Duration, o Options) *Guard {
	g := &Guard{resolver: o.Resolver, allowed: o.Allowed, ports: make(map[string]bool, len(ports)), dial: o.Dial}
	for _, p := range ports {
		g.ports[p] = true
	}
	if g.resolver == nil {
		g.resolver = net.DefaultResolver
	}
	if g.allowed == nil {
		g.allowed = IsPublicAddr
	}
	if g.dial == nil {
		d := &net.Dialer{Timeout: timeout, Control: g.Control}
		g.dial = d.DialContext
	}
	return g
}

// DialContext resuelve el nombre, exige que TODAS sus direcciones sean publicas y conecta con la
// primera que responde, por su IP: la resolucion que se comprobo es la que se usa. Es el DialContext
// de un http.Transport.
func (g *Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !g.ports[port] {
		return nil, ErrRefused
	}
	addrs, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, a := range addrs {
		conn, err := g.dial(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		if errors.Is(err, ErrRefused) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !g.allowed(ip.Unmap()) {
			return nil, ErrRefused
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	addrs, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, ErrUnresolved
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: sin direcciones", ErrUnresolved)
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		a = a.Unmap()
		if !g.allowed(a) {
			return nil, ErrRefused
		}
		out = append(out, a)
	}
	return out, nil
}

// Control es la ultima comprobacion, sobre la direccion con la que el sistema va a conectar.
func (g *Guard) Control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !g.allowed(ap.Addr().Unmap()) || !g.ports[strconv.Itoa(int(ap.Port()))] {
		return ErrRefused
	}
	return nil
}

// blockedPrefixes son rangos que IsPrivate, IsLoopback y compania no cubren: red compartida de
// operadores, documentacion, pruebas de rendimiento, reservados y los prefijos de IPv6 que llevan
// dentro una IPv4 (NAT64, 6to4, Teredo) y podrian apuntar a una interna.
var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
	"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "2002::/16", "fec0::/10",
	// IPv4 compatible (obsoleta) y SIIT: tambien llevan una IPv4 dentro.
	"::/96", "::ffff:0:0:0/96",
)

func mustPrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, p := range list {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// IsPublicAddr dice si una direccion es unicast global y no pertenece a ningun rango privado,
// de loopback, de enlace local, ULA, multicast ni reservado.
func IsPublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
