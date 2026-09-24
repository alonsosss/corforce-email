// Package billingcli consulta a billing el derecho mensual del plan de la empresa.
package billingcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
)

const (
	checkPath = "/internal/billing/entitlements/check"
	// requestTimeout es corto a proposito: la consulta va en el camino de cada envio y, si
	// billing tarda, se autoriza sin el.
	requestTimeout   = 2 * time.Second
	maxResponseBytes = 64 << 10
)

// Client implementa ports.Entitlements. Un solo intento por consulta: el cortacircuitos
// de pkg/httpclient deja de llamar a un billing caido y la autorizacion sigue sin el.
type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New recibe BILLING_URL y el INTERNAL_GATEWAY_TOKEN con el que billing acepta llamadas
// entre servicios.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpclient.New("billing", httpclient.Options{Timeout: requestTimeout, MaxAttempts: 1}),
	}
}

type checkRequest struct {
	Resource string `json:"resource"`
	Quantity int64  `json:"quantity"`
}

type checkResponse struct {
	Data struct {
		Allowed   bool   `json:"allowed"`
		Resource  string `json:"resource"`
		Limit     *int64 `json:"limit"`
		Used      int64  `json:"used"`
		Remaining *int64 `json:"remaining"`
		HardLimit bool   `json:"hard_limit"`
		Reason    string `json:"reason"`
	} `json:"data"`
}

func (c *Client) Check(ctx context.Context, tenantID uuid.UUID, class domain.Class, quantity int64) (domain.Entitlement, error) {
	body, err := json.Marshal(checkRequest{Resource: class.BillingResource(), Quantity: quantity})
	if err != nil {
		return domain.Entitlement{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+checkPath, bytes.NewReader(body))
	if err != nil {
		return domain.Entitlement{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return domain.Entitlement{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return domain.Entitlement{}, fmt.Errorf("billing entitlements/check: status %d", resp.StatusCode)
	}
	var payload checkResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return domain.Entitlement{}, fmt.Errorf("billing entitlements/check: respuesta ilegible: %w", err)
	}
	d := payload.Data
	if d.Resource != "" && d.Resource != class.BillingResource() {
		return domain.Entitlement{}, fmt.Errorf("billing entitlements/check: respondio por %q en vez de %q", d.Resource, class.BillingResource())
	}
	return domain.Entitlement{
		Allowed:   d.Allowed,
		Limit:     unlimitedAsNil(d.Limit),
		Used:      d.Used,
		Remaining: unlimitedAsNil(d.Remaining),
		HardLimit: d.HardLimit,
		Reason:    d.Reason,
		Requested: quantity,
	}, nil
}

// unlimitedAsNil traduce el -1 con que billing dice que el plan no limita al nil del dominio: un
// remanente negativo con limite duro deniega cualquier envio.
func unlimitedAsNil(v *int64) *int64 {
	if v == nil || *v < 0 {
		return nil
	}
	return v
}
