// Package organizationcli escribe en el indice global de los dominios de correo activos que
// sirve organization (Modelo_de_Datos_y_Celdas.md, 5.5): domain-service reclama cada dominio
// antes de activarlo en el directorio de la celda de su empresa y lo suelta despues de
// desactivarlo. Token interno y sin usuario: organization rechaza cualquier llamada con usuario.
package organizationcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

const (
	callTimeout  = 5 * time.Second
	callAttempts = 3
	maxErrorBody = 4 << 10
	// codeClaimed es el 409 de organization para un dominio activo en otra empresa.
	codeClaimed = "MAIL_DOMAIN_CLAIMED"
)

// Client implementa ports.DomainIndex. Las dos llamadas son idempotentes (PUT y DELETE), asi
// que pkg/httpclient las reintenta ante un fallo de red o un 502/503/504.
type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpclient.New("organization", httpclient.Options{Timeout: callTimeout, MaxAttempts: callAttempts}),
	}
}

// Claim hace PUT /internal/organization/tenants/{id}/mail-domains/{dominio}: 200 es reclamado
// (tambien si ya lo estaba), 409 MAIL_DOMAIN_CLAIMED es domain.ErrDomainClaimedElsewhere y
// cualquier otra respuesta es un fallo que se reintenta en el barrido.
func (c *Client) Claim(ctx context.Context, tenantID uuid.UUID, name string) error {
	resp, err := c.do(ctx, http.MethodPut, tenantID, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusConflict && errorCode(resp.Body) == codeClaimed:
		return domain.ErrDomainClaimedElsewhere
	default:
		return fmt.Errorf("organization: reclamar %s respondio %d", name, resp.StatusCode)
	}
}

// Release hace DELETE de la misma ruta: 204 aunque la empresa no lo tuviera reclamado.
func (c *Client) Release(ctx context.Context, tenantID uuid.UUID, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, tenantID, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("organization: soltar %s respondio %d", name, resp.StatusCode)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method string, tenantID uuid.UUID, name string) (*http.Response, error) {
	target := c.baseURL + "/internal/organization/tenants/" + tenantID.String() + "/mail-domains/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("organization: %w", err)
	}
	return resp, nil
}

// errorCode lee el codigo del sobre de error JSON ({"error":{"code":...}}), o vacio.
func errorCode(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBody))
	if err != nil {
		return ""
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(bytes.NewReader(raw)).Decode(&envelope) != nil {
		return ""
	}
	return envelope.Error.Code
}
