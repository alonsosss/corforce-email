package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const (
	asTenantA  = "11111111-1111-4111-8111-111111111111"
	asTenantB  = "33333333-3333-4333-8333-333333333333"
	asMailboxA = "22222222-2222-4222-8222-222222222222"
)

type fakeProvider struct {
	mu      sync.Mutex
	prompts []domain.AssistantPrompt
	reply   domain.AssistantCompletion
	err     error
}

func (p *fakeProvider) Complete(_ context.Context, pr domain.AssistantPrompt) (domain.AssistantCompletion, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append(p.prompts, pr)
	out := p.reply
	if out.Model == "" {
		out.Model = "modelo-de-prueba"
	}
	return out, p.err
}

type fakeAssistantSettings struct {
	enabled map[string]bool
	err     error
	calls   int
}

func (f *fakeAssistantSettings) AssistantEnabled(_ context.Context, username string) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.enabled[username], nil
}

// fakeQuota aplica los topes como el script de Redis: comprobar y anotar juntos.
type fakeQuota struct {
	mailbox map[string]int
	tenant  map[string]int
	err     error
}

func (q *fakeQuota) Consume(_ context.Context, tenantID, username string, _ time.Time, l domain.AssistantLimits) (domain.AssistantUsage, error) {
	if q.err != nil {
		return domain.AssistantUsage{}, q.err
	}
	if q.mailbox[username] >= l.MailboxDaily {
		return domain.AssistantUsage{}, &domain.AssistantQuotaError{Scope: domain.QuotaMailbox, Limit: l.MailboxDaily}
	}
	if q.tenant[tenantID] >= l.TenantDaily {
		return domain.AssistantUsage{}, &domain.AssistantQuotaError{Scope: domain.QuotaTenant, Limit: l.TenantDaily}
	}
	q.mailbox[username]++
	q.tenant[tenantID]++
	return domain.AssistantUsage{Mailbox: q.mailbox[username], Tenant: q.tenant[tenantID]}, nil
}

func (q *fakeQuota) Usage(_ context.Context, tenantID, username string, _ time.Time) (domain.AssistantUsage, error) {
	return domain.AssistantUsage{Mailbox: q.mailbox[username], Tenant: q.tenant[tenantID]}, nil
}

type fakeAssistantAudit struct {
	records []domain.AssistantUsageRecord
	err     error
}

func (a *fakeAssistantAudit) AssistantUsed(_ context.Context, rec domain.AssistantUsageRecord) error {
	a.records = append(a.records, rec)
	return a.err
}

type fakeAssistantMetrics struct {
	outcomes     []string
	tokens       int
	auditFailure int
}

func (m *fakeAssistantMetrics) AssistantRequest(action domain.AssistantAction, outcome string) {
	m.outcomes = append(m.outcomes, string(action)+":"+outcome)
}
func (m *fakeAssistantMetrics) AssistantTokens(_ string, in, out int)                  { m.tokens += in + out }
func (m *fakeAssistantMetrics) AssistantLatency(domain.AssistantAction, time.Duration) {}
func (m *fakeAssistantMetrics) AssistantAuditFailed()                                  { m.auditFailure++ }
func (m *fakeAssistantMetrics) String() string                                         { return strings.Join(m.outcomes, ",") }
func (m *fakeAssistantMetrics) tokensSeen() int                                        { return m.tokens }
func (m *fakeAssistantMetrics) failures() int                                          { return m.auditFailure }

func (m *fakeAssistantMetrics) contains(action domain.AssistantAction, outcome string) bool {
	for _, o := range m.outcomes {
		if o == string(action)+":"+outcome {
			return true
		}
	}
	return false
}

type fakeSource struct {
	msgs  map[domain.MessageRef]domain.AssistantSourceMessage
	calls int
}

func (s *fakeSource) AssistantMessages(_ context.Context, _ domain.Session, refs []domain.MessageRef) ([]domain.AssistantSourceMessage, error) {
	s.calls++
	out := make([]domain.AssistantSourceMessage, 0, len(refs))
	for _, r := range refs {
		m, ok := s.msgs[r]
		if !ok {
			return nil, domain.ErrMessageNotFound
		}
		out = append(out, m)
	}
	return out, nil
}

