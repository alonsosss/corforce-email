// Package identitycli pide a identity, por su API interna, las operaciones sobre las cuentas
// de una empresa entera que orquesta la saga de alta y baja. Autentica con el token interno
// y sin usuario. La contrasena del primer administrador solo viaja en el cuerpo de la
// peticion: ningun error la incluye.
package identitycli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/alonsosss/corforce-email/services/organization/internal/ports"
	"github.com/google/uuid"
)

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New fija un tiempo por intento holgado: el alta del primer usuario calcula un bcrypt y
// consulta las contrasenas filtradas (hasta 3 s) antes de responder.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("identity", httpclient.Options{Timeout: 15 * time.Second, MaxAttempts: 3}),
	}
}

func tenantPath(tenantID uuid.UUID) string {
	return "/internal/identity/tenants/" + tenantID.String()
}

type firstUserBody struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) CreateFirstUser(ctx context.Context, tenantID uuid.UUID, admin ports.FirstAdmin) error {
	payload, err := json.Marshal(firstUserBody{
		UserID:    admin.UserID.String(),
		Email:     admin.Email,
		Password:  admin.Password,
		FirstName: admin.FirstName,
		LastName:  admin.LastName,
	})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPut, tenantPath(tenantID)+"/first-user", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	case http.StatusUnprocessableEntity:
		e := readError(resp)
		if e.Code == "" {
			e.Code = "ADMIN_REJECTED"
		}
		return &domain.AdminRejectedError{Code: e.Code, Message: e.Message}
	case http.StatusConflict:
		return fmt.Errorf("%w (%s)", domain.ErrAdminUserConflict, readError(resp).Code)
	default:
		return fmt.Errorf("identity: alta del primer usuario: %s", describe(resp.StatusCode, readError(resp)))
	}
}

func (c *Client) RemoveTenantUsers(ctx context.Context, tenantID uuid.UUID) error {
	resp, err := c.do(ctx, http.MethodDelete, tenantPath(tenantID)+"/users", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("identity: retirar las cuentas: %s", describe(resp.StatusCode, readError(resp)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// do envia la peticion con el token interno. Las dos operaciones son idempotentes por
// contrato (PUT al id elegido por la saga, DELETE de la empresa), asi que el cliente
// compartido puede repetirlas ante una caida transitoria.
func (c *Client) do(ctx context.Context, method, path string, payload []byte) (*http.Response, error) {
	if c.baseURL == "" {
		return nil, errors.New("identity: IDENTITY_URL no configurada")
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.http.Do(httpclient.Idempotent(req))
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	return resp, nil
}

func readError(resp *http.Response) apiError {
	var body struct {
		Error apiError `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return apiError{}
	}
	return body.Error
}

func describe(status int, e apiError) string {
	if e.Code == "" {
		return fmt.Sprintf("status %d", status)
	}
	return fmt.Sprintf("status %d %s: %s", status, e.Code, e.Message)
}
