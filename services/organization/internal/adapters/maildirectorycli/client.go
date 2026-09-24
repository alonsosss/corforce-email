// Package maildirectorycli pide a mail-directory de la celda de una empresa, por su API interna,
// que la de de baja en el directorio de correo de esa celda: es el paso de la saga de baja con el
// que su correo deja de entrar y de autenticar. Va a la instancia de esa celda
// (tenantcell.Caller), con el token interno y la empresa en X-Tenant-ID, y falla cerrado: sin
// instancia declarada de la celda, o con una instancia que no es de ella, el paso no se da por
// hecho.
package maildirectorycli

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/google/uuid"
)

const retirementPath = "/internal/mail-directory/tenant-retirement"

// Client implementa ports.MailDirectory.
type Client struct {
	cell *tenantcell.Caller
}

func New(cell *tenantcell.Caller) *Client {
	return &Client{cell: cell}
}

// RetireTenant hace PUT /internal/mail-directory/tenant-retirement en la instancia de la celda.
// Es idempotente y pkg/httpclient la reintenta ante un fallo de red o un 502/503/504. Solo un 200
// es la baja hecha: un 404 es una instancia que no sirve la ruta y cualquier otra respuesta es un
// fallo que la saga reintenta.
func (c *Client) RetireTenant(ctx context.Context, cellCode string, tenantID uuid.UUID) error {
	resp, err := c.cell.DoInCell(ctx, cellCode, tenantID, http.MethodPut, retirementPath, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mail-directory de la celda %s: la baja de la empresa respondió %d %s",
			cellCode, resp.StatusCode, tenantcell.ErrorCode(resp))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
