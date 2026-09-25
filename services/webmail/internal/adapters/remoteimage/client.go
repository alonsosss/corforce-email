// Package remoteimage descarga las imagenes remotas que sirve el proxy de imagenes del webmail. La URL
// la escribio el remitente del correo: se trata como hostil igual que la baja en un clic
// (adapters/egress), con http y https en sus puertos por defecto, unas pocas redirecciones que se
// vuelven a validar una a una, y nada que identifique al lector: sin cookies, sin credenciales, sin
// Referer y con un User-Agent fijo de la plataforma.
package remoteimage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/egress"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	// MaxRedirects es cuantas redirecciones se siguen; cada salto se valida como la URL original.
	MaxRedirects        = 3
	maxHeaderBytes      = 16 << 10
	tlsHandshakeTimeout = 5 * time.Second
	idleConnTimeout     = 30 * time.Second
	maxIdleConnsPerHost = 2
	userAgent           = "CoreForceMail-ImageProxy/1.0"
	accept              = "image/png,image/jpeg,image/gif,image/webp"
)

// ports son los de http y https por defecto: los unicos que admite domain.ValidateRemoteImageURL.
var ports = []string{"80", "443"}

var errTooManyRedirects = errors.New("demasiadas redirecciones")

// Config acota cada descarga: Timeout de principio a fin (redirecciones y cuerpo incluidos) y
// MaxBytes del cuerpo ya descomprimido.
type Config struct {
	Timeout  time.Duration
	MaxBytes int64
}

// options son lo que las pruebas del paquete sustituyen: el DNS, la conexion, la regla de direcciones
// y la confianza TLS.
type options struct {
	egress  egress.Options
	rootCAs *x509.CertPool
}

// Client implementa ports.RemoteImageFetcher.
type Client struct {
	http *http.Client
	cfg  Config
}

// New crea el cliente; Timeout y MaxBytes deben ser positivos.
func New(cfg Config) (*Client, error) {
	return newClient(cfg, options{})
}

func newClient(cfg Config, o options) (*Client, error) {
	if cfg.Timeout <= 0 || cfg.MaxBytes <= 0 {
		return nil, errors.New("remoteimage: el plazo y el tope de bytes deben ser positivos")
	}
	guard := egress.New(ports, cfg.Timeout, o.egress)
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            guard.DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.rootCAs},
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ResponseHeaderTimeout:  cfg.Timeout,
		MaxResponseHeaderBytes: maxHeaderBytes,
		MaxIdleConnsPerHost:    maxIdleConnsPerHost,
		IdleConnTimeout:        idleConnTimeout,
		ForceAttemptHTTP2:      true,
	}
	return &Client{cfg: cfg, http: &http.Client{
		Transport:     transport,
		Timeout:       cfg.Timeout,
		CheckRedirect: checkRedirect,
	}}, nil
}

// checkRedirect valida cada salto con las reglas de la URL original y quita el Referer que net/http
// pone al redirigir: el servidor de destino no sabe de donde viene la peticion.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return errTooManyRedirects
	}
	if _, err := domain.ValidateRemoteImageURL(req.URL.String()); err != nil {
		return err
	}
	req.Header.Del("Referer")
	return nil
}

// Fetch descarga la imagen de rawURL (ya validada al firmar el enlace; se vuelve a validar). Los
// errores no llevan la URL: puede incluir un identificador del destinatario del correo.
func (c *Client) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := domain.ValidateRemoteImageURL(rawURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, domain.ErrRemoteImageRefused
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classify(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: el servidor respondió %d", domain.ErrRemoteImageUnavailable, resp.StatusCode)
	}
	if resp.ContentLength > c.cfg.MaxBytes {
		return nil, domain.ErrRemoteImageTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: cuerpo interrumpido", domain.ErrRemoteImageUnavailable)
	}
	if int64(len(data)) > c.cfg.MaxBytes {
		return nil, domain.ErrRemoteImageTooLarge
	}
	return data, nil
}

// classify traduce el error de net/http sin copiar su texto, que lleva la URL.
func classify(err error) error {
	var timeout interface{ Timeout() bool }
	switch {
	case errors.Is(err, egress.ErrRefused), errors.Is(err, domain.ErrRemoteImageRefused):
		return domain.ErrRemoteImageRefused
	case errors.Is(err, errTooManyRedirects):
		return fmt.Errorf("%w: %v", domain.ErrRemoteImageUnavailable, errTooManyRedirects)
	case errors.Is(err, egress.ErrUnresolved):
		return fmt.Errorf("%w: %v", domain.ErrRemoteImageUnavailable, egress.ErrUnresolved)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return fmt.Errorf("%w: plazo agotado", domain.ErrRemoteImageUnavailable)
	default:
		return fmt.Errorf("%w: fallo de red", domain.ErrRemoteImageUnavailable)
	}
}
