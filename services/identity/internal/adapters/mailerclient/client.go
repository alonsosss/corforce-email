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
	"strings"
	"time"

	"github.com/google/uuid"
)

// EnvBaseURL es la variable que apunta al servicio de correo transaccional. Sin ella el
// cliente no adivina ningun host: los correos de identity no salen y queda constancia.
const EnvBaseURL = "TRANSACTIONAL_MAIL_URL"

// EnvPlatformTenantID es la empresa de plataforma: los correos del sistema salen como ella, con su dominio
// de remitente verificado, y no como la empresa del usuario que los provoca.
const EnvPlatformTenantID = "PLATFORM_TENANT_ID"

const sendPath = "/internal/send-email"

var errNotConfigured = errors.New("correo transaccional no configurado: falta " + EnvBaseURL)

type Client struct {
	baseURL          string
	token            string
	platformTenantID uuid.UUID
	http             *http.Client
}

// New envia a baseURL, la URL base de transactional ya validada al arrancar; vacia, el
// cliente queda sin configurar. Con platformTenantID, todo correo sale como esa empresa: el
// remitente del sistema (PLATFORM_FROM_EMAIL) solo esta verificado en la de plataforma, y
// transactional lo busca entre los dominios de la empresa que envia, asi que con la del usuario
// la recuperacion de contrasena fallaba con 422 para toda empresa que no fuera la de plataforma.
// Sin el (uuid.Nil), sale como la empresa del usuario.
func New(baseURL, token string, platformTenantID uuid.UUID) *Client {
	return &Client{
		baseURL:          strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:            token,
		platformTenantID: platformTenantID,
		http:             &http.Client{Timeout: 15 * time.Second},
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
	sender := tenantID
	if c.platformTenantID != uuid.Nil {
		sender = c.platformTenantID
	}
	req.Header.Set("X-Tenant-ID", sender.String())

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
