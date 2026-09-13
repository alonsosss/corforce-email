// Package suppressionclient lee de suppression las causas vigentes de una o varias
// direcciones por su API interna (POST /internal/suppression/check), autenticada con el
// token interno.
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
	// callTimeout acota la consulta, que para una sola direccion se hace con la fila del
	// contacto bloqueada.
	callTimeout = 5 * time.Second
	// maxResponseBytes: la respuesta de una sola direccion cabe con holgura.
	maxResponseBytes = 64 << 10
	// checkBatch son las direcciones de cada consulta en bloque, por debajo del tope de
	// suppression (max_check_emails, 1000). maxBatchResponseBytes cabe una tanda entera
	// suprimida con todas sus causas.
	checkBatch            = 500
	maxBatchResponseBytes = 4 << 20
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New: un solo intento por consulta. Si suppression no responde, el evento se queda sin
// confirmar y lo reentrega JetStream, y el barrido lo reintenta en su siguiente pasada;
// reintentar aqui alargaria el bloqueo de la fila.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("suppression", httpclient.Options{Timeout: callTimeout, MaxAttempts: 1}),
	}
}

type checkItem struct {
	Email   string   `json:"email"`
	Reason  string   `json:"reason"`
	Reasons []string `json:"reasons"`
	Causes  []struct {
		Reason    string    `json:"reason"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"causes"`
}

type checkResponse struct {
	Data struct {
		Suppressed []checkItem `json:"suppressed"`
	} `json:"data"`
}

// ActiveCauses consulta una sola direccion ya normalizada.
func (c *Client) ActiveCauses(ctx context.Context, tenantID uuid.UUID, email string) ([]domain.ActiveCause, error) {
	items, err := c.check(ctx, tenantID, []string{email}, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	switch len(items) {
	case 0:
		return []domain.ActiveCause{}, nil
	case 1:
	default:
		return nil, fmt.Errorf("suppression: %d resultados para una sola direccion", len(items))
	}
	if items[0].Email != email {
		return nil, fmt.Errorf("suppression: respondio por otra direccion")
	}
	return items[0].activeCauses()
}

// ActiveCausesOf consulta varias direcciones ya normalizadas, en tandas de checkBatch.
// Una respuesta por una direccion que no se pidio, o repetida, es un error: leerla como
// libre o como de otro contacto pondria un estado que suppression no dice.
func (c *Client) ActiveCausesOf(ctx context.Context, tenantID uuid.UUID, emails []string) (map[string][]domain.ActiveCause, error) {
	out := make(map[string][]domain.ActiveCause)
	for start := 0; start < len(emails); start += checkBatch {
		chunk := emails[start:min(start+checkBatch, len(emails))]
		asked := make(map[string]bool, len(chunk))
		for _, e := range chunk {
			asked[e] = true
		}
		items, err := c.check(ctx, tenantID, chunk, maxBatchResponseBytes)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if !asked[item.Email] {
				return nil, fmt.Errorf("suppression: respondio por una direccion que no se pidio")
			}
			if _, dup := out[item.Email]; dup {
				return nil, fmt.Errorf("suppression: direccion repetida en la respuesta")
			}
			causes, err := item.activeCauses()
			if err != nil {
				return nil, err
			}
			out[item.Email] = causes
		}
	}
	return out, nil
}

func (c *Client) check(ctx context.Context, tenantID uuid.UUID, emails []string, limit int64) ([]checkItem, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("suppression: SUPPRESSION_URL no configurada")
	}
	payload, err := json.Marshal(map[string][]string{"emails": emails})
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
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, limit))
		return nil, fmt.Errorf("suppression: status %d", resp.StatusCode)
	}
	var out checkResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(&out); err != nil {
		return nil, fmt.Errorf("suppression: respuesta ilegible: %w", err)
	}
	return out.Data.Suppressed, nil
}

// activeCauses lee las causas de una direccion que suppression devuelve como suprimida,
// que nunca se lee como libre: sin reasons (una replica anterior) su causa es reason, y
// sin ninguna de las dos la respuesta es un error. La hora de cada causa sale de causes;
// sin causes (una replica anterior) queda en cero, y un causes que no nombra exactamente
// las causas de reasons es un error.
func (item checkItem) activeCauses() ([]domain.ActiveCause, error) {
	raw := item.Reasons
	if len(raw) == 0 && item.Reason != "" {
		raw = []string{item.Reason}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("suppression: direccion suprimida sin causa")
	}
	var since map[string]time.Time
	if item.Causes != nil {
		since = make(map[string]time.Time, len(item.Causes))
		for _, cause := range item.Causes {
			since[cause.Reason] = cause.CreatedAt
		}
		if len(item.Causes) != len(raw) || len(since) != len(raw) {
			return nil, fmt.Errorf("suppression: causes no coincide con reasons")
		}
	}
	causes := make([]domain.ActiveCause, len(raw))
	for i, r := range raw {
		causes[i].Cause = domain.SuppressionCause(r)
		if since == nil {
			continue
		}
		at, ok := since[r]
		if !ok {
			return nil, fmt.Errorf("suppression: causes no coincide con reasons")
		}
		causes[i].RegisteredAt = at
	}
	return causes, nil
}
