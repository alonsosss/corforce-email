package maildirectorycli

import (
	"context"
	"net/http"
	"net/url"

	"github.com/alonsosss/corforce-email/services/webmail/internal/adapters/internalapi"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const primarySendersPath = "/internal/mail-directory/primary-senders"

var primarySenderErrors = internalapi.Errors{
	ByCode: map[string]error{"PRIMARY_SENDER_LIMIT": domain.ErrPrimarySenderLimit},
	Field:  "address",
}

type primarySenderBody struct {
	Address string `json:"address"`
}

// PrimarySenders lee las direcciones que el buzon fijo en Principal, en minusculas.
func (c *Client) PrimarySenders(ctx context.Context, username string) ([]string, error) {
	var out struct {
		Items []struct {
			Address string `json:"address"`
		} `json:"items"`
	}
	if err := c.api.Do(ctx, internalapi.Request{Method: http.MethodGet, Path: primarySendersPath, Query: usernameQuery(username), Out: &out, Errors: primarySenderErrors}); err != nil {
		return nil, err
	}
	addresses := make([]string, len(out.Items))
	for i, item := range out.Items {
		addresses[i] = item.Address
	}
	return addresses, nil
}

// AddPrimarySender fija una direccion en Principal; repetirla no es un error.
func (c *Client) AddPrimarySender(ctx context.Context, username, address string) error {
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodPost, Path: primarySendersPath, Query: usernameQuery(username), Body: primarySenderBody{Address: address}, Errors: primarySenderErrors})
}

// RemovePrimarySender quita una direccion de Principal; quitar una ausente no es un error.
func (c *Client) RemovePrimarySender(ctx context.Context, username, address string) error {
	query := url.Values{"username": {username}, "address": {address}}
	return c.api.Do(ctx, internalapi.Request{Method: http.MethodDelete, Path: primarySendersPath, Query: query, Errors: primarySenderErrors})
}
