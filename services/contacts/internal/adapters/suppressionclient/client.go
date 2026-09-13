// Package suppressionclient lee de suppression las causas vigentes de una direccion por
// su API interna (POST /internal/suppression/check), autenticada con el token interno.
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
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

const (
	checkPath = "/internal/suppression/check"
	// callTimeout acota la consulta, que se hace con la fila del contacto bloqueada.
	callTimeout = 5 * time.Second
	// maxResponseBytes: la respuesta de una sola direccion cabe con holgura.
	maxResponseBytes = 64 << 10
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New: un solo intento por consulta. Si suppression no responde, el evento se queda sin
// confirmar y lo reentrega JetStream; reintentar aqui alargaria el bloqueo de la fila.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("suppression", httpclient.Options{Timeout: callTimeout, MaxAttempts: 1}),
	}
}

type checkResponse struct {
	Data struct {
		Suppressed []struct {
			Email   string   `json:"email"`
			Reason  string   `json:"reason"`
			Reasons []string `json:"reasons"`
		} `json:"suppressed"`
	} `json:"data"`
}

// ActiveCauses consulta una sola direccion ya normalizada. Una direccion que suppression
// devuelve como suprimida nunca se lee como libre: sin reasons (una replica anterior) su
// causa es reason, y sin ninguna de las dos la respuesta es un error.
func (c *Client) ActiveCauses(ctx context.Context, tenantID uuid.UUID, email string) ([]domain.SuppressionCause, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("suppression: SUPPRESSION_URL no configurada")
	}
	payload, err := json.Marshal(map[string][]string{"emails": {email}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+checkPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("suppression: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return nil, fmt.Errorf("suppression: status %d", resp.StatusCode)
	}
	var out checkResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("suppression: respuesta ilegible: %w", err)
	}
	switch len(out.Data.Suppressed) {
	case 0:
		return []domain.SuppressionCause{}, nil
	case 1:
	default:
		return nil, fmt.Errorf("suppression: %d resultados para una sola direccion", len(out.Data.Suppressed))
	}
	item := out.Data.Suppressed[0]
	if item.Email != email {
		return nil, fmt.Errorf("suppression: respondio por otra direccion")
	}
	raw := item.Reasons
	if len(raw) == 0 && item.Reason != "" {
		raw = []string{item.Reason}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("suppression: direccion suprimida sin causa")
	}
	causes := make([]domain.SuppressionCause, len(raw))
	for i, r := range raw {
		causes[i] = domain.SuppressionCause(r)
	}
	return causes, nil
}
