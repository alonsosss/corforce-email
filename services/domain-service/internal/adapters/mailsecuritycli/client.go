package mailsecuritycli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
)

// Client entrega las claves DKIM a mail-security, que las escribe en el Redis de los
// motores (DKIM_PRIV_KEYS y DKIM_SELECTORS). La clave privada viaja solo en el cuerpo
// de esta llamada interna y no se registra en ningun log.
type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpclient.New("mail-security", httpclient.Options{Timeout: 5 * time.Second, MaxAttempts: 3}),
	}
}

// PublishDKIM hace un PUT /internal/mail-security/dkim/{domain} por clave, en orden: cada
// PUT deja el selector como el activo del dominio, asi que la ultima clave es la que firma.
func (c *Client) PublishDKIM(ctx context.Context, tenantID uuid.UUID, name string, keys []ports.DKIMKey) error {
	for _, key := range keys {
		body, err := json.Marshal(map[string]string{
			"selector":        key.Selector,
			"private_key_pem": key.PrivateKeyPEM,
		})
		if err != nil {
			return err
		}
		if err := c.do(ctx, tenantID, http.MethodPut, c.dkimPath(name), body); err != nil {
			return fmt.Errorf("publicar selector %s: %w", key.Selector, err)
		}
	}
	return nil
}

// RetireDKIM hace DELETE /internal/mail-security/dkim/{domain}/{selector}.
func (c *Client) RetireDKIM(ctx context.Context, tenantID uuid.UUID, name, selector string) error {
	return c.do(ctx, tenantID, http.MethodDelete, c.dkimPath(name)+"/"+url.PathEscape(selector), nil)
}

// DeleteDKIM hace DELETE /internal/mail-security/dkim/{domain}: retira todos los selectores.
func (c *Client) DeleteDKIM(ctx context.Context, tenantID uuid.UUID, name string) error {
	return c.do(ctx, tenantID, http.MethodDelete, c.dkimPath(name), nil)
}

func (c *Client) dkimPath(name string) string {
	return c.baseURL + "/internal/mail-security/dkim/" + url.PathEscape(name)
}

// do ejecuta la llamada. PUT y DELETE son idempotentes y pkg/httpclient los reintenta;
// un 404 en DELETE cuenta como hecho.
func (c *Client) do(ctx context.Context, tenantID uuid.UUID, method, endpoint string, body []byte) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound && method == http.MethodDelete:
		return nil
	default:
		return fmt.Errorf("mail-security: %s %s respondio %d", method, endpoint, resp.StatusCode)
	}
}
