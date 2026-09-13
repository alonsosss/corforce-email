// Package suppressionclient consulta y alimenta la lista de supresion (servicio
// suppression) por su API interna, autenticada con el token del gateway.
package suppressionclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("suppression", httpclient.Options{Timeout: 5 * time.Second, MaxAttempts: 3}),
	}
}

func (c *Client) Check(ctx context.Context, tenantID uuid.UUID, emails []string) ([]ports.Suppressed, error) {
	if len(emails) == 0 {
		return []ports.Suppressed{}, nil
	}
	var out struct {
		Data struct {
			Suppressed []ports.Suppressed `json:"suppressed"`
		} `json:"data"`
	}
	if err := c.post(ctx, tenantID, "/internal/suppression/check", map[string]any{"emails": emails}, &out); err != nil {
		return nil, err
	}
	if out.Data.Suppressed == nil {
		return []ports.Suppressed{}, nil
	}
	return out.Data.Suppressed, nil
}

func (c *Client) Add(ctx context.Context, tenantID uuid.UUID, entry ports.SuppressionEntry) error {
	return c.post(ctx, tenantID, "/internal/suppression/add", entry, nil)
}

// post envia un JSON y decodifica la respuesta en out (si no es nil). Las dos
// operaciones son idempotentes por contrato (consulta y alta por direccion), asi que se
// marcan como tales para que el cliente compartido pueda reintentarlas.
func (c *Client) post(ctx context.Context, tenantID uuid.UUID, path string, body, out any) error {
	if c.baseURL == "" {
		return fmt.Errorf("SUPPRESSION_URL no configurada")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	resp, err := c.http.Do(httpclient.Idempotent(req))
	if err != nil {
		return fmt.Errorf("suppression: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("suppression: status %d", resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("suppression: respuesta ilegible: %w", err)
	}
	return nil
}
