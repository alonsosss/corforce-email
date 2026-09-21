package maildirectorycli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type directoryResponse struct {
	Data []struct {
		Address     string `json:"address"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Search pide a mail-directory los buzones activos de la empresa del buzon que pregunta. Un texto que
// rechaza (422) vuelve como *domain.ValidationError con su motivo; cualquier otro fallo es
// domain.ErrUnavailable. Una direccion que el webmail no podria usar en un destinatario se descarta.
func (c *Client) Search(ctx context.Context, username, query string, limit int) ([]domain.AddressBookEntry, error) {
	params := url.Values{"username": {username}, "q": {query}}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.directory+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: mail-directory: %v", domain.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	var decoded directoryResponse
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&decoded)
	if resp.StatusCode == http.StatusUnprocessableEntity && decodeErr == nil && decoded.Error != nil && decoded.Error.Message != "" {
		return nil, domain.NewValidationError("q", decoded.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: mail-directory respondio %d", domain.ErrUnavailable, resp.StatusCode)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("%w: respuesta de mail-directory ilegible", domain.ErrUnavailable)
	}
	out := make([]domain.AddressBookEntry, 0, len(decoded.Data))
	for _, e := range decoded.Data {
		a, err := domain.NewAddress("to", "", e.Address)
		if err != nil {
			continue
		}
		out = append(out, domain.AddressBookEntry{Address: a.Email, DisplayName: e.DisplayName})
	}
	return out, nil
}
