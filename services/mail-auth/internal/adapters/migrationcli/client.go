// Package migrationcli pregunta a mail-migration si un token es la credencial de destino vigente de un
// trabajo de migracion y de que buzon. Es la unica fuente de esa respuesta: mail-auth no guarda
// credenciales de trabajo.
package migrationcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/google/uuid"
)

const (
	verifyPath      = "/internal/mail-migration/credentials/verify"
	maxResponseBody = 16 << 10
	// callTimeout debe quedar por debajo del plazo con el que Dovecot espera a mail-auth.
	callTimeout = 5 * time.Second
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New crea el cliente de mail-migration en baseURL (scheme://host:puerto) con el token de gateway que
// exigen las rutas entre servicios.
func New(baseURL, gatewayToken string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   gatewayToken,
		http:    httpclient.New("mail-migration", httpclient.Options{Timeout: callTimeout, MaxAttempts: 2}),
	}
}

type verifyResponse struct {
	Data struct {
		TenantID  uuid.UUID `json:"tenant_id"`
		JobID     uuid.UUID `json:"job_id"`
		MailboxID uuid.UUID `json:"mailbox_id"`
		Username  string    `json:"username"`
	} `json:"data"`
}

// Verify devuelve domain.ErrJobCredentialRejected cuando mail-migration responde 401 y un error distinto
// para todo lo demas (transporte, 5xx, respuesta ilegible o de otro buzon): un fallo no se toma por un
// rechazo, porque los rechazos alimentan el freno de fuerza bruta.
func (c *Client) Verify(ctx context.Context, token, username string) (domain.JobCredential, error) {
	body, err := json.Marshal(map[string]string{"token": token, "username": username})
	if err != nil {
		return domain.JobCredential{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+verifyPath, bytes.NewReader(body))
	if err != nil {
		return domain.JobCredential{}, err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)

	resp, err := c.http.Do(httpclient.Idempotent(req))
	if err != nil {
		return domain.JobCredential{}, fmt.Errorf("mail-migration: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return domain.JobCredential{}, domain.ErrJobCredentialRejected
	default:
		return domain.JobCredential{}, fmt.Errorf("mail-migration respondio %d", resp.StatusCode)
	}
	var out verifyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out); err != nil {
		return domain.JobCredential{}, fmt.Errorf("mail-migration: respuesta ilegible: %w", err)
	}
	if out.Data.TenantID == uuid.Nil || out.Data.MailboxID == uuid.Nil || out.Data.JobID == uuid.Nil {
		return domain.JobCredential{}, errors.New("mail-migration: respuesta sin empresa, buzon o trabajo")
	}
	return domain.JobCredential{TenantID: out.Data.TenantID, MailboxID: out.Data.MailboxID, JobID: out.Data.JobID}, nil
}
