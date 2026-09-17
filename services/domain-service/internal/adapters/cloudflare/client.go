// Package cloudflare implementa ports.DNSProviderAPI contra la API v4 de Cloudflare.
//
// Solo habla con la URL base que recibe (https://api.cloudflare.com por defecto, main.go), no sigue
// redirecciones a ningun otro host y acota tiempo y tamano de cada respuesta. El token viaja solo en
// la cabecera Authorization; ningun error lo contiene ni repite el mensaje de Cloudflare, que puede
// citar la peticion: se traduce a los errores de domain por estado HTTP y codigo numerico.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

const (
	// DefaultBaseURL es la API publica de Cloudflare.
	DefaultBaseURL = "https://api.cloudflare.com"
	apiPath        = "/client/v4"
	// DefaultTimeout acota cada llamada: una publicacion hace unas pocas por registro y va con el
	// cerrojo de claves del dominio tomado.
	DefaultTimeout = 15 * time.Second

	zonesPerPage   = 50
	recordsPerPage = 100
	// maxPages acota la paginacion: 5000 zonas o 10000 registros de un nombre son mas de lo que una
	// empresa tiene; pasar de ahi es una respuesta anomala y se corta.
	maxPages         = 100
	maxResponseBytes = 4 << 20
	// autoTTL es el TTL automatico de Cloudflare.
	autoTTL = 1
)

// Codigos de error de Cloudflare que no se distinguen por el estado HTTP.
const (
	codeInvalidToken          = 1000
	codeInvalidHeaders        = 6003
	codeInvalidAuthFormat     = 6111
	codeUnauthorized          = 9109
	codeAuthenticationError   = 10000
	codeIdenticalRecordExists = 81057
	codeRecordExistsSameName  = 81058
)

// Los ids de zona y de registro de Cloudflare son hexadecimales; se validan antes de ponerlos en
// una ruta.
var idRegex = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

type Client struct {
	base string
	http *http.Client
}

// New recibe la URL base ya validada (config.ServiceURL). Con timeout <= 0 usa DefaultTimeout.
func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		base: strings.TrimSuffix(baseURL, "/") + apiPath,
		http: &http.Client{
			Timeout: timeout,
			// Una redireccion llevaria el token a otro sitio: se devuelve tal cual y cuenta como
			// respuesta inesperada.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type apiError struct {
	Code int `json:"code"`
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *resultInfo     `json:"result_info"`
}

type resultInfo struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
}

type zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type record struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	Priority *int   `json:"priority,omitempty"`
	TTL      int    `json:"ttl,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

func (c *Client) VerifyToken(ctx context.Context, token domain.APIToken) error {
	var result struct {
		Status string `json:"status"`
	}
	if _, err := c.do(ctx, token, http.MethodGet, "/user/tokens/verify", nil, nil, &result); err != nil {
		return err
	}
	if result.Status != "active" {
		return fmt.Errorf("cloudflare: token en estado %q: %w", result.Status, domain.ErrDNSProviderTokenInvalid)
	}
	return nil
}

func (c *Client) ListZones(ctx context.Context, token domain.APIToken) ([]domain.DNSZone, error) {
	var out []domain.DNSZone
	for page := 1; page <= maxPages; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(zonesPerPage)}}
		var zones []zone
		info, err := c.do(ctx, token, http.MethodGet, "/zones", q, nil, &zones)
		if err != nil {
			return nil, err
		}
		for _, z := range zones {
			if !idRegex.MatchString(z.ID) || z.Name == "" {
				continue
			}
			out = append(out, domain.DNSZone{ID: z.ID, Name: strings.ToLower(strings.TrimSuffix(z.Name, "."))})
		}
		if info == nil || page >= info.TotalPages || len(zones) == 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("cloudflare: mas de %d paginas de zonas: %w", maxPages, domain.ErrDNSProviderRejected)
}

func (c *Client) ListRecords(ctx context.Context, token domain.APIToken, z domain.DNSZone, recordType, name string) ([]domain.ProviderRecord, error) {
	if !idRegex.MatchString(z.ID) {
		return nil, domain.ErrDNSZoneNotFound
	}
	var out []domain.ProviderRecord
	for page := 1; page <= maxPages; page++ {
		q := url.Values{
			"type": {recordType}, "name": {name},
			"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(recordsPerPage)},
		}
		var records []record
		info, err := c.do(ctx, token, http.MethodGet, "/zones/"+z.ID+"/dns_records", q, nil, &records)
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			// El filtro lo aplica Cloudflare; se repite aqui para no actuar nunca sobre otro nombre.
			if !strings.EqualFold(r.Type, recordType) || !domain.SameHost(r.Name, name) || !idRegex.MatchString(r.ID) {
				continue
			}
			pr := domain.ProviderRecord{ID: r.ID, Type: strings.ToUpper(r.Type), Name: r.Name, Content: r.Content, Comment: r.Comment}
			if r.Priority != nil {
				pr.Priority = *r.Priority
			}
			out = append(out, pr)
		}
		if info == nil || page >= info.TotalPages || len(records) == 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("cloudflare: mas de %d paginas de registros: %w", maxPages, domain.ErrDNSProviderRejected)
}

func (c *Client) CreateRecord(ctx context.Context, token domain.APIToken, z domain.DNSZone, rec domain.ProviderRecord) error {
	if !idRegex.MatchString(z.ID) {
		return domain.ErrDNSZoneNotFound
	}
	_, err := c.do(ctx, token, http.MethodPost, "/zones/"+z.ID+"/dns_records", nil, toRecord(rec), nil)
	return err
}

func (c *Client) UpdateRecord(ctx context.Context, token domain.APIToken, z domain.DNSZone, rec domain.ProviderRecord) error {
	if !idRegex.MatchString(z.ID) {
		return domain.ErrDNSZoneNotFound
	}
	if !idRegex.MatchString(rec.ID) {
		return domain.ErrDNSProviderRejected
	}
	_, err := c.do(ctx, token, http.MethodPut, "/zones/"+z.ID+"/dns_records/"+rec.ID, nil, toRecord(rec), nil)
	return err
}

func (c *Client) DeleteRecord(ctx context.Context, token domain.APIToken, z domain.DNSZone, recordID string) error {
	if !idRegex.MatchString(z.ID) {
		return domain.ErrDNSZoneNotFound
	}
	if !idRegex.MatchString(recordID) {
		return domain.ErrDNSProviderRejected
	}
	_, err := c.do(ctx, token, http.MethodDelete, "/zones/"+z.ID+"/dns_records/"+recordID, nil, nil, nil)
	if errors.Is(err, domain.ErrDNSZoneNotFound) {
		// El registro ya no esta: retirarlo es lo que se queria. La zona se comprobo al listar.
		return nil
	}
	return err
}

func toRecord(rec domain.ProviderRecord) record {
	r := record{Type: rec.Type, Name: rec.Name, Content: rec.Content, TTL: autoTTL, Comment: rec.Comment}
	if strings.EqualFold(rec.Type, "MX") {
		p := rec.Priority
		r.Priority = &p
	}
	return r
}

// do hace la llamada y decodifica result en dst. Devuelve la paginacion si la hay.
func (c *Client) do(ctx context.Context, token domain.APIToken, method, path string, query url.Values, body, dst interface{}) (*resultInfo, error) {
	if token.Empty() {
		return nil, domain.ErrDNSProviderTokenInvalid
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: serializar la peticion: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: preparar %s %s: %w", method, path, domain.ErrDNSProviderRejected)
	}
	req.Header.Set("Authorization", "Bearer "+token.Reveal())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// El error de net/http lleva la URL, nunca las cabeceras. Aun asi solo se conserva el tipo.
		return nil, fmt.Errorf("cloudflare: %s %s sin respuesta: %w", method, path, domain.ErrDNSProviderUnavailable)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("cloudflare: %s %s: respuesta ilegible: %w", method, path, domain.ErrDNSProviderUnavailable)
	}
	var env envelope
	decodeErr := json.Unmarshal(raw, &env)
	if resp.StatusCode < 200 || resp.StatusCode > 299 || !env.Success {
		return nil, fmt.Errorf("cloudflare: %s %s: estado %d, codigos %v: %w",
			method, path, resp.StatusCode, codes(env.Errors), classify(resp.StatusCode, env.Errors))
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("cloudflare: %s %s: respuesta no es JSON: %w", method, path, domain.ErrDNSProviderUnavailable)
	}
	if dst != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, dst); err != nil {
			return nil, fmt.Errorf("cloudflare: %s %s: resultado inesperado: %w", method, path, domain.ErrDNSProviderUnavailable)
		}
	}
	return env.ResultInfo, nil
}

func codes(errs []apiError) []int {
	out := make([]int, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

func hasCode(errs []apiError, want ...int) bool {
	for _, e := range errs {
		for _, w := range want {
			if e.Code == w {
				return true
			}
		}
	}
	return false
}

// classify traduce una respuesta de error de Cloudflare a un error de domain.
func classify(status int, errs []apiError) error {
	switch {
	case status == http.StatusTooManyRequests:
		return domain.ErrDNSProviderRateLimited
	case status == http.StatusUnauthorized, hasCode(errs, codeInvalidToken, codeInvalidHeaders, codeInvalidAuthFormat):
		return domain.ErrDNSProviderTokenInvalid
	case status == http.StatusForbidden, hasCode(errs, codeUnauthorized, codeAuthenticationError):
		return domain.ErrDNSProviderPermissionDenied
	case status == http.StatusNotFound:
		return domain.ErrDNSZoneNotFound
	case hasCode(errs, codeIdenticalRecordExists, codeRecordExistsSameName):
		return domain.ErrDNSRecordExists
	case status >= 500, status >= 300 && status < 400, status >= 200 && status < 300:
		// 2xx sin success, una redireccion o un fallo suyo: Cloudflare no respondio como se esperaba.
		return domain.ErrDNSProviderUnavailable
	default:
		return domain.ErrDNSProviderRejected
	}
}
