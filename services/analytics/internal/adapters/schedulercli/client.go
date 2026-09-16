// Package schedulercli cierra en el scheduler la ejecucion de un trabajo que este despacho:
// POST /internal/scheduler/executions/{id}/complete o /fail, con el token interno y la
// empresa en cabecera. No pasa por el gateway.
package schedulercli

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
	"github.com/alonsosss/corforce-email/services/analytics/internal/ports"
	"github.com/google/uuid"
)

// maxResponseBytes acota lo que se lee del scheduler: la respuesta es la ejecucion cerrada.
const maxResponseBytes = 1 << 20

type Client struct {
	baseURL string
	token   string
	http    *httpclient.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		// El cierre es idempotente en el scheduler (repetirlo devuelve la misma ejecucion),
		// asi que un POST que no se sabe si llego se puede repetir.
		http: httpclient.New("scheduler", httpclient.Options{Timeout: 10 * time.Second}),
	}
}

type completeBody struct {
	Result any `json:"result"`
}

type failBody struct {
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
}

// Complete informa que la ejecucion termino con exito, con lo que hizo como resultado.
func (c *Client) Complete(ctx context.Context, tenantID, executionID uuid.UUID, result any) error {
	return c.post(ctx, tenantID, executionID, "complete", completeBody{Result: result})
}

// Fail informa el fallo. retryable deja que el scheduler decida el reintento con su espera
// creciente y su presupuesto (max_retries), en vez de reintentarlo el bus.
func (c *Client) Fail(ctx context.Context, tenantID, executionID uuid.UUID, message string, retryable bool) error {
	return c.post(ctx, tenantID, executionID, "fail", failBody{Error: message, Retryable: retryable})
}

func (c *Client) post(ctx context.Context, tenantID, executionID uuid.UUID, action string, body any) error {
	if c.baseURL == "" {
		return fmt.Errorf("%w: SCHEDULER_URL sin configurar", ports.ErrSchedulerUnavailable)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/internal/scheduler/executions/%s/%s", c.baseURL, executionID, action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	resp, err := c.http.Do(httpclient.Idempotent(req))
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrSchedulerUnavailable, err)
	}
	defer resp.Body.Close()
	reader := io.LimitReader(resp.Body, maxResponseBytes)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, reader)
		return nil
	}
	detail := errorCode(reader)
	// Un 4xx es definitivo: la ejecucion ya no admite este cierre (venció y se cerro por
	// timeout, se cancelo, o no es de esta empresa). Repetirlo daria siempre lo mismo.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("%w: status %d %s", ports.ErrReportRejected, resp.StatusCode, detail)
	}
	return fmt.Errorf("%w: status %d %s", ports.ErrSchedulerUnavailable, resp.StatusCode, detail)
}

// errorCode saca el codigo del envelope de pkg/response, para que el registro diga por que
// se rechazo el cierre y no solo el estado.
func errorCode(r io.Reader) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(r).Decode(&env) != nil {
		return ""
	}
	return env.Error.Code
}
