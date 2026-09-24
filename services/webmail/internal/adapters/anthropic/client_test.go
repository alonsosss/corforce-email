package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const testKey = "clave-de-prueba"

// fakeAPI responde en orden las respuestas dadas y guarda lo que recibe. No habla con la API real.
type fakeAPI struct {
	mu        sync.Mutex
	responses []fakeResponse
	requests  []map[string]any
	headers   []http.Header
}

type fakeResponse struct {
	status     int
	body       string
	retryAfter string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.requests = append(f.requests, body)
	f.headers = append(f.headers, r.Header.Clone())
	resp := f.responses[0]
	if len(f.responses) > 1 {
		f.responses = f.responses[1:]
	}
	if resp.retryAfter != "" {
		w.Header().Set("retry-after", resp.retryAfter)
	}
	w.Header().Set("request-id", "req_prueba")
	w.WriteHeader(resp.status)
	_, _ = io.WriteString(w, resp.body)
}

func okBody(text, stop string) string {
	b, _ := json.Marshal(map[string]any{
		"model": "claude-haiku-4-5", "stop_reason": stop,
		"content": []map[string]any{{"type": "thinking", "thinking": ""}, {"type": "text", "text": text}},
		"usage":   map[string]int{"input_tokens": 120, "output_tokens": 30},
	})
	return string(b)
}

