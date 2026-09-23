// Package templatesclient renderiza plantillas en el servicio templates por su API
// interna, autenticada con el token del gateway.
package templatesclient

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
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
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
		http:    httpclient.New("templates", httpclient.Options{Timeout: 10 * time.Second, MaxAttempts: 2}),
	}
}

func (c *Client) Render(ctx context.Context, tenantID uuid.UUID, r ports.RenderRequest) (*ports.Rendered, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("TEMPLATES_URL no configurada")
	}
	body := map[string]any{
		"variables": r.Variables,
		"reserved":  r.Reserved,
	}
	if r.Variables == nil {
		body["variables"] = map[string]any{}
	}
	if r.Version != nil {
		body["version"] = *r.Version
	}
	if r.Test {
		body["test"] = true
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := c.baseURL + "/internal/templates/" + r.TemplateID.String() + "/render"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	// Renderizar no tiene efectos: es seguro repetirlo.
	resp, err := c.http.Do(httpclient.Idempotent(req))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrTemplatesUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, domain.ErrTemplateNotFound
	case resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusBadRequest:
		return nil, domain.NewValidationError("template: %s", errorMessage(resp.Body))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: status %d", domain.ErrTemplatesUnavailable, resp.StatusCode)
	}
	var out struct {
		Data struct {
			Subject string `json:"subject"`
			HTML    string `json:"html"`
			Text    string `json:"text"`
			Version int    `json:"version"`
			Kind    string `json:"kind"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: respuesta ilegible: %v", domain.ErrTemplatesUnavailable, err)
	}
	return &ports.Rendered{
		Subject: out.Data.Subject, HTML: out.Data.HTML, Text: out.Data.Text,
		Version: out.Data.Version, Kind: out.Data.Kind,
	}, nil
}

// errorMessage extrae el mensaje del envelope de error de pkg/response.
func errorMessage(body io.Reader) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 64<<10)).Decode(&env); err != nil || env.Error.Message == "" {
		return "la plantilla no acepto las variables"
	}
	return env.Error.Message
}
