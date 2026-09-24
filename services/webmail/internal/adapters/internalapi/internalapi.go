// Package internalapi hace las llamadas internas del webmail a los servicios duenos de los datos
// del buzon (mail-directory en la celda, mail-dav en la empresa): token de gateway, envelope de
// pkg/response y traduccion de sus errores a los del dominio. Las rutas /internal nunca las
// expone el gateway.
package internalapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/alonsosss/corforce-email/pkg/httpclient"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// maxResponseBytes acota lo que se lee de una respuesta JSON.
const maxResponseBytes = 4 << 20

// APIError es el error del envelope. details.field dice que campo se rechazo
// (rules[3].conditions[0].value, emails[1].value...).
type APIError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details"`
}

// Meta es la paginacion del envelope.
type Meta struct {
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
	Total   int64 `json:"total"`
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *APIError       `json:"error"`
	Meta  *Meta           `json:"meta"`
}

// Errors dice como traducir las respuestas que no son 2xx: primero el codigo de error del envelope
// (ByCode), despues el codigo HTTP (ByStatus); lo que no esta es indisponibilidad. Field es el campo
// que se da a un 422 sin details.field.
type Errors struct {
	ByCode   map[string]error
	ByStatus map[int]error
	Field    string
	// Passthrough entrega los rechazos del servicio tal cual (*domain.ServiceRejection con su
	// codigo, mensaje, detalles, ETag y Retry-After) en vez de traducirlos.
	Passthrough bool
}

// rejectionKinds son los estados que un servicio usa para rechazar y la clase de dominio de cada uno.
var rejectionKinds = map[int]domain.RejectionKind{
	http.StatusBadRequest:            domain.RejectBadRequest,
	http.StatusUnprocessableEntity:   domain.RejectValidation,
	http.StatusNotFound:              domain.RejectNotFound,
	http.StatusPreconditionFailed:    domain.RejectPrecondition,
	http.StatusRequestEntityTooLarge: domain.RejectTooLarge,
	http.StatusInsufficientStorage:   domain.RejectQuota,
	http.StatusTooManyRequests:       domain.RejectRateLimited,
	http.StatusServiceUnavailable:    domain.RejectUnavailable,
	http.StatusConflict:              domain.RejectConflict,
}

// Request es una llamada interna. Body se envia como JSON salvo que Raw lo sustituya (con su
// ContentType); Out recibe data y MetaOut la paginacion.
type Request struct {
	Method      string
	Path        string
	Query       url.Values
	Header      http.Header
	Body        any
	Raw         io.Reader
	ContentType string
	Out         any
	MetaOut     *Meta
	Errors      Errors
}

// Caller llama a un servicio interno.
type Caller struct {
	service string
	base    string
	token   string
	http    *httpclient.Client
}

// NewBaseURL valida la URL base del servicio (http o https, sin credenciales ni ruta).
func NewBaseURL(envName, raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%s debe ser una URL http(s) sin credenciales: %q", envName, raw)
	}
	return u.String(), nil
}

func New(service, base, token string, client *httpclient.Client) *Caller {
	return &Caller{service: service, base: base, token: token, http: client}
}

// Do hace la llamada. Con Errors.Passthrough un rechazo del servicio es un *domain.ServiceRejection;
// sin el, un 422 con mensaje es un *domain.ValidationError con el campo y el motivo del servicio y un
// codigo de Errors.ByCode o Errors.ByStatus, su error. Cualquier otro fallo, domain.ErrUnavailable.
func (c *Caller) Do(ctx context.Context, r Request) error {
	resp, err := c.send(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	var env envelope
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&env)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if r.Out == nil {
			return nil
		}
		if decodeErr != nil || len(env.Data) == 0 {
			return c.unavailable("respuesta ilegible")
		}
		if err := json.Unmarshal(env.Data, r.Out); err != nil {
			return c.unavailable("respuesta ilegible: " + err.Error())
		}
		if r.MetaOut != nil && env.Meta != nil {
			*r.MetaOut = *env.Meta
		}
		return nil
	}
	if decodeErr != nil {
		env.Error = nil
	}
	return c.statusError(resp, env.Error, r.Errors)
}

// Stream hace la llamada y devuelve el cuerpo de una respuesta 200 sin interpretarlo; lo cierra
// quien lo pide.
func (c *Caller) Stream(ctx context.Context, r Request) (io.ReadCloser, error) {
	resp, err := c.send(ctx, r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}
	defer resp.Body.Close()
	var env envelope
	if json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&env) != nil {
		env.Error = nil
	}
	return nil, c.statusError(resp, env.Error, r.Errors)
}

func (c *Caller) send(ctx context.Context, r Request) (*http.Response, error) {
	endpoint := c.base + r.Path
	if len(r.Query) > 0 {
		endpoint += "?" + r.Query.Encode()
	}
	payload, contentType := r.Raw, r.ContentType
	if payload == nil && r.Body != nil {
		raw, err := json.Marshal(r.Body)
		if err != nil {
			return nil, c.unavailable(err.Error())
		}
		payload, contentType = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, endpoint, payload)
	if err != nil {
		return nil, c.unavailable(err.Error())
	}
	for k, values := range r.Header {
		for _, v := range values {
			req.Header.Add(k, v)
		}
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.unavailable(err.Error())
	}
	return resp, nil
}

func (c *Caller) statusError(resp *http.Response, apiErr *APIError, errs Errors) error {
	status := resp.StatusCode
	if kind, ok := rejectionKinds[status]; ok && errs.Passthrough && apiErr != nil && apiErr.Code != "" {
		return &domain.ServiceRejection{
			Kind: kind, Code: apiErr.Code, Message: apiErr.Message, Details: apiErr.Details,
			ETag: resp.Header.Get("ETag"), RetryAfter: resp.Header.Get("Retry-After"),
		}
	}
	if status == http.StatusUnprocessableEntity && apiErr != nil && apiErr.Message != "" {
		field := errs.Field
		if f := apiErr.Details["field"]; f != "" {
			field = f
		}
		return domain.NewValidationError(field, apiErr.Message)
	}
	if apiErr != nil {
		if mapped, ok := errs.ByCode[apiErr.Code]; ok && mapped != nil {
			return mapped
		}
	}
	if mapped, ok := errs.ByStatus[status]; ok && mapped != nil {
		return mapped
	}
	return c.unavailable(fmt.Sprintf("respondió %d", status))
}

func (c *Caller) unavailable(reason string) error {
	return fmt.Errorf("%w: %s: %s", domain.ErrUnavailable, c.service, reason)
}

// Unavailable es el error de una respuesta correcta en forma pero inservible.
func (c *Caller) Unavailable(reason string) error { return c.unavailable(reason) }
