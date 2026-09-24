package contactsclient

import (
	"context"
	"encoding/json"

	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

const (
	matchPath         = "/internal/contacts/match"
	anniversariesPath = "/internal/contacts/anniversaries"
)

type matchRequest struct {
	SegmentID  *uuid.UUID      `json:"segment_id,omitempty"`
	Definition json.RawMessage `json:"definition,omitempty"`
	ContactIDs []uuid.UUID     `json:"contact_ids"`
}

// Match es una consulta: repetirla ante un fallo transitorio no cambia nada.
func (c *Client) Match(ctx context.Context, tenantID uuid.UUID, q ports.MatchQuery) ([]uuid.UUID, error) {
	ids := q.ContactIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	var out struct {
		Data struct {
			ContactIDs []uuid.UUID `json:"contact_ids"`
		} `json:"data"`
	}
	err := c.caller.Post(ctx, tenantID, matchPath, matchRequest{SegmentID: q.SegmentID, Definition: q.Definition, ContactIDs: ids}, true, nil, &out)
	if err != nil {
		return nil, internalapi.Classify(err)
	}
	return out.Data.ContactIDs, nil
}

type anniversaryRequest struct {
	Attribute        string     `json:"attribute"`
	Hour             int        `json:"hour"`
	FallbackTimezone string     `json:"fallback_timezone"`
	ListID           *uuid.UUID `json:"list_id,omitempty"`
	Cursor           string     `json:"cursor,omitempty"`
	Limit            int        `json:"limit,omitempty"`
}

func (c *Client) Anniversaries(ctx context.Context, tenantID uuid.UUID, q ports.AnniversaryQuery) (*ports.AnniversaryPage, error) {
	var out struct {
		Data struct {
			Matches []struct {
				ContactID  uuid.UUID `json:"contact_id"`
				Occurrence string    `json:"occurrence"`
			} `json:"matches"`
			NextCursor *string `json:"next_cursor"`
		} `json:"data"`
	}
	err := c.caller.Post(ctx, tenantID, anniversariesPath, anniversaryRequest{
		Attribute: q.Attribute, Hour: q.Hour, FallbackTimezone: q.Timezone, ListID: q.ListID, Cursor: q.Cursor, Limit: q.Limit,
	}, true, nil, &out)
	if err != nil {
		return nil, internalapi.Classify(err)
	}
	page := &ports.AnniversaryPage{Matches: make([]ports.AnniversaryMatch, 0, len(out.Data.Matches))}
	for _, m := range out.Data.Matches {
		page.Matches = append(page.Matches, ports.AnniversaryMatch{ContactID: m.ContactID, Occurrence: m.Occurrence})
	}
	if out.Data.NextCursor != nil {
		page.NextCursor = *out.Data.NextCursor
	}
	return page, nil
}

var _ ports.ContactRules = (*Client)(nil)
