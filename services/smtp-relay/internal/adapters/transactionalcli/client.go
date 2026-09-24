// Package transactionalcli entrega el mensaje a transactional por POST
// /internal/transactional/raw-messages con el token interno y la empresa de la clave, y traduce su
// respuesta a los rechazos del relay.
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
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
)

const rawPath = "/internal/transactional/raw-messages"

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New crea el cliente. Un solo intento: el relay responde 4xx al cliente SMTP, que reintenta con
// la misma clave de idempotencia, y repetir aqui un envio de 40 MB solo alargaria la sesion.
func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpclient.New("transactional", httpclient.Options{Timeout: timeout, MaxAttempts: 1}),
	}
}

type request struct {
	EnvelopeFrom   string   `json:"envelope_from"`
	Recipients     []string `json:"recipients"`
	Raw            []byte   `json:"raw"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
	APIKeyID       string   `json:"api_key_id,omitempty"`
}

type accepted struct {
	Data struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	} `json:"data"`
}

type failure struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (c *Client) Submit(ctx context.Context, cred domain.Credential, env domain.Envelope, raw []byte, idempotencyKey string) (string, error) {
	body, err := json.Marshal(request{
		EnvelopeFrom: env.From, Recipients: env.Recipients, Raw: raw, IdempotencyKey: idempotencyKey, APIKeyID: cred.KeyID,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUpstream, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+rawPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUpstream, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", cred.TenantID)
	if c.token != "" {
		req.Header.Set("X-Gateway-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUpstream, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUpstream, err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		var ok accepted
		if err := json.Unmarshal(payload, &ok); err != nil || len(ok.Data.Messages) == 0 {
			return "", fmt.Errorf("%w: respuesta de transactional ilegible", domain.ErrUpstream)
		}
		return ok.Data.Messages[0].ID, nil
	}
	var f failure
	_ = json.Unmarshal(payload, &f)
	return "", Classify(resp.StatusCode, f.Error.Code)
}

// Classify traduce la respuesta de transactional. Lo que depende del mensaje o de la empresa es
// permanente; lo que depende de que un servicio responda, temporal.
func Classify(status int, code string) error {
	switch {
	case code == "SENDING_DOMAIN_NOT_VERIFIED":
		return domain.ErrSenderNotVerified
	case status == http.StatusRequestEntityTooLarge || code == "MESSAGE_TOO_LARGE":
		return domain.ErrTooLarge
	case strings.HasPrefix(code, "MESSAGE_"):
		return domain.ErrMalformed
	case code == "RATE_LIMITED":
		return domain.ErrSendingThrottled
	case code == "SENDING_RESTRICTED" || code == "PLAN_LIMIT_REACHED":
		return domain.ErrSendingDenied
	case status == http.StatusUnprocessableEntity || status == http.StatusBadRequest || status == http.StatusConflict:
		return domain.ErrRejected
	}
	return fmt.Errorf("%w: transactional respondio %d %s", domain.ErrUpstream, status, code)
}
