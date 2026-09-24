// Package queueagent habla con el agente de la cola de Postfix de la celda (deploy/mail/postfix/queue-agent):
// listar, reintentar, retener, liberar y borrar mensajes, y vaciar la cola diferida.
package queueagent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

const (
	defaultTimeout = 30 * time.Second
	// maxResponseBytes acota la respuesta del agente: 500 mensajes con sus destinatarios caben de sobra.
	maxResponseBytes = 16 << 20
	maxErrorBytes    = 4 << 10
)

// apiKeyPattern es la forma de QUEUE_AGENT_API_KEY: la misma regla que DOVEADM_API_KEY.
var apiKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`)

// Config describe el agente de la cola de la celda.
type Config struct {
	// BaseURL es https://host[:puerto], ya validada con config.ServiceURL y nunca en claro: la clave
	// viaja en cada peticion.
	BaseURL string
	APIKey  string
	// ServerName es el nombre que presenta el certificado del agente (el de MAIL_HOSTNAME).
	ServerName string
	// CAFile suma una CA propia a las del sistema.
	CAFile  string
	Timeout time.Duration
}

// Client implementa ports.EngineQueue.
type Client struct {
	base          string
	authorization string
	http          *http.Client
}

func New(cfg Config) (*Client, error) {
	if !strings.HasPrefix(cfg.BaseURL, "https://") {
		return nil, errors.New("QUEUE_AGENT_URL debe ser https: la clave del agente viaja en cada petición")
	}
	if !apiKeyPattern.MatchString(cfg.APIKey) {
		return nil, errors.New("QUEUE_AGENT_API_KEY debe tener de 32 a 256 caracteres de [A-Za-z0-9_-]")
	}
	serverName := strings.TrimSpace(cfg.ServerName)
	if serverName == "" {
		return nil, errors.New("falta el nombre del certificado del agente (QUEUE_AGENT_TLS_SERVER_NAME o MAIL_HOSTNAME)")
	}
	tlsCfg, err := config.ClientTLS(serverName, strings.TrimSpace(cfg.CAFile))
	if err != nil {
		return nil, fmt.Errorf("TLS del agente de la cola: %w", err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	transport := &http.Transport{
		// Sin proxy: la clave no sale de la red de la celda.
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		base:          strings.TrimRight(cfg.BaseURL, "/") + "/v1/queue",
		authorization: "Bearer " + cfg.APIKey,
		http: &http.Client{
			Timeout:       timeout,
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) List(ctx context.Context, limit int) (domain.QueueListing, error) {
	var out domain.QueueListing
	if err := c.do(ctx, http.MethodGet, c.base+"?"+url.Values{"limit": {strconv.Itoa(limit)}}.Encode(), &out); err != nil {
		return domain.QueueListing{}, err
	}
	if out.Items == nil {
		out.Items = []domain.QueueMessage{}
	}
	return out, nil
}

// Apply hace action sobre el mensaje id. El identificador ya se valido en el caso de uso y el agente lo
// vuelve a validar; aqui se codifica igualmente para que ningun valor cambie la ruta.
func (c *Client) Apply(ctx context.Context, action domain.QueueAction, id string) error {
	return c.do(ctx, http.MethodPost, c.base+"/"+url.PathEscape(id)+"/"+url.PathEscape(string(action)), nil)
}

func (c *Client) Flush(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, c.base+"/flush", nil)
}

// do hace la peticion y traduce el resultado a los errores del dominio. out, si no es nil, recibe el
// cuerpo JSON de una respuesta 200.
func (c *Client) do(ctx context.Context, method, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrEngineCommand, err)
	}
	req.Header.Set("Authorization", c.authorization)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return fmt.Errorf("%w: certificado del agente de la cola: %v", domain.ErrEngineRejected, err)
		}
		return fmt.Errorf("%w: %v", domain.ErrEngineUnreachable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return domain.ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: HTTP %d (QUEUE_AGENT_API_KEY distinta en Postfix y en mail-security)", domain.ErrEngineRejected, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("%w: HTTP %d", domain.ErrEngineUnreachable, resp.StatusCode)
	default:
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		return fmt.Errorf("%w: HTTP %d %s", domain.ErrEngineCommand, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w: leer la respuesta: %v", domain.ErrEngineUnreachable, err)
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("%w: respuesta de más de %d bytes", domain.ErrEngineCommand, maxResponseBytes)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: respuesta ilegible: %v", domain.ErrEngineCommand, err)
	}
	return nil
}