type assistantHarness struct {
	svc      *AssistantService
	provider *fakeProvider
	settings *fakeAssistantSettings
	quota    *fakeQuota
	audit    *fakeAssistantAudit
	metrics  *fakeAssistantMetrics
	source   *fakeSource
	clock    *testClock
}

var assistantTestLimits = domain.AssistantLimits{MaxInputChars: 300, MaxThreadMessages: 3, MaxInstructionChars: 50, MailboxDaily: 2, TenantDaily: 3}

func newAssistantHarness(t *testing.T, withProvider bool) *assistantHarness {
	t.Helper()
	h := &assistantHarness{
		provider: &fakeProvider{reply: domain.AssistantCompletion{Text: "  Texto propuesto  ", InputTokens: 10, OutputTokens: 5}},
		settings: &fakeAssistantSettings{enabled: map[string]bool{testUser: true, "luis@empresa.pe": true, "eva@empresa.pe": true}},
		quota:    &fakeQuota{mailbox: map[string]int{}, tenant: map[string]int{}},
		audit:    &fakeAssistantAudit{},
		metrics:  &fakeAssistantMetrics{},
		source: &fakeSource{msgs: map[domain.MessageRef]domain.AssistantSourceMessage{
			{Folder: "INBOX", UID: 1}: {From: "Luis", Subject: "Pedido", Body: "Necesito el presupuesto el viernes."},
			{Folder: "INBOX", UID: 2}: {From: "Eva", Subject: "Re: Pedido", Body: strings.Repeat("largo ", 200)},
		}},
		clock: &testClock{now: time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)},
	}
	d := AssistantDeps{
		Settings: h.settings, Quota: h.quota, Audit: h.audit, Metrics: h.metrics, Source: h.source,
		Clock: h.clock.Now, Logger: zap.NewNop(),
		Config: AssistantConfig{Limits: assistantTestLimits, SettingsTTL: 30 * time.Second},
	}
	if withProvider {
		d.Provider = h.provider
	}
	svc, err := NewAssistantService(d)
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

func assistantSession(username, tenant string) domain.Session {
	return domain.Session{Username: username, DisplayName: "Ana Perez", TenantID: tenant, MailboxID: asMailboxA}
}

var inbox1 = domain.MessageRef{Folder: "INBOX", UID: 1}

func TestAsistenteSinClaveNoSeOfreceNiLlamaANadie(t *testing.T) {
	h := newAssistantHarness(t, false)
	sess := assistantSession(testUser, asTenantA)
	st, err := h.svc.Status(context.Background(), sess)
	if err != nil || st.Available || st.Reason != "not_configured" {
		t.Fatalf("estado: %+v %v", st, err)
	}
	if _, err := h.svc.Reply(context.Background(), sess, inbox1, ""); !errors.Is(err, domain.ErrAssistantNotConfigured) {
		t.Fatalf("sin clave: %v", err)
	}
	if h.settings.calls != 0 || h.source.calls != 0 || len(h.audit.records) != 0 {
		t.Fatal("sin clave no se consulta el ajuste, ni el buzon, ni se audita nada")
	}
}

func TestAsistenteEmpresaSinActivar(t *testing.T) {
	h := newAssistantHarness(t, true)
	h.settings.enabled[testUser] = false
	sess := assistantSession(testUser, asTenantA)
	st, err := h.svc.Status(context.Background(), sess)
	if err != nil || st.Available || st.Reason != "disabled" {
		t.Fatalf("estado: %+v %v", st, err)
	}
	if _, err := h.svc.Summarize(context.Background(), sess, []domain.MessageRef{inbox1}); !errors.Is(err, domain.ErrAssistantDisabled) {
		t.Fatalf("empresa sin activar: %v", err)
	}
	if len(h.provider.prompts) != 0 || h.source.calls != 0 || h.quota.mailbox[testUser] != 0 {
		t.Fatal("sin activar no se lee el buzon, no se gasta cupo ni sale nada")
	}
	if !h.metrics.contains(domain.AssistantSummarize, domain.AssistantOutcomeDisabled) {
		t.Fatalf("metrica: %s", h.metrics)
	}
}

