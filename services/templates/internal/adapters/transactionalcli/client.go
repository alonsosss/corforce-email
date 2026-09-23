// Package transactionalcli pide a transactional el envio de prueba de una version de
// plantilla (POST /internal/transactional/test-send, token interno y empresa en X-Tenant-ID).
// transactional es el unico servicio que habla con SES: aplica el remitente verificado, la
// supresion, reputation y el carril de la clase de la plantilla.
package transactionalcli

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
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

const (
	testSendPath = "/internal/transactional/test-send"
	// callTimeout cubre el render de cada destinatario en templates y las consultas de
	// supresion y reputation que hace transactional antes de encolar.
	callTimeout      = 30 * time.Second
	maxResponseBytes = 256 << 10
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New: un solo intento. Enviar no es idempotente y un reintento podria duplicar la prueba.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("transactional-test-send", httpclient.Options{Timeout: callTimeout, MaxAttempts: 1}),
	}
}

type recipient struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type testSendBody struct {
	TemplateID      uuid.UUID                  `json:"template_id"`
	TemplateVersion int                        `json:"template_version"`
	From            recipient                  `json:"from"`
	ReplyTo         string                     `json:"reply_to,omitempty"`
	To              []string                   `json:"to"`
	Variables       map[string]json.RawMessage `json:"variables"`
	RequestedBy     uuid.UUID                  `json:"requested_by"`
}

type messageSummary struct {
	ID     uuid.UUID   `json:"id"`
	Status string      `json:"status"`
	To     []recipient `json:"to"`
}

type suppressed struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

type envelope struct {
	Data struct {
		Messages   []messageSummary `json:"messages"`
		Suppressed []suppressed     `json:"suppressed"`
	} `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) SendTest(ctx context.Context, tenantID uuid.UUID, r ports.TestSendRequest) (*ports.TestSendResult, error) {
	variables := r.Variables
	if variables == nil {
		variables = map[string]json.RawMessage{}
	}
	payload, err := json.Marshal(testSendBody{
		TemplateID: r.TemplateID, TemplateVersion: r.Version,
		From: recipient{Email: r.FromEmail, Name: r.FromName}, ReplyTo: r.ReplyTo,
		To: r.To, Variables: variables, RequestedBy: r.RequestedBy,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+testSendPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrTestSendUnavailable, err)
	}
	defer resp.Body.Close()

	var env envelope
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&env)
	switch {
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK:
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: respuesta ilegible: %v", domain.ErrTestSendUnavailable, decodeErr)
		}
		return toResult(env), nil
	case relayed(resp.StatusCode) && decodeErr == nil && env.Error.Code != "":
		return nil, &domain.TestSendRejectedError{
			Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message,
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	default:
		return nil, fmt.Errorf("%w: status %d", domain.ErrTestSendUnavailable, resp.StatusCode)
	}
}

// relayed son los rechazos de transactional que decide la peticion o la empresa y se
// devuelven tal cual. Un 401 (token interno) o un 404 son de la plataforma, no de quien
// prueba, y quedan como no disponible.
func relayed(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	}
	return false
}

func toResult(env envelope) *ports.TestSendResult {
	out := &ports.TestSendResult{
		Messages:   make([]ports.TestSendMessage, 0, len(env.Data.Messages)),
		Suppressed: make([]ports.TestSendSuppressed, 0, len(env.Data.Suppressed)),
	}
	for _, m := range env.Data.Messages {
		msg := ports.TestSendMessage{ID: m.ID, Status: m.Status}
		if len(m.To) > 0 {
			msg.Email = m.To[0].Email
		}
		out.Messages = append(out.Messages, msg)
	}
	for _, s := range env.Data.Suppressed {
		out.Suppressed = append(out.Suppressed, ports.TestSendSuppressed{Email: s.Email, Reason: s.Reason})
	}
	return out
}
