package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// stubInsight es lo que el buzon falso devuelve para conversaciones y fichas.
type stubInsight struct {
	threads      domain.ThreadPage
	threadsQuery domain.ListQuery
	conversation []domain.ConversationMessage
	source       domain.InsightSource
	sourceErr    error
}

func (m *stubMailbox) ListThreads(_ context.Context, folder string, q domain.ListQuery) (domain.ThreadPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listed, m.insight.threadsQuery = folder, q
	return m.insight.threads, nil
}

func (m *stubMailbox) Conversation(context.Context, string, uint32, int) ([]domain.ConversationMessage, error) {
	return m.insight.conversation, nil
}

func (m *stubMailbox) Related(context.Context, string, []string, int) ([]domain.ConversationMessage, error) {
	return nil, nil
}

func (m *stubMailbox) Insight(context.Context, string, uint32) (domain.InsightSource, error) {
	return m.insight.source, m.insight.sourceErr
}

type stubUnsubscriber struct {
	targets []string
	err     error
}

func (u *stubUnsubscriber) OneClick(_ context.Context, target string) error {
	u.targets = append(u.targets, target)
	return u.err
}

// newInsightEnv es newTestEnv con un Unsubscriber que la prueba controla.
func newInsightEnv(t *testing.T, unsub *stubUnsubscriber) *testEnv {
	t.Helper()
	env := &testEnv{mb: &stubMailbox{}, vac: &stubVacations{}, book: &stubAddressBook{}, settings: newStubSettings(), dav: &stubDAV{}}
	deps := testDeps(&memStore{m: map[string]domain.Session{}}, env.mb, nopSender{}, env.vac, env.book, env.settings, env.dav)
	deps.Unsubscriber = unsub
	svc, err := app.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(svc, Config{
		CookieSecure: true, SessionIdle: 30 * time.Minute, SessionMax: 12 * time.Hour,
		MFAChallengeTTL: 5 * time.Minute, IPRateLimiter: unlimited{}, MailboxRateLimiter: unlimited{}, ImageProxyRateLimiter: unlimited{},
		AllowedOrigins:  []string{allowedOrigin},
		MaxMessageBytes: 4096, OperationTimeout: 5 * time.Second, TransferTimeout: 5 * time.Second,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	env.h = h.Routes()
	return env
}

func TestRutasDeLaFichaExigenSesionYOrigen(t *testing.T) {
	env := newInsightEnv(t, &stubUnsubscriber{})
	cookie := login(t, env.h)
	for _, path := range []string{"/threads?folder=INBOX&uid=1", "/sender-insight?folder=INBOX&uid=1", "/folders/INBOX/messages?view=threads"} {
		if rec := do(env.h, http.MethodGet, BasePath+path, nil, nil, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s sin sesion: %d", path, rec.Code)
		}
	}
	rec := do(env.h, http.MethodPost, BasePath+"/unsubscribe", body(`{"folder":"INBOX","uid":1}`), map[string]string{"Content-Type": "application/json"}, cookie)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "ORIGIN_NOT_ALLOWED" {
		t.Fatalf("baja sin Origin: %d %s", rec.Code, rec.Body)
	}
}

func TestListadoPorConversacionesYPestanas(t *testing.T) {
	env := newInsightEnv(t, &stubUnsubscriber{})
	cookie := login(t, env.h)
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	env.mb.insight.threads = domain.ThreadPage{Total: 1, Items: []domain.ThreadSummary{{
		Latest: domain.Envelope{UID: 9, Subject: "Presupuesto", Date: at, Category: domain.CategoryPrimary,
			From: []domain.Address{{Name: "Cliente", Email: "c@cliente.test"}}},
		UIDs: []uint32{9, 4}, Size: 2, Unread: 1,
		Participants: []domain.Address{{Name: "Cliente", Email: "c@cliente.test"}},
	}}}
	rec := do(env.h, http.MethodGet, BasePath+"/folders/INBOX/messages?view=threads&category=primary&page=1", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var rows []threadRowDTO
	envelopeData(t, rec, &rows)
	if len(rows) != 1 || rows[0].UID != 9 || rows[0].Thread.Size != 2 || rows[0].Thread.Unread != 1 ||
		len(rows[0].Thread.UIDs) != 2 || rows[0].Category != "primary" || len(rows[0].Thread.Participants) != 1 {
		t.Fatalf("filas: %+v", rows)
	}
	if env.mb.insight.threadsQuery.Filter.Category != domain.CategoryPrimary {
		t.Fatalf("la pestana debe llegar al buzon: %+v", env.mb.insight.threadsQuery)
	}
	for _, bad := range []string{"view=hilos", "category=promociones"} {
		rec = do(env.h, http.MethodGet, BasePath+"/folders/INBOX/messages?"+bad, nil, nil, cookie)
		if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "VALIDATION_ERROR" {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	rec = do(env.h, http.MethodGet, BasePath+"/folders/INBOX/messages?category=newsletters", nil, nil, cookie)
	if rec.Code != http.StatusOK || env.mb.listQuery.Filter.Category != domain.CategoryNewsletters {
		t.Fatalf("vista por mensajes con pestana: %d %+v", rec.Code, env.mb.listQuery.Filter)
	}
}

func TestConversacionPorHTTP(t *testing.T) {
	env := newInsightEnv(t, &stubUnsubscriber{})
	cookie := login(t, env.h)
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	env.mb.insight.conversation = []domain.ConversationMessage{
		{Folder: "INBOX", MessageID: "a@x", Envelope: domain.Envelope{UID: 4, Date: at}},
		{Folder: "INBOX", MessageID: "b@x", Envelope: domain.Envelope{UID: 9, Date: at.Add(time.Hour)}},
	}
	rec := do(env.h, http.MethodGet, BasePath+"/threads?folder=INBOX&uid=9", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var msgs []conversationMessageDTO
	envelopeData(t, rec, &msgs)
	if len(msgs) != 2 || msgs[0].UID != 4 || msgs[1].MessageID != "b@x" || msgs[1].Folder != "INBOX" {
		t.Fatalf("conversacion: %+v", msgs)
	}
	for _, q := range []string{"folder=&uid=9", "folder=INBOX&uid=0", "folder=INBOX"} {
		if rec := do(env.h, http.MethodGet, BasePath+"/threads?"+q, nil, nil, cookie); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestFichaDelRemitentePorHTTP(t *testing.T) {
	env := newInsightEnv(t, &stubUnsubscriber{})
	cookie := login(t, env.h)
	env.mb.insight.source = domain.InsightSource{
		From: []domain.Address{{Name: "Banco", Email: "avisos@ernpresa.pe"}},
		Headers: domain.MessageHeaders{
			domain.HeaderAuthResults:     {"mx; spf=fail smtp.mailfrom=ernpresa.pe; dkim=none; dmarc=fail"},
			domain.HeaderListUnsubscribe: {"<https://ernpresa.pe/baja?t=secreto>"},
		},
	}
	rec := do(env.h, http.MethodGet, BasePath+"/sender-insight?folder=INBOX&uid=3", nil, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got senderInsightDTO
	envelopeData(t, rec, &got)
	if got.Sender == nil || got.Sender.Email != "avisos@ernpresa.pe" || got.Shield.Level != "danger" || !got.Shield.External {
		t.Fatalf("ficha: %+v", got)
	}
	if got.Shield.Authentication.SPF == nil || *got.Shield.Authentication.SPF != "fail" || got.Shield.Authentication.DKIM == nil {
		t.Fatalf("autenticacion: %+v", got.Shield.Authentication)
	}
	codes := map[string]bool{}
	for _, r := range got.Shield.Reasons {
		codes[r.Code] = true
	}
	for _, want := range []string{"external_sender", "dmarc_fail", "spf_fail", "homoglyph_domain"} {
		if !codes[want] {
			t.Errorf("falta el motivo %s: %+v", want, got.Shield.Reasons)
		}
	}
	if got.Unsubscribe.Method == nil || *got.Unsubscribe.Method != "web" || got.Unsubscribe.URL != "https://ernpresa.pe/baja?t=secreto" {
		t.Fatalf("baja: %+v", got.Unsubscribe)
	}

	env.mb.insight.source = domain.InsightSource{}
	rec = do(env.h, http.MethodGet, BasePath+"/sender-insight?folder=INBOX&uid=3", nil, nil, cookie)
	var empty struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil || string(empty.Data["sender"]) != "null" || rec.Code != http.StatusOK {
		t.Fatalf("sin remitente: %s", rec.Body)
	}
	env.mb.insight.sourceErr = domain.ErrMessageNotFound
	rec = do(env.h, http.MethodGet, BasePath+"/sender-insight?folder=INBOX&uid=3", nil, nil, cookie)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "MESSAGE_NOT_FOUND" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestBajaPorHTTP(t *testing.T) {
	unsub := &stubUnsubscriber{}
	env := newInsightEnv(t, unsub)
	cookie := login(t, env.h)
	env.mb.insight.source = domain.InsightSource{Headers: domain.MessageHeaders{
		domain.HeaderListUnsubscribe:     {"<https://news.tienda.test/u/abc>"},
		domain.HeaderListUnsubscribePost: {"List-Unsubscribe=One-Click"},
	}}
	rec := do(env.h, http.MethodPost, BasePath+"/unsubscribe", body(`{"folder":"INBOX","uid":3}`), jsonOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var res unsubscribeResultDTO
	envelopeData(t, rec, &res)
	if res.Method != "one_click" || res.Target != "news.tienda.test" || len(unsub.targets) != 1 {
		t.Fatalf("baja: %+v %v", res, unsub.targets)
	}
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrUnsubscribeRefused, http.StatusUnprocessableEntity, "UNSUBSCRIBE_TARGET_REFUSED"},
		{domain.ErrUnsubscribeFailed, http.StatusBadGateway, "UNSUBSCRIBE_FAILED"},
	}
	for _, c := range cases {
		unsub.err = c.err
		rec = do(env.h, http.MethodPost, BasePath+"/unsubscribe", body(`{"folder":"INBOX","uid":3}`), jsonOrigin, cookie)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%v: %d %s", c.err, rec.Code, rec.Body)
		}
	}
	env.mb.insight.source = domain.InsightSource{}
	rec = do(env.h, http.MethodPost, BasePath+"/unsubscribe", body(`{"folder":"INBOX","uid":3}`), jsonOrigin, cookie)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "UNSUBSCRIBE_NOT_AVAILABLE" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, b := range []string{`{"folder":"","uid":3}`, `{"folder":"INBOX","uid":0}`} {
		if rec := do(env.h, http.MethodPost, BasePath+"/unsubscribe", body(b), jsonOrigin, cookie); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", b, rec.Code)
		}
	}
	if rec := do(env.h, http.MethodPost, BasePath+"/unsubscribe", body(`{"folder":"INBOX","uid":3,"url":"https://x"}`), jsonOrigin, cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("un campo que no es del contrato (la URL) se rechaza: %d", rec.Code)
	}
}

func TestMetaIncluyeLasPestanas(t *testing.T) {
	env := newInsightEnv(t, &stubUnsubscriber{})
	cookie := login(t, env.h)
	rec := do(env.h, http.MethodGet, BasePath+"/meta", nil, nil, cookie)
	var m metaDTO
	envelopeData(t, rec, &m)
	if m.Limits.MaxThreadMessages != domain.MaxThreadMessages || len(m.InboxCategories) != 3 {
		t.Fatalf("meta: %+v", m)
	}
}
