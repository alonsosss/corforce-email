// Package maildirectorycli consulta a mail-directory, en la celda de la empresa, el buzon destino de
// una migracion. La llamada va a la instancia de esa celda (tenantcell.Caller), con el token de
// gateway y la empresa en cabecera, como el resto de llamadas servicio-a-servicio.
package maildirectorycli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

const (
	maxResponseBody = 64 << 10
	// activeOn es el valor de mail.mailboxes.active de un buzon que acepta sesion.
	activeOn = 1
)

type Client struct {
	cell *tenantcell.Caller
}

func New(cell *tenantcell.Caller) *Client { return &Client{cell: cell} }

type mailboxResponse struct {
	Data struct {
		ID       uuid.UUID `json:"id"`
		Username string    `json:"username"`
		Active   int       `json:"active"`
	} `json:"data"`
}

// Lookup hace GET /internal/mail-directory/mailboxes/{id}. Un 404 es un buzon que no es de la
// empresa; cualquier otra respuesta que no sea 200 es un fallo del servicio, no una ausencia.
func (c *Client) Lookup(ctx context.Context, tenantID, mailboxID uuid.UUID) (ports.MailboxRef, error) {
	resp, err := c.cell.Do(ctx, tenantID, http.MethodGet, "/internal/mail-directory/mailboxes/"+mailboxID.String(), nil)
	if err != nil {
		return ports.MailboxRef{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ports.MailboxRef{}, domain.ErrMailboxNotFound
	default:
		return ports.MailboxRef{}, fmt.Errorf("mail-directory: buzon %s respondio %d", mailboxID, resp.StatusCode)
	}
	var out mailboxResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out); err != nil {
		return ports.MailboxRef{}, fmt.Errorf("mail-directory: respuesta ilegible: %w", err)
	}
	if out.Data.ID != mailboxID || out.Data.Username == "" {
		return ports.MailboxRef{}, fmt.Errorf("mail-directory: respuesta de otro buzon o sin nombre")
	}
	return ports.MailboxRef{ID: out.Data.ID, Username: out.Data.Username, Active: out.Data.Active == activeOn}, nil
}
