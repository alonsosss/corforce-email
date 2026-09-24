// Package mailauth verifica la credencial de un buzon contra mail-auth, con el mismo
// contrato que usa Dovecot (passwd-verify.lua) y service "webmail".
package mailauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	// service es el valor que mail-auth traduce al gateo propio del webmail.
	service          = "webmail"
	maxResponseBytes = 4 << 10
	maxDisplayName   = 1 << 10
)

// Client implementa ports.Authenticator.
type Client struct {
	endpoint string
	http     *http.Client
}

// New exige HTTPS: por esta llamada viaja la contrasena del buzon. tlsConfig decide como
// se verifica el certificado de mail-auth.
func New(endpoint string, tlsConfig *tls.Config, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("MAIL_AUTH_URL debe ser una URL https: %q", endpoint)
	}
	transport := &http.Transport{
		// Sin proxy de entorno: la contrasena no debe salir por un HTTPS_PROXY heredado.
		Proxy:                 nil,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Client{
		endpoint: u.String(),
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			// Una redireccion reenviaria la contrasena a otro destino.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

type verifyRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	RealRIP  string `json:"real_rip"`
	Service  string `json:"service"`
}

type verifyResponse struct {
	Success     bool   `json:"success"`
	DisplayName string `json:"display_name"`
	TenantID    string `json:"tenant_id"`
	MailboxID   string `json:"mailbox_id"`
}

// Verify devuelve domain.ErrInvalidCredentials para cualquier rechazo (401 o success
// false) y domain.ErrUnavailable para todo lo demas.
func (c *Client) Verify(ctx context.Context, username, password, remoteIP string) (domain.Identity, error) {
	body, err := json.Marshal(verifyRequest{Username: username, Password: password, RealRIP: remoteIP, Service: service})
	if err != nil {
		return domain.Identity{}, fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return domain.Identity{}, fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return domain.Identity{}, fmt.Errorf("%w: mail-auth: %v", domain.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return domain.Identity{}, fmt.Errorf("%w: mail-auth: %v", domain.ErrUnavailable, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var out verifyResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			return domain.Identity{}, fmt.Errorf("%w: respuesta de mail-auth ilegible", domain.ErrUnavailable)
		}
		if !out.Success {
			return domain.Identity{}, domain.ErrInvalidCredentials
		}
		return domain.Identity{
			Username: username, DisplayName: displayName(out.DisplayName),
			TenantID: uuidOrEmpty(out.TenantID), MailboxID: uuidOrEmpty(out.MailboxID),
		}, nil
	case http.StatusUnauthorized:
		return domain.Identity{}, domain.ErrInvalidCredentials
	default:
		return domain.Identity{}, fmt.Errorf("%w: mail-auth respondió %d", domain.ErrUnavailable, resp.StatusCode)
	}
}

// displayName descarta un nombre que no podria ir en la cabecera From: sale de la base
// del directorio y el webmail lo escribe en cada mensaje.
func displayName(name string) string {
	name = strings.TrimSpace(name)
	if domain.ValidateHeaderText("display_name", name, maxDisplayName) != nil {
		return ""
	}
	return name
}

// uuidOrEmpty descarta un identificador que no sea un UUID: viaja despues en las cabeceras de las
// llamadas a mail-dav, y una sesion sin el pide volver a entrar en vez de usar uno roto.
func uuidOrEmpty(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if !domain.ValidUUID(v) {
		return ""
	}
	return v
}
