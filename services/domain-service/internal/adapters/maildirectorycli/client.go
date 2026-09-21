package maildirectorycli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
)

// codeTenantRetired es el 409 de mail-directory para una empresa dada de baja en su celda.
const codeTenantRetired = "TENANT_RETIRED"

const (
	// modeNone es el modo MTA-STS que no publica politica.
	modeNone = "none"
	// maxPolicyBody acota la respuesta de mail-directory: es una fila pequena.
	maxPolicyBody = 64 << 10
)

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

// PolicyID hace GET /internal/mail-directory/mta-sts/{domain} y devuelve la version de la politica
// MTA-STS del dominio, la que va en su TXT _mta-sts. Vacia si el dominio no la publica: modo none,
// sin politica, o un dominio que el directorio de la celda no tiene todavia (404). Un 404 de una
// instancia anterior que no sirve la ruta significa lo mismo: sin politica que anunciar.
func (c *Client) PolicyID(ctx context.Context, tenantID uuid.UUID, name string) (string, error) {
	resp, err := c.cell.Do(ctx, tenantID, http.MethodGet, "/internal/mail-directory/mta-sts/"+url.PathEscape(name), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", nil
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("mail-directory: mta-sts de %s respondio %d", name, resp.StatusCode)
	}
	var out struct {
		Data struct {
			Mode     string `json:"mode"`
			PolicyID string `json:"policy_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPolicyBody)).Decode(&out); err != nil {
		return "", fmt.Errorf("mail-directory: mta-sts de %s ilegible: %w", name, err)
	}
	if out.Data.Mode == modeNone {
		return "", nil
	}
	return out.Data.PolicyID, nil
}
