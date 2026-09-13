// Package contactsclient habla con contacts por su API interna: contactos enviables,
// pertenencia a una lista y alta o baja de miembros.
package contactsclient

import (
	"context"
	"encoding/json"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

const (
	sendablePath = "/internal/contacts/sendable"
	listsPath    = "/internal/contacts/lists/"
)

type Client struct {
	caller *internalapi.Caller
}

// New: las cuatro operaciones son idempotentes (consultas, y altas o bajas que ignoran lo
// que ya estaba), asi que el cliente HTTP puede repetirlas ante un fallo transitorio.
func New(baseURL, token string) *Client {
	return &Client{caller: internalapi.NewCaller("contacts", baseURL, token,
		httpclient.Options{Timeout: 10 * time.Second, MaxAttempts: 2})}
}

type idsRequest struct {
	ContactIDs []uuid.UUID `json:"contact_ids"`
}

func (c *Client) Sendable(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Contact, error) {
	var out struct {
		Data struct {
			Contacts []struct {
				ID         uuid.UUID                  `json:"id"`
				Email      string                     `json:"email"`
				FirstName  string                     `json:"first_name"`
				LastName   string                     `json:"last_name"`
				Attributes map[string]json.RawMessage `json:"attributes"`
			} `json:"contacts"`
		} `json:"data"`
	}
	if err := c.caller.Post(ctx, tenantID, sendablePath, idsRequest{ContactIDs: ids}, true, nil, &out); err != nil {
		return nil, internalapi.Classify(err)
	}
	contacts := make([]domain.Contact, 0, len(out.Data.Contacts))
	for _, ct := range out.Data.Contacts {
		contacts = append(contacts, domain.Contact{
			ID: ct.ID, Email: ct.Email, FirstName: ct.FirstName, LastName: ct.LastName, Attributes: ct.Attributes,
		})
	}
	return contacts, nil
}

func (c *Client) ListMembers(ctx context.Context, tenantID, listID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	var out struct {
		Data struct {
			ContactIDs []uuid.UUID `json:"contact_ids"`
		} `json:"data"`
	}
	err := c.caller.Post(ctx, tenantID, listsPath+listID.String()+"/members/check", idsRequest{ContactIDs: ids}, true, nil, &out)
	if err != nil {
		return nil, internalapi.Classify(err)
	}
	return out.Data.ContactIDs, nil
}

func (c *Client) AddToList(ctx context.Context, tenantID, listID uuid.UUID, ids []uuid.UUID) error {
	if err := c.caller.Post(ctx, tenantID, listsPath+listID.String()+"/members", idsRequest{ContactIDs: ids}, true, nil, nil); err != nil {
		return internalapi.Classify(err)
	}
	return nil
}

func (c *Client) RemoveFromList(ctx context.Context, tenantID, listID uuid.UUID, ids []uuid.UUID) error {
	if err := c.caller.Post(ctx, tenantID, listsPath+listID.String()+"/members/remove", idsRequest{ContactIDs: ids}, true, nil, nil); err != nil {
		return internalapi.Classify(err)
	}
	return nil
}

var _ ports.Contacts = (*Client)(nil)
