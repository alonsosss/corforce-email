package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

type stubAssistantProvider struct {
	text string
	err  error
	last domain.AssistantPrompt
}

func (p *stubAssistantProvider) Complete(_ context.Context, pr domain.AssistantPrompt) (domain.AssistantCompletion, error) {
	p.last = pr
	return domain.AssistantCompletion{Text: p.text, Model: "m"}, p.err
}

type stubAssistantSettings struct{ enabled bool }

func (s *stubAssistantSettings) AssistantEnabled(context.Context, string) (bool, error) {
	return s.enabled, nil
}

type stubAssistantQuota struct{ used int }

func (q *stubAssistantQuota) Consume(_ context.Context, _, _ string, _ time.Time, l domain.AssistantLimits) (domain.AssistantUsage, error) {
	if q.used >= l.MailboxDaily {
		return domain.AssistantUsage{}, &domain.AssistantQuotaError{Scope: domain.QuotaMailbox, Limit: l.MailboxDaily}
	}
	q.used++
	return domain.AssistantUsage{Mailbox: q.used, Tenant: q.used}, nil
}

func (q *stubAssistantQuota) Usage(context.Context, string, string, time.Time) (domain.AssistantUsage, error) {
	return domain.AssistantUsage{Mailbox: q.used, Tenant: q.used}, nil
}

type nopAssistantAudit struct{}

func (nopAssistantAudit) AssistantUsed(context.Context, domain.AssistantUsageRecord) error {
	return nil
}

type nopAssistantMetrics struct{}

func (nopAssistantMetrics) AssistantRequest(domain.AssistantAction, string)        {}
func (nopAssistantMetrics) AssistantTokens(string, int, int)                       {}
func (nopAssistantMetrics) AssistantLatency(domain.AssistantAction, time.Duration) {}
func (nopAssistantMetrics) AssistantAuditFailed()                                  {}

type stubAssistantSource struct{}

func (stubAssistantSource) AssistantMessages(_ context.Context, _ domain.Session, refs []domain.MessageRef) ([]domain.AssistantSourceMessage, error) {
	out := make([]domain.AssistantSourceMessage, 0, len(refs))
	for _, r := range refs {
		if r.UID == 404 {
			return nil, domain.ErrMessageNotFound
		}
		out = append(out, domain.AssistantSourceMessage{From: "Luis", Subject: "Pedido", Body: "Nos vemos el jueves."})
	}
	return out, nil
}

type assistantTestEnv struct {
	h        http.Handler
	provider *stubAssistantProvider
	settings *stubAssistantSettings
	quota    *stubAssistantQuota
}

