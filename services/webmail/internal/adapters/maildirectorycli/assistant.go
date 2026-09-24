package maildirectorycli

import (
	"context"
	"net/http"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
)

const assistantPath = "/internal/mail-directory/assistant"

type assistantData struct {
	Enabled bool `json:"enabled"`
}

// AssistantEnabled dice si la empresa del buzon activo el asistente (GET
// /internal/mail-directory/assistant). La empresa la resuelve mail-directory a partir del buzon.
func (c *Client) AssistantEnabled(ctx context.Context, username string) (bool, error) {
	var out assistantData
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: assistantPath, Query: usernameQuery(username), Out: &out}); err != nil {
		return false, err
	}
	return out.Enabled, nil
}
