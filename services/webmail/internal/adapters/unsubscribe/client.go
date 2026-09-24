// Package unsubscribe hace la baja en un clic de RFC 8058: la unica peticion que el webmail envia a
// un servidor de terceros, y solo por una accion explicita del usuario.
//
// La URL la dicta el remitente del boletin, asi que se trata como hostil (SSRF): solo https al
// puerto 443, el nombre se resuelve aqui y se rechaza si alguna de sus direcciones no es publica,
// se conecta a la direccion ya comprobada (un DNS que cambia entre la comprobacion y la conexion no
// cuela una IP interna) y el dialer vuelve a comprobar la direccion de cada conexion. Sin proxy del
// entorno, sin redirecciones, sin cookies y con tiempo, cabeceras y cuerpo acotados.
package unsubscribe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	// oneClickBody es el cuerpo exacto que fija RFC 8058.
	oneClickBody        = "List-Unsubscribe=One-Click"
	maxResponseBytes    = 64 << 10
	maxHeaderBytes      = 16 << 10
	defaultTimeout      = 10 * time.Second
	tlsHandshakeTimeout = 5 * time.Second
	httpsPort           = "443"
	userAgent           = "CoreForceMail-Unsubscribe/1.0"
)

// resolver resuelve un nombre; en produccion es net.DefaultResolver.
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// options son lo que las pruebas del paquete sustituyen: el DNS, la conexion, la confianza TLS y la
// regla de direcciones. Fuera del paquete no se pueden tocar.
type options struct {
	resolver resolver
	dial     func(ctx context.Context, network, address string) (net.Conn, error)
	rootCAs  *x509.CertPool
	allowed  func(netip.Addr) bool
}

// Client implementa ports.Unsubscriber.
type Client struct {
	http     *http.Client
	resolver resolver
	allowed  func(netip.Addr) bool
	timeout  time.Duration
}

// New crea el cliente; timeout <= 0 usa el plazo por defecto (10 s) para toda la peticion.
func New(timeout time.Duration) *Client {
	return newClient(timeout, options{})
}

func newClient(timeout time.Duration, o options) *Client {
	c := &Client{resolver: o.resolver, allowed: o.allowed, timeout: timeout}
	if c.resolver == nil {
		c.resolver = net.DefaultResolver
	}
	if c.allowed == nil {
		c.allowed = IsPublicAddr
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	dial := o.dial
	if dial == nil {
		d := &net.Dialer{Timeout: c.timeout, Control: c.controlConn}
		dial = d.DialContext
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            c.dialer(dial),
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.rootCAs},
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ResponseHeaderTimeout:  c.timeout,
		MaxResponseHeaderBytes: maxHeaderBytes,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
	}
	c.http = &http.Client{
		Transport: transport,
		Timeout:   c.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c
}

// OneClick envia el POST de RFC 8058. Una respuesta 2xx confirma la baja; una 3xx tambien (el
// servidor la recibio y propone una pagina que no se visita). Cualquier otra no.
func (c *Client) OneClick(ctx context.Context, target string) error {
	u, err := domain.ValidateUnsubscribeURL(target)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(oneClickBody))
	if err != nil {
		return domain.ErrUnsubscribeRefused
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, domain.ErrUnsubscribeRefused) {
			return domain.ErrUnsubscribeRefused
		}
		return fmt.Errorf("%w: %v", domain.ErrUnsubscribeFailed, redact(err, u))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("%w: el servidor respondió %d", domain.ErrUnsubscribeFailed, resp.StatusCode)
	}
	return nil
}

// dialer resuelve el nombre, exige que TODAS sus direcciones sean publicas y conecta con la
// primera que responde, por su IP: la resolucion que se comprobo es la que se usa.
func (c *Client) dialer(dial func(ctx context.Context, network, address string) (net.Conn, error)) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != httpsPort {
			return nil, domain.ErrUnsubscribeRefused
		}
		addrs, err := c.resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, a := range addrs {
			conn, err := dial(ctx, network, net.JoinHostPort(a.String(), port))
			if err == nil {
				return conn, nil
			}
			if errors.Is(err, domain.ErrUnsubscribeRefused) {
				return nil, err
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

func (c *Client) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !c.allowed(ip.Unmap()) {
			return nil, domain.ErrUnsubscribeRefused
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	addrs, err := c.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("%w: no se pudo resolver el servidor de baja", domain.ErrUnsubscribeFailed)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: el servidor de baja no tiene direcciones", domain.ErrUnsubscribeFailed)
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		a = a.Unmap()
		if !c.allowed(a) {
			return nil, domain.ErrUnsubscribeRefused
		}
		out = append(out, a)
	}
	return out, nil
}

// controlConn es la ultima comprobacion, sobre la direccion con la que el sistema va a conectar.
func (c *Client) controlConn(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !c.allowed(ap.Addr().Unmap()) {
		return domain.ErrUnsubscribeRefused
	}
	return nil
}

// redact quita la URL del error de red: puede llevar un token del destinatario.
func redact(err error, u *url.URL) string {
	return strings.ReplaceAll(err.Error(), u.String(), u.Scheme+"://"+u.Host)
}

// blockedPrefixes son rangos que IsPrivate, IsLoopback y compania no cubren: red compartida de
// operadores, documentacion, pruebas de rendimiento, reservados y los prefijos de IPv6 que llevan
// dentro una IPv4 (NAT64, 6to4, Teredo) y podrian apuntar a una interna.
var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
	"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "2002::/16", "fec0::/10",
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
