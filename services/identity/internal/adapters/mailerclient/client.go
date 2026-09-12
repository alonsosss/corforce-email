// Package mailerclient envia los correos transaccionales de identity (recuperacion de
// contrasena) a traves del endpoint interno del servicio de correo transaccional,
// servicio a servicio y autenticado con el token interno del gateway. El servicio
// transaccional resuelve el remitente y el proveedor efectivos del tenant.
package mailerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EnvBaseURL es la variable que apunta al servicio de correo transaccional. Sin ella el
// cliente no adivina ningun host: los correos de identity no salen y queda constancia.
const EnvBaseURL = "TRANSACTIONAL_MAIL_URL"

const sendPath = "/internal/send-email"

var errNotConfigured = errors.New("correo transaccional no configurado: falta " + EnvBaseURL)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New() *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(os.Getenv(EnvBaseURL)), "/"),
		token:   os.Getenv("INTERNAL_GATEWAY_TOKEN"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Configured dice si hay a donde enviar. Se consulta al arrancar para avisar en el log,
// no para fallar: identity sigue autenticando aunque no pueda mandar correos.
func (c *Client) Configured() bool { return c.baseURL != "" }

func (c *Client) Send(ctx context.Context, tenantID uuid.UUID, to, subject, htmlBody string) error {
	if !c.Configured() {
		return errNotConfigured
	}
	payload, err := json.Marshal(map[string]string{
		"to":        to,
		"subject":   subject,
		"html_body": htmlBody,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+sendPath, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("correo transaccional no disponible: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("correo transaccional respondio %d", resp.StatusCode)
	}
	return nil
}
