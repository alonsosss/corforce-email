package maildirectorycli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

// codeTenantRetired es el 409 de mail-directory para una empresa dada de baja en su celda.
const codeTenantRetired = "TENANT_RETIRED"

// Client activa y desactiva dominios en el directorio de la celda de la empresa a traves de
// mail-directory. La llamada va a la instancia de esa celda (tenantcell.Caller), con el token de
// gateway y la empresa en cabecera, como el resto de llamadas servicio-a-servicio.
type Client struct {
	cell *tenantcell.Caller
}

func New(cell *tenantcell.Caller) *Client {
	return &Client{cell: cell}
}

// SetActivation hace PUT /internal/mail-directory/domains/{domain}/activation. Un 409
// TENANT_RETIRED es una empresa dada de baja en la celda, cuyo directorio ya no activa nada; otro
// 409 significa que el dominio conserva buzones y no puede desactivarse. mail-directory da de alta
// el dominio que no tiene, en los dos sentidos: ninguna respuesta de error es "nada que hacer", y
// un 404 es una instancia que no sirve la ruta, no un dominio que falta.
func (c *Client) SetActivation(ctx context.Context, tenantID uuid.UUID, name string, active bool) error {
	body, err := json.Marshal(map[string]bool{"active": active})
	if err != nil {
		return err
	}
	resp, err := c.cell.Do(ctx, tenantID, http.MethodPut, "/internal/mail-directory/domains/"+url.PathEscape(name)+"/activation", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusConflict && tenantcell.ErrorCode(resp) == codeTenantRetired:
		return domain.ErrTenantBeingRemoved
	case resp.StatusCode == http.StatusConflict:
		return domain.ErrDomainHasMailboxes
	default:
		return fmt.Errorf("mail-directory: activation de %s respondio %d", name, resp.StatusCode)
	}
}
