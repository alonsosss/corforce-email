// Package contactsclient pide a contacts las paginas de la audiencia de una campana por
// su API interna (POST /internal/contacts/audience).
package contactsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

const audiencePath = "/internal/contacts/audience"

type Client struct {
	caller *internalapi.Caller
}

// New: pedir una pagina no tiene efectos, asi que el cliente HTTP puede repetirla ante
// un fallo transitorio.
func New(baseURL, token string) *Client {
	return &Client{caller: internalapi.NewCaller("contacts", baseURL, token,
		httpclient.Options{Timeout: 20 * time.Second, MaxAttempts: 2})}
}

type audienceRequest struct {
	ListIDs           []uuid.UUID `json:"list_ids"`
	SegmentIDs        []uuid.UUID `json:"segment_ids"`
	ExcludeSegmentIDs []uuid.UUID `json:"exclude_segment_ids"`
	Cursor            *string     `json:"cursor,omitempty"`
	Limit             int         `json:"limit"`
}

type audienceResponse struct {
	Data struct {
		Contacts []struct {
			ID         uuid.UUID                  `json:"id"`
			Email      string                     `json:"email"`
			FirstName  string                     `json:"first_name"`
			LastName   string                     `json:"last_name"`
			Locale     string                     `json:"locale"`
			Timezone   string                     `json:"timezone"`
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"contacts"`
		NextCursor *string `json:"next_cursor"`
	} `json:"data"`
}

func (c *Client) Audience(ctx context.Context, tenantID uuid.UUID, q ports.AudienceQuery) (*ports.AudiencePage, error) {
	a := q.Audience.Normalized()
	var out audienceResponse
	err := c.caller.Post(ctx, tenantID, audiencePath, audienceRequest{
		ListIDs: a.ListIDs, SegmentIDs: a.SegmentIDs, ExcludeSegmentIDs: a.ExcludeSegmentIDs,
		Cursor: q.Cursor, Limit: q.Limit,
	}, true, &out)
	if err != nil {
		return nil, internalapi.Classify(err)
	}
	// Una pagina mayor que el limite pedido romperia el tope del lote de transactional:
	// es un incumplimiento del contrato y no se envia.
	if len(out.Data.Contacts) > q.Limit {
		return nil, fmt.Errorf("%w: contacts devolvió %d contactos con límite %d",
			ports.ErrUnavailable, len(out.Data.Contacts), q.Limit)
	}
	page := &ports.AudiencePage{Contacts: make([]domain.Contact, 0, len(out.Data.Contacts))}
	for _, ct := range out.Data.Contacts {
		page.Contacts = append(page.Contacts, domain.Contact{
			ID: ct.ID, Email: ct.Email, FirstName: ct.FirstName, LastName: ct.LastName,
			Locale: ct.Locale, Timezone: ct.Timezone, Attributes: ct.Attributes,
		})
	}
	if next := out.Data.NextCursor; next != nil && strings.TrimSpace(*next) != "" {
		cursor := *next
		page.NextCursor = &cursor
	}
	return page, nil
}
