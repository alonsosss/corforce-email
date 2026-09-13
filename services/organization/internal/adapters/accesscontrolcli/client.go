// Package accesscontrolcli pide a access-control, por su API interna, las operaciones de
// roles de una empresa entera que orquesta la saga de alta y baja. Autentica con el token
// interno y sin usuario: access-control rechaza estas rutas a cualquier persona.
package accesscontrolcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/google/uuid"
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("access-control", httpclient.Options{Timeout: 10 * time.Second, MaxAttempts: 3}),
	}
}

func tenantPath(tenantID uuid.UUID) string {
	return "/internal/access-control/tenants/" + tenantID.String()
}

func userRolePath(tenantID, userID, roleID uuid.UUID) string {
	return tenantPath(tenantID) + "/users/" + userID.String() + "/roles/" + roleID.String()
}

func (c *Client) SeedTenantAdminRole(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, error) {
	var out struct {
		Data struct {
			RoleID uuid.UUID `json:"role_id"`
		} `json:"data"`
	}
	if err := c.call(ctx, http.MethodPut, tenantPath(tenantID)+"/system-role", &out); err != nil {
		return uuid.Nil, err
	}
	if out.Data.RoleID == uuid.Nil {
		return uuid.Nil, errors.New("access-control: la siembra no devolvio el rol")
	}
	return out.Data.RoleID, nil
}

func (c *Client) ReseedSystemRoles(ctx context.Context) (int, error) {
	var out struct {
		Data struct {
			Roles int `json:"roles"`
		} `json:"data"`
	}
	if err := c.call(ctx, http.MethodPost, "/internal/access-control/system-role/reseed", &out); err != nil {
		return 0, err
	}
	return out.Data.Roles, nil
}

func (c *Client) AssignRole(ctx context.Context, tenantID, userID, roleID uuid.UUID) error {
	return c.call(ctx, http.MethodPut, userRolePath(tenantID, userID, roleID), nil)
}

func (c *Client) RevokeRole(ctx context.Context, tenantID, userID, roleID uuid.UUID) error {
	return c.call(ctx, http.MethodDelete, userRolePath(tenantID, userID, roleID), nil)
}

func (c *Client) RemoveTenantRoles(ctx context.Context, tenantID uuid.UUID) error {
	return c.call(ctx, http.MethodDelete, tenantPath(tenantID)+"/roles", nil)
}

// call hace la peticion y decodifica la respuesta en out (si no es nil). Todas las
// operaciones son idempotentes por contrato, asi que tambien el POST de la resiembra se
// marca como tal para que el cliente compartido lo repita ante una caida transitoria.
func (c *Client) call(ctx context.Context, method, path string, out any) error {
	if c.baseURL == "" {
		return errors.New("access-control: ACCESS_CONTROL_URL no configurada")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.http.Do(httpclient.Idempotent(req))
	if err != nil {
		return fmt.Errorf("access-control: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("access-control: %s %s: %s", method, path, describe(resp))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("access-control: respuesta ilegible: %w", err)
	}
	return nil
}

// describe resume una respuesta de error con su estado y el codigo del cuerpo, si lo trae.
func describe(resp *http.Response) string {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil || body.Error.Code == "" {
		return fmt.Sprintf("status %d", resp.StatusCode)
	}
	return fmt.Sprintf("status %d %s: %s", resp.StatusCode, body.Error.Code, body.Error.Message)
}
