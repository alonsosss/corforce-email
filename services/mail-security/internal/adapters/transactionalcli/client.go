// Package transactionalcli envia el aviso de cuarentena por el envio interno de
// transactional (POST /internal/transactional/messages, purpose=quarantine_notice): token
// interno, empresa en X-Tenant-ID e Idempotency-Key por cabecera.
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
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
)

const (
	messagesPath            = "/internal/transactional/messages"
	purposeQuarantineNotice = "quarantine_notice"
	// statusSuppressed es el estado con que transactional deja constancia de un envio cuyo
	// unico destinatario estaba suprimido.
	statusSuppressed = "suppressed"
	maxResponseBytes = 1 << 20
	requestTimeout   = 30 * time.Second
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New: sin reintentos dentro de la llamada; reintenta el siguiente barrido con la misma
// clave de idempotencia.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("transactional", httpclient.Options{Timeout: requestTimeout, MaxAttempts: 1}),
	}
}

type address struct {
	Email string `json:"email"`
}

type messageRequest struct {
	From    address   `json:"from"`
	To      []address `json:"to"`
	Subject string    `json:"subject"`
	HTML    string    `json:"html"`
	Purpose string    `json:"purpose"`
}

type messageResponse struct {
	Data struct {
		Messages []struct {
			ID     uuid.UUID `json:"id"`
			Status string    `json:"status"`
		} `json:"messages"`
		Suppressed []json.RawMessage `json:"suppressed"`
	} `json:"data"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) SendQuarantineNotice(ctx context.Context, tenantID uuid.UUID, m domain.NoticeMail) (*ports.NoticeReceipt, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("%w: TRANSACTIONAL_URL sin configurar", ports.ErrNoticeUnavailable)
	}
	payload, err := json.Marshal(messageRequest{
		From: address{Email: m.From}, To: []address{{Email: m.To}},
		Subject: m.Subject, HTML: m.HTML, Purpose: purposeQuarantineNotice,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+messagesPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	req.Header.Set("Idempotency-Key", m.IdempotencyKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrNoticeUnavailable, err)
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxResponseBytes)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var env errorResponse
		_ = json.NewDecoder(body).Decode(&env)
		if transient(resp.StatusCode) {
			return nil, fmt.Errorf("%w: status %d %s %s", ports.ErrNoticeUnavailable, resp.StatusCode, env.Error.Code, env.Error.Message)
		}
		code := env.Error.Code
		if code == "" {
			code = fmt.Sprintf("HTTP_%d", resp.StatusCode)
		}
		return nil, &ports.NoticeRejectedError{Status: resp.StatusCode, Code: code, Message: env.Error.Message}
	}
	var out messageResponse
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: respuesta ilegible: %v", ports.ErrNoticeUnavailable, err)
	}
	if len(out.Data.Messages) == 0 {
		return nil, fmt.Errorf("%w: respuesta sin mensajes", ports.ErrNoticeUnavailable)
	}
	msg := out.Data.Messages[0]
	id := msg.ID
	return &ports.NoticeReceipt{MessageID: &id, Status: msg.Status, Suppressed: msg.Status == statusSuppressed}, nil
}

// transient: lo que puede salir bien repitiendo la misma peticion mas tarde. 429 es una
// espera de reputation; 401 es la credencial interna mal configurada, que se corrige en
// despliegue y no debe dar por avisado a nadie; 408 y 5xx, caidas.
func transient(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusUnauthorized ||
		status == http.StatusRequestTimeout || status >= 500
}
