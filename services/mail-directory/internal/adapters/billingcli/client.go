// Package billingcli consulta a billing lo que el plan de la empresa INCLUYE antes de dar
// de alta un buzon o de subirle la cuota. Pregunta por los limites del plan y no por el
// consumo (/internal/billing/plan-limits): el espacio asignado a los buzones lo suma este
// servicio en su celda, que es donde vive el directorio, y billing no lo cuenta.
package billingcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

const (
	limitsPath = "/internal/billing/plan-limits"
	// requestTimeout es corto: la consulta va en el camino de un alta de buzon y, si billing
	// tarda, el alta sigue sin el limite del plan (el del dominio se aplica igual).
	requestTimeout   = 2 * time.Second
	maxResponseBytes = 64 << 10
)

// Client implementa ports.Entitlements. Un solo intento: el cortacircuitos de
// pkg/httpclient deja de llamar a un billing caido en vez de encadenar esperas.
type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

// New recibe BILLING_URL y el INTERNAL_GATEWAY_TOKEN con el que billing acepta llamadas
// entre servicios. Con baseURL vacia el cliente no consulta y todo queda sin limite de plan.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New("billing", httpclient.Options{Timeout: requestTimeout, MaxAttempts: 1}),
	}
}

// Configured dice si hay a quien preguntar. Se consulta al arrancar para avisar en el log,
// no para fallar: el directorio funciona sin billing, solo sin el limite del plan.
func (c *Client) Configured() bool { return c.baseURL != "" }

type limitsResponse struct {
	Data struct {
		HasPlan     bool             `json:"has_plan"`
		AllowsUsage bool             `json:"allows_usage"`
		Limits      map[string]int64 `json:"limits"`
		HardLimits  map[string]bool  `json:"hard_limits"`
	} `json:"data"`
}

// Limit devuelve lo que el plan incluye del recurso. Unknown queda a true cuando no hay a
// quien preguntar, cuando la empresa no tiene plan o cuando el plan no fija ese recurso:
// quien la recibe no restringe.
func (c *Client) Limit(ctx context.Context, tenantID uuid.UUID, resource string) (ports.PlanAllowance, error) {
	if !c.Configured() {
		return ports.PlanAllowance{Unknown: true}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+limitsPath, nil)
	if err != nil {
		return ports.PlanAllowance{Unknown: true}, err
	}
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return ports.PlanAllowance{Unknown: true}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return ports.PlanAllowance{Unknown: true}, fmt.Errorf("billing plan-limits: status %d", resp.StatusCode)
	}
	var payload limitsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return ports.PlanAllowance{Unknown: true}, fmt.Errorf("billing plan-limits: respuesta ilegible: %w", err)
	}
	d := payload.Data
	if !d.HasPlan {
		return ports.PlanAllowance{Unknown: true}, nil
	}
	// Una empresa dada de baja no crece, aunque su plan incluyera mas o no limitara: es el
	// mismo criterio que aplica entitlements/check al denegar el envio (ADR 0010).
	if !d.AllowsUsage {
		return ports.PlanAllowance{SubscriptionInactive: true}, nil
	}
	included, ok := d.Limits[resource]
	if !ok {
		return ports.PlanAllowance{Unknown: true}, nil
	}
	return ports.PlanAllowance{Limit: included, HardLimit: d.HardLimits[resource]}, nil
}
