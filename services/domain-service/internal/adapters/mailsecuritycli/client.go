package mailsecuritycli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
)

// Client entrega las claves DKIM a mail-security de la celda de la empresa (tenantcell.Caller),
// que las escribe en el Redis de sus motores (DKIM_PRIV_KEYS y DKIM_SELECTORS). La clave privada
// viaja solo en el cuerpo de esta llamada interna y no se registra en ningun log.
type Client struct {
	cell *tenantcell.Caller
}

func New(cell *tenantcell.Caller) *Client {
	return &Client{cell: cell}
}

type keyBody struct {
	Selector      string `json:"selector"`
	PrivateKeyPEM string `json:"private_key_pem"`
}

// PublishDKIM hace un solo PUT /internal/mail-security/dkim/{domain} con el juego completo de
// claves en keys, en orden: la ultima es la que firma y mail-security retira cualquier otro
// selector del dominio, asi que una clave que la fila ya olvido no sobrevive a la siguiente
// publicacion. 409 DKIM_DOMAIN_NOT_ACTIVE: la celda no sirve el dominio y no guarda nada.
func (c *Client) PublishDKIM(ctx context.Context, tenantID uuid.UUID, name string, keys []ports.DKIMKey) error {
	body := struct {
		Keys []keyBody `json:"keys"`
	}{Keys: make([]keyBody, 0, len(keys))}
	for _, key := range keys {
		body.Keys = append(body.Keys, keyBody{Selector: key.Selector, PrivateKeyPEM: key.PrivateKeyPEM})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.do(ctx, tenantID, http.MethodPut, dkimPath(name), raw)
}

// RetireDKIM hace DELETE /internal/mail-security/dkim/{domain}/{selector}.
func (c *Client) RetireDKIM(ctx context.Context, tenantID uuid.UUID, name, selector string) error {
	return c.do(ctx, tenantID, http.MethodDelete, dkimPath(name)+"/"+url.PathEscape(selector), nil)
}

// DeleteDKIM hace DELETE /internal/mail-security/dkim/{domain}: retira todos los selectores.
func (c *Client) DeleteDKIM(ctx context.Context, tenantID uuid.UUID, name string) error {
	return c.do(ctx, tenantID, http.MethodDelete, dkimPath(name), nil)
}

func dkimPath(name string) string {
	return "/internal/mail-security/dkim/" + url.PathEscape(name)
}

// do ejecuta la llamada. PUT y DELETE son idempotentes y pkg/httpclient los reintenta. Retirar
// una clave que no esta responde 204, asi que solo un 2xx cuenta como hecho: un 404 es una
// instancia que no sirve la ruta y la clave podria seguir en los motores.
func (c *Client) do(ctx context.Context, tenantID uuid.UUID, method, path string, body []byte) error {
	resp, err := c.cell.Do(ctx, tenantID, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("mail-security: %s %s respondio %d", method, path, resp.StatusCode)
}
