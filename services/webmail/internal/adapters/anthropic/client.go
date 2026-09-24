// Package anthropic es el proveedor del asistente del webmail: la API de mensajes de Claude
// (POST /v1/messages) por HTTPS directo, sin SDK (docs/adr/0015).
//
// Envia solo lo que el caso de uso ya compuso (instrucciones del sistema y el texto delimitado) y no
// guarda nada: ni la peticion ni la respuesta se registran, solo su estado, su duracion, el modelo y
// los tokens. Los fallos transitorios (429, 5xx, 529 y cortes de red) se reintentan con espera
// exponencial y respetando retry-after, dentro del plazo de la peticion.
package anthropic

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const (
	// APIVersion es la version de la API de mensajes que habla este cliente.
	APIVersion       = "2023-06-01"
	messagesPath     = "/v1/messages"
	maxResponseBytes = 1 << 20
	maxRetryWait     = 10 * time.Second
	baseRetryWait    = 500 * time.Millisecond

	stopMaxTokens = "max_tokens"
	stopRefusal   = "refusal"
)

// Config del proveedor. APIKey llega del almacen de secretos (ANTHROPIC_API_KEY).
type Config struct {
	APIKey  string
	BaseURL string
	// Model sirve el resumen y la extraccion; DraftModel, la redaccion (respuesta y tono).
	Model      string
	DraftModel string
	// MaxOutputTokens acota lo que genera cada peticion, y con ello su coste.
	MaxOutputTokens int
	// Timeout acota cada intento; MaxAttempts cuenta el primero.
	Timeout     time.Duration
	MaxAttempts int
}

// Client implementa ports.AssistantProvider.
type Client struct {
	cfg      Config
	endpoint string
	http     *http.Client
	logger   *zap.Logger
	// sleep espera entre intentos; las pruebas lo sustituyen para no esperar de verdad.
	sleep func(ctx context.Context, d time.Duration) error
}

// New valida la configuracion. Una URL que no es http(s) absoluta, sin clave o sin modelo es un error
// de despliegue.
func New(cfg Config, logger *zap.Logger) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("URL del proveedor del asistente inválida: %q", cfg.BaseURL)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("falta la clave del proveedor del asistente")
	}
	if cfg.Model == "" || cfg.DraftModel == "" {
		return nil, errors.New("faltan los modelos del asistente")
	}
	if cfg.MaxOutputTokens < 1 || cfg.Timeout <= 0 || cfg.MaxAttempts < 1 {
		return nil, errors.New("tope de tokens, plazo e intentos del asistente deben ser positivos")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &Client{
		cfg: cfg, endpoint: u.String() + messagesPath, logger: logger,
		// Sin redirecciones: la clave viaja en una cabecera y no debe seguir a otro destino.
		http: &http.Client{Timeout: cfg.Timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		sleep: sleepCtx,
	}, nil
}

type textMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type outputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}

type outputConfig struct {
	Format *outputFormat `json:"format,omitempty"`
}

type messagesRequest struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	System       string        `json:"system"`
	Messages     []textMessage `json:"messages"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type messagesResponse struct {
	Model      string         `json:"model"`
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type errorResponse struct {
	Error struct {
		Type string `json:"type"`
	} `json:"error"`
}

// Complete envia la peticion y devuelve el texto de la respuesta.
func (c *Client) Complete(ctx context.Context, p domain.AssistantPrompt) (domain.AssistantCompletion, error) {
	model := c.cfg.Model
	if p.Drafting {
		model = c.cfg.DraftModel
	}
	req := messagesRequest{
		Model: model, MaxTokens: c.cfg.MaxOutputTokens, System: p.System,
		Messages: []textMessage{{Role: "user", Content: p.User}},
	}
	if p.JSONSchema != nil {
		req.OutputConfig = &outputConfig{Format: &outputFormat{Type: "json_schema", Schema: p.JSONSchema}}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return domain.AssistantCompletion{Model: model}, fmt.Errorf("%w: %v", domain.ErrAssistantFailed, err)
	}

	var last error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		resp, wait, err := c.attempt(ctx, body)
		if err == nil {
			return c.completion(model, resp)
		}
		last = err
		if wait < 0 || attempt == c.cfg.MaxAttempts || ctx.Err() != nil {
			break
		}
		if wait == 0 {
			wait = backoff(attempt)
		}
		if serr := c.sleep(ctx, wait); serr != nil {
			break
		}
	}
	return domain.AssistantCompletion{Model: model}, last
}

// attempt hace un intento. wait es la espera antes del siguiente: negativa si reintentar no sirve, cero
// para la exponencial y positiva si el proveedor la indico (retry-after).
func (c *Client) attempt(ctx context.Context, body []byte) (*messagesResponse, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, -1, fmt.Errorf("%w: %v", domain.ErrAssistantFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", APIVersion)
	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, -1, fmt.Errorf("%w: %v", domain.ErrAssistantBusy, ctx.Err())
		}
		c.logger.Warn("webmail: el proveedor del asistente no respondio", zap.Duration("elapsed", time.Since(started)), zap.Error(err))
		return nil, 0, fmt.Errorf("%w: %v", domain.ErrAssistantBusy, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: lectura de la respuesta: %v", domain.ErrAssistantBusy, err)
	}
	requestID := resp.Header.Get("request-id")
	if resp.StatusCode == http.StatusOK {
		if len(raw) > maxResponseBytes {
			return nil, -1, fmt.Errorf("%w: respuesta demasiado grande", domain.ErrAssistantFailed)
		}
		var out messagesResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, -1, fmt.Errorf("%w: respuesta ilegible", domain.ErrAssistantFailed)
		}
		return &out, 0, nil
	}
	var e errorResponse
	_ = json.Unmarshal(raw, &e)
	c.logger.Warn("webmail: el proveedor del asistente rechazo la peticion", zap.Int("status", resp.StatusCode),
		zap.String("error_type", e.Error.Type), zap.String("request_id", requestID), zap.Duration("elapsed", time.Since(started)))
	if retryable(resp.StatusCode) {
		return nil, retryAfter(resp.Header.Get("retry-after")), fmt.Errorf("%w: el proveedor respondió %d", domain.ErrAssistantBusy, resp.StatusCode)
	}
	return nil, -1, fmt.Errorf("%w: el proveedor respondió %d (%s)", domain.ErrAssistantFailed, resp.StatusCode, e.Error.Type)
}

func (c *Client) completion(model string, r *messagesResponse) (domain.AssistantCompletion, error) {
	out := domain.AssistantCompletion{Model: model, InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
	if r.StopReason == stopRefusal {
		return out, domain.ErrAssistantRefused
	}
	var b strings.Builder
	for _, block := range r.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	out.Text = b.String()
	out.Truncated = r.StopReason == stopMaxTokens
	return out, nil
}

// retryable son los estados que un reintento puede arreglar: limite de peticiones, fallo interno,
// pasarela y saturacion (529).
func retryable(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// retryAfter lee los segundos de retry-after, acotados: el usuario espera la respuesta.
func retryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	if d := time.Duration(secs) * time.Second; d < maxRetryWait {
		return d
	}
	return maxRetryWait
}

// backoff es la espera exponencial con variacion aleatoria tras el intento n.
func backoff(n int) time.Duration {
	d := baseRetryWait << (n - 1)
	if d > maxRetryWait {
		d = maxRetryWait
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