func newAssistantTestEnv(t *testing.T, configured bool) *assistantTestEnv {
	t.Helper()
	env := newTestEnv(t, nopSender{})
	svc, err := app.New(testDeps(&memStore{m: map[string]domain.Session{}}, env.mb, nopSender{}, env.vac, env.book, env.settings, env.dav))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(svc, Config{
		CookieSecure: true, SessionIdle: 30 * time.Minute, SessionMax: 12 * time.Hour,
		AllowedOrigins: []string{allowedOrigin}, MaxMessageBytes: 4096, OperationTimeout: 5 * time.Second, TransferTimeout: 5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	a := &assistantTestEnv{provider: &stubAssistantProvider{text: "Propuesta"}, settings: &stubAssistantSettings{enabled: true}, quota: &stubAssistantQuota{}}
	deps := app.AssistantDeps{
		Settings: a.settings, Quota: a.quota, Audit: nopAssistantAudit{}, Metrics: nopAssistantMetrics{}, Source: stubAssistantSource{},
		Logger: zap.NewNop(),
		Config: app.AssistantConfig{
			Limits:      domain.AssistantLimits{MaxInputChars: 100, MaxThreadMessages: 2, MaxInstructionChars: 20, MailboxDaily: 2, TenantDaily: 5},
			SettingsTTL: time.Second,
		},
	}
	if configured {
		deps.Provider = a.provider
	}
	assistant, err := app.NewAssistantService(deps)
	if err != nil {
		t.Fatal(err)
	}
	h.SetAssistant(assistant)
	a.h = h.Routes()
	return a
}

func (a *assistantTestEnv) post(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	cookie := login(t, a.h)
	rec := do(a.h, http.MethodPost, BasePath+path, strings.NewReader(body), map[string]string{"Origin": allowedOrigin, "Content-Type": "application/json"}, cookie)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env
}

func TestAPIAsistenteEstado(t *testing.T) {
	a := newAssistantTestEnv(t, true)
	cookie := login(t, a.h)
	rec := do(a.h, http.MethodGet, BasePath+"/assistant", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var env struct {
		Data struct {
			Available bool     `json:"available"`
			Reason    string   `json:"reason"`
			Actions   []string `json:"actions"`
			Tones     []string `json:"tones"`
			Limits    struct {
				MaxInputChars       int `json:"max_input_chars"`
				MaxThreadMessages   int `json:"max_thread_messages"`
				MaxInstructionChars int `json:"max_instruction_chars"`
				MailboxDaily        int `json:"mailbox_daily"`
				TenantDaily         int `json:"tenant_daily"`
			} `json:"limits"`
			Usage struct {
				Mailbox int `json:"mailbox"`
				Tenant  int `json:"tenant"`
			} `json:"usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	d := env.Data
	if !d.Available || d.Reason != "" || len(d.Actions) != 4 || len(d.Tones) != 3 || d.Limits.MaxInputChars != 100 || d.Limits.MailboxDaily != 2 || d.Limits.TenantDaily != 5 {
		t.Fatalf("estado: %+v", d)
	}

	a.settings.enabled = false
	b := newAssistantTestEnv(t, false)
	rec = do(b.h, http.MethodGet, BasePath+"/assistant", nil, nil, login(t, b.h))
	if !strings.Contains(rec.Body.String(), `"reason":"not_configured"`) || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("sin clave: %s", rec.Body)
	}
}

func TestAPIAsistenteExigeSesionYOrigen(t *testing.T) {
	a := newAssistantTestEnv(t, true)
	if rec := do(a.h, http.MethodGet, BasePath+"/assistant", nil, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin sesion: %d", rec.Code)
	}
	cookie := login(t, a.h)
	rec := do(a.h, http.MethodPost, BasePath+"/assistant/tone", strings.NewReader(`{"text":"hola","tone":"formal"}`), map[string]string{"Content-Type": "application/json"}, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("una escritura sin Origin: %d", rec.Code)
	}
}

func TestAPIAsistenteAcciones(t *testing.T) {
	a := newAssistantTestEnv(t, true)
	code, env := a.post(t, "/assistant/reply", `{"folder":"INBOX","uid":1,"instructions":"acepta"}`)
	if code != http.StatusOK || env["data"].(map[string]any)["text"] != "Propuesta" {
		t.Fatalf("reply: %d %v", code, env)
	}
	if !strings.Contains(a.provider.last.User, "acepta") {
		t.Fatal("las indicaciones llegan al prompt")
	}
	a.quota.used = 0
	code, _ = a.post(t, "/assistant/summarize", `{"messages":[{"folder":"INBOX","uid":1},{"folder":"INBOX","uid":2}]}`)
	if code != http.StatusOK {
		t.Fatalf("summarize: %d", code)
	}
	code, env = a.post(t, "/assistant/summarize", `{"messages":[{"folder":"INBOX","uid":1},{"folder":"INBOX","uid":2},{"folder":"INBOX","uid":3}]}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("mas mensajes que el tope: %d %v", code, env)
	}
	code, _ = a.post(t, "/assistant/tone", `{"text":"hola","tone":"sarcastico"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("tono desconocido: %d", code)
	}
	code, _ = a.post(t, "/assistant/tone", `{"text":"hola","tone":"formal","extra":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("campos desconocidos: %d", code)
	}
	code, _ = a.post(t, "/assistant/reply", `{"folder":"INBOX","uid":404}`)
	if code != http.StatusNotFound {
		t.Fatalf("mensaje inexistente: %d", code)
	}
}

func TestAPIAsistenteCupoYErrores(t *testing.T) {
	a := newAssistantTestEnv(t, true)
	a.quota.used = 2
	code, env := a.post(t, "/assistant/tone", `{"text":"hola","tone":"brief"}`)
	errBody, _ := env["error"].(map[string]any)
	if code != http.StatusTooManyRequests || errBody["code"] != "ASSISTANT_QUOTA_EXCEEDED" || errBody["details"].(map[string]any)["scope"] != "mailbox" {
		t.Fatalf("cupo: %d %v", code, env)
	}

	a.quota.used = 0
	a.provider.err = domain.ErrAssistantBusy
	cookie := login(t, a.h)
	rec := do(a.h, http.MethodPost, BasePath+"/assistant/tone", strings.NewReader(`{"text":"hola","tone":"brief"}`),
		map[string]string{"Origin": allowedOrigin, "Content-Type": "application/json"}, cookie)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "ASSISTANT_BUSY" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("saturado: %d %s", rec.Code, rec.Body)
	}

	a.quota.used = 0
	a.provider.err = domain.ErrAssistantFailed
	if code, env := a.post(t, "/assistant/tone", `{"text":"hola","tone":"brief"}`); code != http.StatusBadGateway {
		t.Fatalf("fallo del proveedor: %d %v", code, env)
	}

	a.provider.err = nil
	a.settings.enabled = false
	b := newAssistantTestEnv(t, true)
	b.settings.enabled = false
	if code, env := b.post(t, "/assistant/extract", `{"folder":"INBOX","uid":1,"today":"2026-09-24"}`); code != http.StatusForbidden || env["error"].(map[string]any)["code"] != "ASSISTANT_DISABLED" {
		t.Fatalf("empresa sin activar: %d %v", code, env)
	}
	c := newAssistantTestEnv(t, false)
	if code, env := c.post(t, "/assistant/reply", `{"folder":"INBOX","uid":1}`); code != http.StatusServiceUnavailable || env["error"].(map[string]any)["code"] != "ASSISTANT_NOT_CONFIGURED" {
		t.Fatalf("sin clave: %d %v", code, env)
	}
}

func TestAPIAsistenteExtraccion(t *testing.T) {
	a := newAssistantTestEnv(t, true)
	a.provider.text = `{"tasks":[],"events":[{"title":"Visita","date":"2026-09-25","start_time":"","end_time":"","location":"","notes":""}]}`
	code, env := a.post(t, "/assistant/extract", `{"folder":"INBOX","uid":1,"today":"2026-09-24"}`)
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, env)
	}
	data := env["data"].(map[string]any)
	events := data["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["date"] != "2026-09-25" || len(data["tasks"].([]any)) != 0 {
		t.Fatalf("extraccion: %v", data)
	}
	if code, _ := a.post(t, "/assistant/extract", `{"folder":"INBOX","uid":1,"today":"ayer"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("fecha de hoy invalida: %d", code)
	}
}