func TestAsistenteCacheCortaDelAjuste(t *testing.T) {
	h := newAssistantHarness(t, true)
	sess := assistantSession(testUser, asTenantA)
	ctx := context.Background()
	if _, err := h.svc.Status(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Status(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if h.settings.calls != 1 {
		t.Fatalf("dentro de la cache no se vuelve a preguntar: %d", h.settings.calls)
	}
	h.settings.enabled[testUser] = false
	h.clock.Advance(31 * time.Second)
	if st, _ := h.svc.Status(ctx, sess); st.Available || h.settings.calls != 2 {
		t.Fatalf("al caducar se nota el apagado: %+v %d", st, h.settings.calls)
	}

	h.settings.err = errors.New("mail-directory caido")
	h.clock.Advance(31 * time.Second)
	if _, err := h.svc.Reply(ctx, sess, inbox1, ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("un fallo del directorio deja el asistente sin servir: %v", err)
	}
	h.settings.err = nil
	h.settings.enabled[testUser] = true
	if _, err := h.svc.Reply(ctx, sess, inbox1, ""); err != nil {
		t.Fatalf("el fallo no se cachea: %v", err)
	}
}

func TestAsistenteRespuestaYAuditoriaSinContenido(t *testing.T) {
	h := newAssistantHarness(t, true)
	sess := assistantSession(testUser, asTenantA)
	res, err := h.svc.Reply(context.Background(), sess, inbox1, "confirma el viernes")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Texto propuesto" {
		t.Fatalf("texto: %q", res.Text)
	}
	p := h.provider.prompts[0]
	if !p.Drafting || !strings.Contains(p.User, "<correo>") || !strings.Contains(p.User, "confirma el viernes") || !strings.Contains(p.User, "Ana Perez") {
		t.Fatalf("prompt: %+v", p)
	}
	if len(h.audit.records) != 1 {
		t.Fatalf("un apunte por uso: %d", len(h.audit.records))
	}
	rec := h.audit.records[0]
	if rec.TenantID != asTenantA || rec.MailboxID != asMailboxA || rec.Username != testUser || rec.Action != domain.AssistantReply ||
		rec.Outcome != domain.AssistantOutcomeOK || rec.InputChars == 0 || rec.OutputChars != len("Texto propuesto") ||
		rec.InputTokens != 10 || rec.OutputTokens != 5 || rec.Model != "modelo-de-prueba" {
		t.Fatalf("apunte: %+v", rec)
	}
	if h.metrics.tokensSeen() != 15 || !h.metrics.contains(domain.AssistantReply, domain.AssistantOutcomeOK) {
		t.Fatalf("metricas: %s %d", h.metrics, h.metrics.tokensSeen())
	}
}

func TestAsistenteTopesPorBuzonYPorEmpresa(t *testing.T) {
	h := newAssistantHarness(t, true)
	ctx := context.Background()
	ana := assistantSession(testUser, asTenantA)
	for i := 0; i < 2; i++ {
		if _, err := h.svc.Reply(ctx, ana, inbox1, ""); err != nil {
			t.Fatal(err)
		}
	}
	var qerr *domain.AssistantQuotaError
	if _, err := h.svc.Reply(ctx, ana, inbox1, ""); !errors.As(err, &qerr) || qerr.Scope != domain.QuotaMailbox {
		t.Fatalf("tope del buzon: %v", err)
	}
	luis := assistantSession("luis@empresa.pe", asTenantA)
	if _, err := h.svc.Reply(ctx, luis, inbox1, ""); err != nil {
		t.Fatalf("otro buzon de la empresa aun tiene cupo: %v", err)
	}
	eva := assistantSession("eva@empresa.pe", asTenantA)
	if _, err := h.svc.Reply(ctx, eva, inbox1, ""); !errors.As(err, &qerr) || qerr.Scope != domain.QuotaTenant {
		t.Fatalf("tope de la empresa: %v", err)
	}
	other := assistantSession("eva@empresa.pe", asTenantB)
	if _, err := h.svc.Reply(ctx, other, inbox1, ""); err != nil {
		t.Fatalf("otra empresa no comparte cupo: %v", err)
	}
	if len(h.provider.prompts) != 4 {
		t.Fatalf("lo rechazado por cupo no sale: %d", len(h.provider.prompts))
	}
	if !h.metrics.contains(domain.AssistantReply, domain.AssistantOutcomeQuota) {
		t.Fatalf("metrica de cupo: %s", h.metrics)
	}
}

func TestAsistenteEntradaInvalidaNoGastaCupo(t *testing.T) {
	h := newAssistantHarness(t, true)
	ctx := context.Background()
	sess := assistantSession(testUser, asTenantA)
	var verr *domain.ValidationError
	if _, err := h.svc.Tone(ctx, sess, strings.Repeat("a", 301), domain.ToneBrief); !errors.As(err, &verr) {
		t.Fatalf("borrador largo: %v", err)
	}
	if _, err := h.svc.Reply(ctx, sess, inbox1, strings.Repeat("a", 51)); !errors.As(err, &verr) {
		t.Fatalf("indicaciones largas: %v", err)
	}
	if _, err := h.svc.Summarize(ctx, sess, make([]domain.MessageRef, 4)); !errors.As(err, &verr) {
		t.Fatalf("demasiados mensajes: %v", err)
	}
	if _, err := h.svc.Reply(ctx, sess, domain.MessageRef{Folder: "INBOX", UID: 99}, ""); !errors.Is(err, domain.ErrMessageNotFound) {
		t.Fatalf("mensaje inexistente: %v", err)
	}
	if h.quota.mailbox[testUser] != 0 || len(h.provider.prompts) != 0 {
		t.Fatal("una entrada invalida no gasta cupo ni sale")
	}
}

func TestAsistenteConCupoAgotadoNoLeeElBuzon(t *testing.T) {
	h := newAssistantHarness(t, true)
	h.quota.mailbox[testUser] = h.svc.cfg.Limits.MailboxDaily
	var qerr *domain.AssistantQuotaError
	_, err := h.svc.Summarize(context.Background(), assistantSession(testUser, asTenantA), []domain.MessageRef{inbox1})
	if !errors.As(err, &qerr) || qerr.Scope != domain.QuotaMailbox {
		t.Fatalf("cupo agotado: %v", err)
	}
	if h.source.calls != 0 {
		t.Fatalf("con el cupo agotado no se leen mensajes: %d lecturas", h.source.calls)
	}
}

func TestAsistenteTruncaElHiloAlTopeServido(t *testing.T) {
	h := newAssistantHarness(t, true)
	res, err := h.svc.Summarize(context.Background(), assistantSession(testUser, asTenantA),
		[]domain.MessageRef{inbox1, {Folder: "INBOX", UID: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.InputTruncated || h.provider.prompts[0].InputChars > assistantTestLimits.MaxInputChars {
		t.Fatalf("truncado: %+v %d", res, h.provider.prompts[0].InputChars)
	}
}

func TestAsistenteErroresDelProveedor(t *testing.T) {
	cases := []struct {
		err     error
		outcome string
	}{
		{domain.ErrAssistantBusy, domain.AssistantOutcomeBusy},
		{domain.ErrAssistantFailed, domain.AssistantOutcomeFailed},
		{domain.ErrAssistantRefused, domain.AssistantOutcomeRefused},
	}
	for _, c := range cases {
		h := newAssistantHarness(t, true)
		h.provider.err = c.err
		_, err := h.svc.Reply(context.Background(), assistantSession(testUser, asTenantA), inbox1, "")
		if !errors.Is(err, c.err) {
			t.Fatalf("%v: %v", c.err, err)
		}
		if len(h.audit.records) != 1 || h.audit.records[0].Outcome != c.outcome {
			t.Fatalf("un fallo del proveedor tambien se audita: %+v", h.audit.records)
		}
		if h.quota.mailbox[testUser] != 1 {
			t.Fatal("la peticion al proveedor ya gasto cupo")
		}
	}

	h := newAssistantHarness(t, true)
	h.provider.reply = domain.AssistantCompletion{Text: "   "}
	if _, err := h.svc.Tone(context.Background(), assistantSession(testUser, asTenantA), "hola", domain.ToneFormal); !errors.Is(err, domain.ErrAssistantEmptyResult) {
		t.Fatalf("respuesta vacia: %v", err)
	}
}

func TestAsistenteAuditoriaCaidaNoRompeLaRespuestaPeroSeCuenta(t *testing.T) {
	h := newAssistantHarness(t, true)
	h.audit.err = errors.New("nats caido")
	if _, err := h.svc.Reply(context.Background(), assistantSession(testUser, asTenantA), inbox1, ""); err != nil {
		t.Fatal(err)
	}
	if h.metrics.failures() != 1 {
		t.Fatal("el uso sin apunte se cuenta")
	}
}

func TestAsistenteSesionSinEmpresa(t *testing.T) {
	h := newAssistantHarness(t, true)
	legacy := domain.Session{Username: testUser}
	if _, err := h.svc.Reply(context.Background(), legacy, inbox1, ""); !errors.Is(err, domain.ErrSessionInvalid) {
		t.Fatalf("una sesion anterior sin empresa ni buzon debe volver a entrar: %v", err)
	}
	if st, err := h.svc.Status(context.Background(), legacy); err != nil || st.Available {
		t.Fatalf("consultar el estado no cierra la sesion, solo no lo ofrece: %+v %v", st, err)
	}
}

func TestAsistenteExtraccion(t *testing.T) {
	h := newAssistantHarness(t, true)
	h.provider.reply = domain.AssistantCompletion{Text: `{"tasks":[{"title":"Enviar presupuesto","due_date":"2026-09-26"}],"events":[{"title":"Llamada","date":"2026-09-25","start_time":"10:00","end_time":"10:30","location":"","notes":""}]}`}
	x, _, err := h.svc.Extract(context.Background(), assistantSession(testUser, asTenantA), inbox1, "2026-09-24")
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Tasks) != 1 || len(x.Events) != 1 || x.Events[0].EndTime != "10:30" {
		t.Fatalf("extraccion: %+v", x)
	}
	p := h.provider.prompts[0]
	if p.JSONSchema == nil || p.Drafting || !strings.Contains(p.User, "Fecha de hoy: 2026-09-24") {
		t.Fatalf("prompt: %+v", p)
	}
	if h.audit.records[0].OutputChars != 2 {
		t.Fatalf("la auditoria cuenta propuestas, no texto: %+v", h.audit.records[0])
	}

	h.provider.reply = domain.AssistantCompletion{Text: `{"tasks":[`, Truncated: true}
	if _, _, err := h.svc.Extract(context.Background(), assistantSession(testUser, asTenantA), inbox1, ""); !errors.Is(err, domain.ErrAssistantEmptyResult) {
		t.Fatalf("una salida cortada no se interpreta: %v", err)
	}
}

func TestAsistenteLeeElBuzonSinMarcarYSinAdjuntos(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.raw = &domain.RawMessage{
		Envelope: domain.Envelope{From: []domain.Address{{Name: "Luis", Email: "luis@cliente.example"}}, Subject: "Hola"},
		HTML:     "<p>Hola</p>", HTMLTruncated: true,
		Parts: []domain.Part{{ID: "2", Filename: "contrato.pdf"}},
	}
	msgs, err := h.svc.AssistantMessages(context.Background(), sess, []domain.MessageRef{inbox1, {Folder: "INBOX", UID: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Body != "texto plano" || !msgs[0].BodyTruncated || msgs[0].From != "Luis" {
		t.Fatalf("mensajes: %+v", msgs)
	}
	if h.mail.opened != 1 {
		t.Fatalf("una sola conexion para todo el hilo: %d", h.mail.opened)
	}
	if _, err := h.svc.AssistantMessages(context.Background(), sess, []domain.MessageRef{{Folder: "INBOX", UID: 0}}); err == nil {
		t.Fatal("uid 0 no es un mensaje")
	}
}
