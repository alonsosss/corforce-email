// Package reputationclient pide a reputation la autorizacion previa a un envio por su API
// interna, autenticada con el token del gateway.
package reputationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

const (
	// timeout acota la autorizacion: esta en el camino de cada peticion de envio.
	timeout = 3 * time.Second
	// maxRetryAfterSeconds acota la espera que se traslada al llamador: un valor
	// desmedido no puede dejar a una campana parada indefinidamente.
	maxRetryAfterSeconds = 24 * 60 * 60
	maxResponseBytes     = 64 << 10
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New arma el cliente. Un solo intento: reputation puede descontar el cupo de la tasa al
// autorizar, y repetir la llamada lo descontaria dos veces.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("reputation", httpclient.Options{Timeout: timeout, MaxAttempts: 1}),
	}
}

// Authorize pregunta si la empresa puede enviar a count destinatarios de la clase ahora.
// reputation responde 200 tanto al autorizar como al denegar (allowed false con su
// reason); por eso solo una respuesta 2xx con allowed explicito es una respuesta y
// cualquier otra cosa es un error (reputation no respondio): quien llama decide si falla
// abierto o cerrado.
func (c *Client) Authorize(ctx context.Context, tenantID uuid.UUID, class string, count int) (*ports.Authorization, error) {
	if c.baseURL == "" {
		return nil, errors.New("REPUTATION_URL no configurada")
	}
	payload, err := json.Marshal(map[string]any{"class": class, "count": count})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/reputation/authorize", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reputation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return nil, fmt.Errorf("reputation: status %d", resp.StatusCode)
	}
	var out struct {
		Data *struct {
			Allowed           *bool    `json:"allowed"`
			Class             string   `json:"class"`
			State             string   `json:"state"`
			Reason            string   `json:"reason"`
			RetryAfterSeconds *float64 `json:"retry_after_seconds"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("reputation: respuesta ilegible: %w", err)
	}
	if out.Data == nil || out.Data.Allowed == nil {
		return nil, errors.New("reputation: la respuesta no dice si se permite el envio")
	}
	auth := &ports.Authorization{
		Allowed: *out.Data.Allowed,
		Class:   out.Data.Class,
		State:   out.Data.State,
		Reason:  out.Data.Reason,
	}
	if out.Data.RetryAfterSeconds != nil {
		seconds := retryAfter(*out.Data.RetryAfterSeconds)
		auth.RetryAfterSeconds = &seconds
	}
	return auth, nil
}

// retryAfter redondea hacia arriba y acota la espera; un valor negativo no espera nada.
func retryAfter(seconds float64) int {
	if seconds <= 0 {
		return 0
	}
	if seconds >= maxRetryAfterSeconds {
		return maxRetryAfterSeconds
	}
	return int(math.Ceil(seconds))
}
