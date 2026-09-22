// Package transactionalcli envia el informe de anclas por el correo de la propia plataforma de
// transactional (POST /internal/send-email, el mismo contrato que usa identity para el reinicio
// de contrasena): token interno, la empresa de plataforma en X-Tenant-ID, y el remitente lo pone
// transactional (PLATFORM_FROM_EMAIL). Sale en texto plano, sin pasar por reputation.
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
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
)

const (
	sendPath = "/internal/send-email"
	// statusSuppressed es el estado con que transactional deja constancia de un envio cuyo
	// destinatario estaba suprimido.
	statusSuppressed = "suppressed"
	maxResponseBytes = 1 << 20
	requestTimeout   = 30 * time.Second
)

type Client struct {
	baseURL        string
	token          string
	platformTenant uuid.UUID
	http           *httpclient.Client
}

// New: sin reintentos dentro de la llamada; el informe periodico vuelve a salir en el siguiente
// intervalo y el aviso de rotura, en la siguiente verificacion.
func New(baseURL, token string, platformTenant uuid.UUID) *Client {
	return &Client{
		baseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:          token,
		platformTenant: platformTenant,
		http:           httpclient.New("transactional", httpclient.Options{Timeout: requestTimeout, MaxAttempts: 1}),
	}
}

type sendRequest struct {
	To       string `json:"to"`
	Subject  string `json:"subject"`
	TextBody string `json:"text_body"`
}

type sendResponse struct {
	Data struct {
		MessageID uuid.UUID `json:"message_id"`
		Status    string    `json:"status"`
	} `json:"data"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) SendAnchorReport(ctx context.Context, to, subject, text string) (bool, error) {
	payload, err := json.Marshal(sendRequest{To: to, Subject: subject, TextBody: text})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+sendPath, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", c.platformTenant.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ports.ErrReportUnavailable, err)
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxResponseBytes)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var env errorResponse
		_ = json.NewDecoder(body).Decode(&env)
		if transient(resp.StatusCode) {
			return false, fmt.Errorf("%w: status %d %s %s", ports.ErrReportUnavailable, resp.StatusCode, env.Error.Code, env.Error.Message)
		}
		code := env.Error.Code
		if code == "" {
			code = fmt.Sprintf("HTTP_%d", resp.StatusCode)
		}
		return false, &ports.ReportRejectedError{Status: resp.StatusCode, Code: code, Message: env.Error.Message}
	}
	var out sendResponse
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return false, fmt.Errorf("%w: respuesta ilegible: %v", ports.ErrReportUnavailable, err)
	}
	if out.Data.Status == "" {
		return false, fmt.Errorf("%w: respuesta sin estado", ports.ErrReportUnavailable)
	}
	return out.Data.Status == statusSuppressed, nil
}

// transient: lo que puede salir bien repitiendo la misma peticion mas tarde. 429 es una
// espera; 401 es la credencial interna mal configurada, que se corrige en despliegue y no
// debe darse por rechazo definitivo; 408 y 5xx, caidas.
func transient(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusUnauthorized ||
		status == http.StatusRequestTimeout || status >= 500
}
