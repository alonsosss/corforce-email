// Package templatesclient renderiza por el API interno de templates
// (POST /internal/templates/{id}/render), que devuelve ademas la version y el tipo (kind).
package templatesclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/automations/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

type Client struct {
	caller *internalapi.Caller
}

// New: renderizar no tiene efectos; es seguro repetirlo.
func New(baseURL, token string) *Client {
	return &Client{caller: internalapi.NewCaller("templates", baseURL, token,
		httpclient.Options{Timeout: 10 * time.Second, MaxAttempts: 2})}
}

type renderRequest struct {
	Version   *int                       `json:"version,omitempty"`
	Variables map[string]json.RawMessage `json:"variables"`
	Reserved  map[string]string          `json:"reserved"`
}

func (c *Client) Render(ctx context.Context, tenantID uuid.UUID, req ports.RenderRequest) (*ports.Rendered, error) {
	vars := req.Variables
	if vars == nil {
		vars = map[string]json.RawMessage{}
	}
	var out struct {
		Data struct {
			Subject string `json:"subject"`
			HTML    string `json:"html"`
			Text    string `json:"text"`
			Version int    `json:"version"`
			Kind    string `json:"kind"`
		} `json:"data"`
	}
	err := c.caller.Post(ctx, tenantID, "/internal/templates/"+req.TemplateID.String()+"/render",
		renderRequest{Version: req.Version, Variables: vars, Reserved: map[string]string{}}, true, nil, &out)
	if err != nil {
		var se *internalapi.StatusError
		if !errors.As(err, &se) {
			return nil, err
		}
		switch se.Status {
		case http.StatusNotFound:
			return nil, domain.ErrTemplateNotFound
		case http.StatusConflict:
			return nil, fmt.Errorf("%w (templates: %s)", domain.ErrNoPublishedVersion, se.Message)
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return nil, fmt.Errorf("%w (templates: %s)", domain.ErrTemplateVariables, se.Message)
		}
		return nil, fmt.Errorf("%w: templates: %v", ports.ErrUnavailable, se)
	}
	return &ports.Rendered{
		Subject: out.Data.Subject, HTML: out.Data.HTML, Text: out.Data.Text,
		Version: out.Data.Version, Kind: out.Data.Kind,
	}, nil
}
