// Package internalapi es la llamada servicio a servicio que comparten los clientes de
// contacts, transactional y templates: token interno, empresa en cabecera, envelope de
// pkg/response y clasificacion de la respuesta en los errores de los puertos.
package internalapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

// maxResponseBytes acota lo que se lee de un vecino: una pagina de 1000 contactos con
// sus atributos cabe con holgura.
const maxResponseBytes = 32 << 20

// StatusError es una respuesta fuera de 2xx con lo que el vecino dijo de ella.
type StatusError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	if e.Code == "" && e.Message == "" {
		return "status " + strconv.Itoa(e.Status)
	}
	return fmt.Sprintf("status %d %s: %s", e.Status, e.Code, e.Message)
}

type Caller struct {
	name    string
	baseURL string
	token   string
	http    *httpclient.Client
}

func NewCaller(name, baseURL, token string, opt httpclient.Options) *Caller {
	return &Caller{
		name:    name,
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		http:    httpclient.New(name, opt),
	}
}

// Post envia body como JSON y, con 2xx, decodifica el envelope en out. Fuera de 2xx
// devuelve *StatusError; si el vecino no responde, ports.ErrUnavailable. idempotent
// autoriza al cliente HTTP a repetir la peticion ante un fallo transitorio.
func (c *Caller) Post(ctx context.Context, tenantID uuid.UUID, path string, body any, idempotent bool, out any) error {
	if c.baseURL == "" {
		return fmt.Errorf("%w: URL de %s sin configurar", ports.ErrUnavailable, c.name)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", c.token)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	if idempotent {
		req = httpclient.Idempotent(req)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ports.ErrUnavailable, c.name, err)
	}
	defer resp.Body.Close()
	reader := io.LimitReader(resp.Body, maxResponseBytes)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		se := &StatusError{Status: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.NewDecoder(reader).Decode(&env) == nil {
			se.Code, se.Message = env.Error.Code, env.Error.Message
		}
		return se
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(reader).Decode(out); err != nil {
		return fmt.Errorf("%w: %s: respuesta ilegible: %v", ports.ErrUnavailable, c.name, err)
	}
	return nil
}

// Classify traduce la respuesta de un vecino de envio (contacts, transactional) a los
// errores de los puertos: 429 espera, 403 bloqueo, 400/422 rechazo definitivo; el resto
// es transitorio.
func Classify(err error) error {
	var se *StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch se.Status {
	case http.StatusTooManyRequests:
		return &ports.RateLimitedError{RetryAfter: se.RetryAfter}
	case http.StatusForbidden:
		code := se.Code
		if code == "" {
			code = "FORBIDDEN"
		}
		return &ports.BlockedError{Code: code, Message: se.Message}
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		msg := se.Message
		if msg == "" {
			msg = "petición rechazada (status " + strconv.Itoa(se.Status) + ")"
		}
		return &ports.RejectedError{Code: se.Code, Message: msg}
	}
	return fmt.Errorf("%w: %v", ports.ErrUnavailable, se)
}

// parseRetryAfter lee Retry-After en segundos o como fecha HTTP. 0 si falta o no se
// entiende: quien lo usa aplica su espera por defecto.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