func newTestClient(t *testing.T, api *fakeAPI) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	c, err := New(Config{
		APIKey: testKey, BaseURL: srv.URL, Model: "claude-haiku-4-5", DraftModel: "claude-sonnet-5",
		MaxOutputTokens: 512, Timeout: 5 * time.Second, MaxAttempts: 3,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return c, &waits
}

var summary = domain.AssistantPrompt{Action: domain.AssistantSummarize, System: "reglas", User: "<correo>\nhola\n</correo>"}

func TestPeticionConCabecerasModeloYTope(t *testing.T) {
	api := &fakeAPI{responses: []fakeResponse{{status: 200, body: okBody("Resumen", "end_turn")}}}
	c, _ := newTestClient(t, api)
	out, err := c.Complete(context.Background(), summary)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "Resumen" || out.Truncated || out.InputTokens != 120 || out.OutputTokens != 30 || out.Model != "claude-haiku-4-5" {
		t.Fatalf("respuesta: %+v", out)
	}
	h := api.headers[0]
	if h.Get("x-api-key") != testKey || h.Get("anthropic-version") != APIVersion || h.Get("Content-Type") != "application/json" {
		t.Fatalf("cabeceras: %v", h)
	}
	req := api.requests[0]
	if req["model"] != "claude-haiku-4-5" || req["max_tokens"].(float64) != 512 || req["system"] != "reglas" {
		t.Fatalf("peticion: %v", req)
	}
	if _, ok := req["output_config"]; ok {
		t.Fatal("sin esquema no se pide salida estructurada")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("un solo mensaje de usuario, sin relleno del asistente: %v", msgs)
	}
}

func TestRedaccionUsaSuModeloYExtraccionPideEsquema(t *testing.T) {
	api := &fakeAPI{responses: []fakeResponse{{status: 200, body: okBody("ok", "end_turn")}}}
	c, _ := newTestClient(t, api)
	if _, err := c.Complete(context.Background(), domain.AssistantPrompt{Drafting: true, System: "s", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if api.requests[0]["model"] != "claude-sonnet-5" {
		t.Fatalf("la redaccion va al modelo de redaccion: %v", api.requests[0]["model"])
	}
	schema := map[string]any{"type": "object"}
	if _, err := c.Complete(context.Background(), domain.AssistantPrompt{System: "s", User: "u", JSONSchema: schema}); err != nil {
		t.Fatal(err)
	}
	oc, ok := api.requests[1]["output_config"].(map[string]any)
	if !ok || oc["format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("salida estructurada: %v", api.requests[1])
	}
}

func TestReintenta429Y529RespetandoRetryAfter(t *testing.T) {
	api := &fakeAPI{responses: []fakeResponse{
		{status: 429, body: `{"type":"error","error":{"type":"rate_limit_error","message":"x"}}`, retryAfter: "2"},
		{status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"x"}}`},
		{status: 200, body: okBody("tras reintentos", "end_turn")},
	}}
	c, waits := newTestClient(t, api)
	out, err := c.Complete(context.Background(), summary)
	if err != nil || out.Text != "tras reintentos" {
		t.Fatalf("%+v %v", out, err)
	}
	if len(*waits) != 2 || (*waits)[0] != 2*time.Second || (*waits)[1] <= 0 || (*waits)[1] > maxRetryWait {
		t.Fatalf("esperas: %v", *waits)
	}
}

func TestSaturadoTrasAgotarIntentos(t *testing.T) {
	api := &fakeAPI{responses: []fakeResponse{{status: 503, body: `{}`, retryAfter: "600"}}}
	c, waits := newTestClient(t, api)
	_, err := c.Complete(context.Background(), summary)
	if !errors.Is(err, domain.ErrAssistantBusy) {
		t.Fatalf("5xx persistente: %v", err)
	}
	if len(api.requests) != 3 {
		t.Fatalf("intentos: %d", len(api.requests))
	}
	for _, w := range *waits {
		if w > maxRetryWait {
			t.Fatalf("retry-after se acota: %v", w)
		}
	}
}

func TestErrorDePeticionNoSeReintenta(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413} {
		api := &fakeAPI{responses: []fakeResponse{{status: status, body: `{"type":"error","error":{"type":"invalid_request_error"}}`}}}
		c, _ := newTestClient(t, api)
		if _, err := c.Complete(context.Background(), summary); !errors.Is(err, domain.ErrAssistantFailed) {
			t.Fatalf("%d: %v", status, err)
		}
		if len(api.requests) != 1 {
			t.Fatalf("%d se reintento", status)
		}
	}
}

func TestRechazoYCorte(t *testing.T) {
	api := &fakeAPI{responses: []fakeResponse{{status: 200, body: okBody("", "refusal")}}}
	c, _ := newTestClient(t, api)
	if _, err := c.Complete(context.Background(), summary); !errors.Is(err, domain.ErrAssistantRefused) {
		t.Fatalf("rechazo: %v", err)
	}
	api = &fakeAPI{responses: []fakeResponse{{status: 200, body: okBody("a medias", "max_tokens")}}}
	c, _ = newTestClient(t, api)
	out, err := c.Complete(context.Background(), summary)
	if err != nil || !out.Truncated || out.Text != "a medias" {
		t.Fatalf("corte por tope: %+v %v", out, err)
	}
}

func TestRespuestaIlegibleODemasiadoGrande(t *testing.T) {
	api := &fakeAPI{responses: []fakeResponse{{status: 200, body: "no es json"}}}
	c, _ := newTestClient(t, api)
	if _, err := c.Complete(context.Background(), summary); !errors.Is(err, domain.ErrAssistantFailed) {
		t.Fatalf("ilegible: %v", err)
	}
	api = &fakeAPI{responses: []fakeResponse{{status: 200, body: `{"content":[{"type":"text","text":"` + strings.Repeat("a", maxResponseBytes) + `"}]}`}}}
	c, _ = newTestClient(t, api)
	if _, err := c.Complete(context.Background(), summary); !errors.Is(err, domain.ErrAssistantFailed) {
		t.Fatalf("demasiado grande: %v", err)
	}
}

func TestConfiguracionInvalida(t *testing.T) {
	base := Config{APIKey: testKey, BaseURL: "https://api.example.com", Model: "m", DraftModel: "m", MaxOutputTokens: 1, Timeout: time.Second, MaxAttempts: 1}
	bad := []func(*Config){
		func(c *Config) { c.APIKey = " " },
		func(c *Config) { c.BaseURL = "ftp://x" },
		func(c *Config) { c.BaseURL = "https://user:pass@api.example.com" },
		func(c *Config) { c.Model = "" },
		func(c *Config) { c.MaxAttempts = 0 },
	}
	for i, mut := range bad {
		c := base
		mut(&c)
		if _, err := New(c, zap.NewNop()); err == nil {
			t.Fatalf("caso %d debe fallar", i)
		}
	}
	if _, err := New(base, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
}

func TestLaClaveNoSigueRedirecciones(t *testing.T) {
	var hit bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	t.Cleanup(other.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	c, err := New(Config{APIKey: testKey, BaseURL: redirect.URL, Model: "m", DraftModel: "m", MaxOutputTokens: 1, Timeout: time.Second, MaxAttempts: 1}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), summary); !errors.Is(err, domain.ErrAssistantFailed) {
		t.Fatalf("redireccion: %v", err)
	}
	if hit {
		t.Fatal("la peticion con la clave no debe seguir una redireccion")
	}
}
