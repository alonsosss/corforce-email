// Package unsubscribe hace la baja en un clic de RFC 8058: una de las peticiones que el webmail envia
// a un servidor de terceros, y solo por una accion explicita del usuario.
//
// La URL la dicta el remitente del boletin, asi que se trata como hostil (SSRF): solo https al
// puerto 443 y solo a direcciones publicas comprobadas en cada conexion (adapters/egress). Sin proxy
// del entorno, sin redirecciones, sin cookies y con tiempo, cabeceras y cuerpo acotados.
package unsubscribe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/egress"
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

// options son lo que las pruebas del paquete sustituyen: el DNS, la conexion y la regla de
// direcciones (egress.Options) y la confianza TLS. Fuera del paquete no se pueden tocar.
type options struct {
	egress  egress.Options
	rootCAs *x509.CertPool
}

// Client implementa ports.Unsubscriber.
type Client struct {
	http    *http.Client
	guard   *egress.Guard
	timeout time.Duration
}

// New crea el cliente; timeout <= 0 usa el plazo por defecto (10 s) para toda la peticion.
func New(timeout time.Duration) *Client {
	return newClient(timeout, options{})
}

func newClient(timeout time.Duration, o options) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	c := &Client{timeout: timeout, guard: egress.New([]string{httpsPort}, timeout, o.egress)}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            c.guard.DialContext,
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
		if errors.Is(err, egress.ErrRefused) {
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

// redact quita la URL del error de red: puede llevar un token del destinatario.
func redact(err error, u *url.URL) string {
	return strings.ReplaceAll(err.Error(), u.String(), u.Scheme+"://"+u.Host)
}
