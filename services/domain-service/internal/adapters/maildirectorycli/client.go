package maildirectorycli

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
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

// Client activa y desactiva dominios en el directorio de la celda a traves de
// mail-directory. Es una llamada interna: viaja con el token de gateway y la empresa
// en cabecera, como el resto de llamadas servicio-a-servicio.
type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpclient.New("mail-directory", httpclient.Options{Timeout: 5 * time.Second, MaxAttempts: 3}),
	}
}

// SetActivation hace PUT /internal/mail-directory/domains/{domain}/activation. Un 409
// significa que el dominio conserva buzones y no puede desactivarse.
func (c *Client) SetActivation(ctx context.Context, tenantID uuid.UUID, name string, active bool) error {
	body, err := json.Marshal(map[string]bool{"active": active})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/internal/mail-directory/domains/%s/activation", c.baseURL, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	// GetBody permite a pkg/httpclient rebobinar el cuerpo en un reintento.
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	req.Header.Set("Content-Type", "application/json")
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
	case resp.StatusCode == http.StatusConflict:
		return domain.ErrDomainHasMailboxes
	case resp.StatusCode == http.StatusNotFound && !active:
		// Nada que desactivar: el directorio nunca tuvo el dominio.
		return nil
	default:
		return fmt.Errorf("mail-directory: activation de %s respondio %d", name, resp.StatusCode)
	}
}
