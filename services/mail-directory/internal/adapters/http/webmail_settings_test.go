package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type settingsMailboxes struct {
	vacMailboxes
	hash string
}

func (f *settingsMailboxes) UpdatePassword(_ context.Context, _, _ uuid.UUID, hash string) error {
	f.hash = hash
	return nil
}

type settingsSecrets struct{ ports.Secrets }

func (settingsSecrets) HashPassword(plain string) (string, error) { return "hash:" + plain, nil }

type settingsEvents struct {
	ports.EventPublisher
	credentials int
}

func (f *settingsEvents) MailboxCredentialsChanged(context.Context, *domain.Mailbox, domain.Credential, []domain.MailboxAttr) error {
	f.credentials++
	return nil
}

type settingsSignatures struct{ saved *domain.MailboxSignature }

func (f *settingsSignatures) ByUsername(context.Context, uuid.UUID, string) (*domain.MailboxSignature, error) {
	if f.saved == nil {
		return nil, domain.ErrNotFound
	}
	c := *f.saved
	return &c, nil
}
func (f *settingsSignatures) Upsert(_ context.Context, s *domain.MailboxSignature) error {
	s.UpdatedAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	c := *s
	f.saved = &c
	return nil
}
func (f *settingsSignatures) DeleteByUsername(context.Context, uuid.UUID, string) error { return nil }

type settingsFilters struct{ saved *domain.MailboxFilters }

func (f *settingsFilters) ByUsername(context.Context, uuid.UUID, string) (*domain.MailboxFilters, error) {
	if f.saved == nil {
		return nil, domain.ErrNotFound
	}
	c := *f.saved
	return &c, nil
}
func (f *settingsFilters) Upsert(_ context.Context, v *domain.MailboxFilters) error {
	c := *v
	f.saved = &c
	return nil
}
func (f *settingsFilters) DeleteByUsername(context.Context, uuid.UUID, string) error { return nil }

// settingsScheduled guarda las filas en memoria; claim y cierre siguen el contrato del puerto.
type settingsScheduled struct {
	ports.ScheduledSendRepository
	rows map[uuid.UUID]*domain.ScheduledSend
}

func (f *settingsScheduled) Create(_ context.Context, s *domain.ScheduledSend) error {
	c := *s
	f.rows[s.ID] = &c
	return nil
}
func (f *settingsScheduled) CountPending(context.Context, uuid.UUID, string) (int, error) {
	return len(f.rows), nil
}
func (f *settingsScheduled) ListByUsername(_ context.Context, _ uuid.UUID, username string, _ int) ([]domain.ScheduledSend, error) {
	out := []domain.ScheduledSend{}
	for _, s := range f.rows {
		if s.Username == username {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (f *settingsScheduled) GetForUpdate(_ context.Context, _ uuid.UUID, username string, id uuid.UUID) (*domain.ScheduledSend, error) {
	s, ok := f.rows[id]
	if !ok || s.Username != username {
		return nil, domain.ErrNotFound
	}
	c := *s
	return &c, nil
}
func (f *settingsScheduled) Reschedule(_ context.Context, _, id uuid.UUID, at time.Time) (*domain.ScheduledSend, error) {
	f.rows[id].SendAt = at
	c := *f.rows[id]
	return &c, nil
}
func (f *settingsScheduled) Cancel(_ context.Context, _, id uuid.UUID) error {
	f.rows[id].Status = domain.ScheduledCanceled
	return nil
}
func (f *settingsScheduled) Claim(_ context.Context, p domain.ClaimParams, _ int, _ time.Duration) ([]domain.ScheduledSend, error) {
	out := []domain.ScheduledSend{}
	for _, s := range f.rows {
		if s.Status == domain.ScheduledPending && len(out) < p.Limit {
			s.Status, s.Attempts = domain.ScheduledSending, s.Attempts+1
			out = append(out, *s)
		}
	}
	return out, nil
}
func (f *settingsScheduled) ClaimedForUpdate(_ context.Context, id uuid.UUID) (*domain.ScheduledSend, error) {
	s, ok := f.rows[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *s
	return &c, nil
}
func (f *settingsScheduled) Close(_ context.Context, id uuid.UUID, t domain.ScheduledTransition) (*domain.ScheduledSend, error) {
	f.rows[id].Status, f.rows[id].LastError = t.Status, t.Error
	c := *f.rows[id]
	return &c, nil
}

type settingsEnv struct {
	h          http.Handler
	m          *domain.Mailbox
	mailboxes  *settingsMailboxes
	events     *settingsEvents
	signatures *settingsSignatures
	filters    *settingsFilters
	scheduled  *settingsScheduled
	now        time.Time
}

func settingsServer(t *testing.T) *settingsEnv {
	t.Helper()
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	e := &settingsEnv{
		m: m, mailboxes: &settingsMailboxes{vacMailboxes: vacMailboxes{m: m}}, events: &settingsEvents{},
		signatures: &settingsSignatures{}, filters: &settingsFilters{},
		scheduled: &settingsScheduled{rows: map[uuid.UUID]*domain.ScheduledSend{}},
		now:       time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	uc := app.New(app.Deps{
		Tx: vacTx{}, Mailboxes: e.mailboxes, Retirements: vacRetirements{}, Locator: &vacLocator{m: m},
		Signatures: e.signatures, Filters: e.filters, Scheduled: e.scheduled, Secrets: settingsSecrets{}, Events: e.events,
		Clock: func() time.Time { return e.now },
	})
	e.h = middleware.InjectFromGateway(NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", "")).Routes())
	return e
}

func (e *settingsEnv) do(method, path, body string, userID ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, u := range userID {
		req.Header.Set("X-User-ID", u)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder, into any) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible %q: %v", rec.Body.String(), err)
	}
	if into != nil && env.Data != nil {
		if err := json.Unmarshal(env.Data, into); err != nil {
			t.Fatalf("data ilegible %s: %v", env.Data, err)
		}
	}
	return env
}

func expectField(t *testing.T, rec *httptest.ResponseRecorder, field string) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("se esperaba 422 y salio %d %s", rec.Code, rec.Body)
	}
	env := decodeEnvelope(t, rec, nil)
	if env.Error == nil || env.Error.Code != "VALIDATION_ERROR" || env.Error.Details["field"] != field {
		t.Fatalf("se esperaba VALIDATION_ERROR en %q: %s", field, rec.Body)
	}
}

func TestFirmaInterna(t *testing.T) {
	e := settingsServer(t)
	rec := e.do(http.MethodGet, "/internal/mail-directory/signature?username=Ana@acme.test", "")
	var got signatureResponse
	decodeEnvelope(t, rec, &got)
	if rec.Code != http.StatusOK || got.Enabled || got.UpdatedAt != nil || got.Limits.MaxHTMLBytes != domain.MaxSignatureHTMLBytes || got.Limits.MaxTextBytes != domain.MaxSignatureTextBytes {
		t.Fatalf("firma por defecto con sus topes: %d %+v", rec.Code, got)
	}
	rec = e.do(http.MethodPut, "/internal/mail-directory/signature?username=ana@acme.test", `{"enabled":true,"html":"<p>Ana</p>","text":"Ana","on_replies":true}`)
	decodeEnvelope(t, rec, &got)
	if rec.Code != http.StatusOK || !got.Enabled || got.HTML != "<p>Ana</p>" || !got.OnReplies || got.UpdatedAt == nil {
		t.Fatalf("guardar: %d %+v", rec.Code, got)
	}
	expectField(t, e.do(http.MethodPut, "/internal/mail-directory/signature?username=ana@acme.test", `{"enabled":true,"html":" "}`), "html")
	if rec := e.do(http.MethodPut, "/internal/mail-directory/signature?username=ana@acme.test", `{"enabled":true,"html":"x","tenant_id":"y"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("campo desconocido: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/internal/mail-directory/signature?username=nadie@acme.test", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("buzon inexistente: %d", rec.Code)
	}
}

func TestReglasInternas(t *testing.T) {
	e := settingsServer(t)
	rec := e.do(http.MethodGet, "/internal/mail-directory/filters?username=ana@acme.test", "")
	var got filtersResponse
	decodeEnvelope(t, rec, &got)
	if rec.Code != http.StatusOK || got.Rules == nil || got.Forwarding.Addresses == nil || got.Limits.MaxRules != domain.MaxFilterRules ||
		got.Limits.MaxConditions != domain.MaxFilterConditions || got.Limits.MaxActions != domain.MaxFilterActions ||
		got.Limits.MaxForwardAddresses != domain.MaxForwardAddresses || got.Limits.MaxValueLength != domain.MaxFilterValueRunes {
		t.Fatalf("reglas por defecto con sus topes: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"rules":[]`) || !strings.Contains(rec.Body.String(), `"addresses":[]`) {
		t.Fatalf("las listas vacias salen como [] y no null: %s", rec.Body)
	}

	body := `{"rules":[{"name":"Clientes","enabled":true,"match":"any","conditions":[{"field":"from","op":"contains","value":"cliente"}],` +
		`"actions":[{"type":"move","folder":"Clientes"},{"type":"forward","address":"fuera@otro.example","keep_copy":true}],"stop":true}],` +
		`"forwarding":{"enabled":false,"addresses":["copia@otro.example"],"keep_copy":false}}`
	rec = e.do(http.MethodPut, "/internal/mail-directory/filters?username=ana@acme.test", body)
	decodeEnvelope(t, rec, &got)
	if rec.Code != http.StatusOK || len(got.Rules) != 1 || got.Rules[0].ID == uuid.Nil || got.Forwarding.Addresses[0] != "copia@otro.example" {
		t.Fatalf("guardar: %d %s", rec.Code, rec.Body)
	}
	if e.filters.saved == nil || !strings.Contains(e.filters.saved.ScriptData, `redirect :copy "fuera@otro.example";`) {
		t.Fatalf("el script no se genero: %+v", e.filters.saved)
	}
	if strings.Contains(rec.Body.String(), "script") || strings.Contains(rec.Body.String(), "require") {
		t.Fatalf("el script no sale por la API: %s", rec.Body)
	}

	expectField(t, e.do(http.MethodPut, "/internal/mail-directory/filters?username=ana@acme.test",
		`{"rules":[{"name":"a","conditions":[{"field":"from","op":"contains","value":"x"}],"actions":[{"type":"flag"}]},`+
			`{"name":"b","conditions":[{"field":"from","op":"contains","value":"x"}],"actions":[{"type":"flag"}]},`+
			`{"name":"c","conditions":[{"field":"from","op":"contains","value":"x"}],"actions":[{"type":"flag"}]},`+
			`{"name":"d","conditions":[{"field":"from","op":"contains","value":"a\nb"}],"actions":[{"type":"flag"}]}]}`),
		"rules[3].conditions[0].value")
	expectField(t, e.do(http.MethodPut, "/internal/mail-directory/filters?username=ana@acme.test",
		`{"forwarding":{"enabled":true,"addresses":["x@otro.example","ana@acme.test"]}}`), "forwarding.addresses[1]")
	expectField(t, e.do(http.MethodPut, "/internal/mail-directory/filters?username=ana@acme.test",
		`{"rules":[{"id":"no-uuid","name":"a","conditions":[{"field":"from","op":"contains","value":"x"}],"actions":[{"type":"flag"}]}]}`), "rules[0].id")
	if rec := e.do(http.MethodPut, "/internal/mail-directory/filters?username=ana@acme.test", `{"rules":[],"script_data":"discard;"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("el usuario no manda el script: %d", rec.Code)
	}
}

func TestContrasenaInterna(t *testing.T) {
	e := settingsServer(t)
	if rec := e.do(http.MethodPut, "/internal/mail-directory/password?username=ana@acme.test", `{"password":"corta"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("politica de contrasena: %d %s", rec.Code, rec.Body)
	}
	rec := e.do(http.MethodPut, "/internal/mail-directory/password?username=ana@acme.test", `{"password":"una-contrasena-larga-1"}`)
	if rec.Code != http.StatusNoContent || e.mailboxes.hash != "hash:una-contrasena-larga-1" || e.events.credentials != 1 {
		t.Fatalf("cambio: %d hash=%q eventos=%d", rec.Code, e.mailboxes.hash, e.events.credentials)
	}
}

func TestRutasDeAjustesCerradasALasPersonas(t *testing.T) {
	e := settingsServer(t)
	id := uuid.NewString()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/internal/mail-directory/signature?username=ana@acme.test"},
		{http.MethodPut, "/internal/mail-directory/filters?username=ana@acme.test"},
		{http.MethodPut, "/internal/mail-directory/password?username=ana@acme.test"},
		{http.MethodPost, "/internal/mail-directory/scheduled-sends"},
		{http.MethodPost, "/internal/mail-directory/scheduled-sends/claim"},
		{http.MethodPost, "/internal/mail-directory/scheduled-sends/" + id + "/finish"},
		{http.MethodDelete, "/internal/mail-directory/scheduled-sends/" + id + "?username=ana@acme.test"},
	} {
		if rec := e.do(c.method, c.path, `{"password":"una-contrasena-larga-1"}`, uuid.NewString()); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s con usuario: %d", c.method, c.path, rec.Code)
		}
	}
	if e.events.credentials != 0 {
		t.Fatal("una persona no cambia la contrasena por la ruta interna")
	}
}

func TestEnviosProgramadosInternos(t *testing.T) {
	e := settingsServer(t)
	sendAt := e.now.Add(time.Hour).Format(time.RFC3339)
	rec := e.do(http.MethodPost, "/internal/mail-directory/scheduled-sends",
		`{"username":"ana@acme.test","message_id":"<a@acme.test>","folder":"Scheduled","uid_validity":3,"uid":8,"send_at":"`+sendAt+`","subject":"Hola","recipients":["b@otro.example"]}`)
	var created domain.ScheduledSend
	decodeEnvelope(t, rec, &created)
	if rec.Code != http.StatusCreated || created.ID == uuid.Nil || created.Status != domain.ScheduledPending || created.Username != "ana@acme.test" {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPost, "/internal/mail-directory/scheduled-sends",
		`{"username":"ana@acme.test","message_id":"<b@acme.test>","folder":"Scheduled","uid_validity":3,"uid":8,"send_at":"`+sendAt+`","recipients":[]}`), "recipients")

	rec = e.do(http.MethodGet, "/internal/mail-directory/scheduled-sends?username=ana@acme.test", "")
	var list []domain.ScheduledSend
	decodeEnvelope(t, rec, &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].Subject != "Hola" || list[0].Recipients[0] != "b@otro.example" {
		t.Fatalf("listar: %d %s", rec.Code, rec.Body)
	}

	path := "/internal/mail-directory/scheduled-sends/" + created.ID.String()
	later := e.now.Add(2 * time.Hour).Format(time.RFC3339)
	rec = e.do(http.MethodPatch, path+"?username=ana@acme.test", `{"send_at":"`+later+`"}`)
	var moved domain.ScheduledSend
	decodeEnvelope(t, rec, &moved)
	if rec.Code != http.StatusOK || moved.SendAt.Format(time.RFC3339) != later {
		t.Fatalf("reprogramar: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPatch, path+"?username=ana@acme.test", `{"send_at":"2020-01-01T00:00:00Z"}`), "send_at")
	if rec := e.do(http.MethodPatch, path+"?username=luis@acme.test", `{"send_at":"`+later+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("otro buzon: %d", rec.Code)
	}

	e.now = e.now.Add(3 * time.Hour)
	rec = e.do(http.MethodPost, "/internal/mail-directory/scheduled-sends/claim", `{"limit":5,"lease_seconds":120}`)
	var claimed []domain.ScheduledSend
	decodeEnvelope(t, rec, &claimed)
	if rec.Code != http.StatusOK || len(claimed) != 1 || claimed[0].Status != domain.ScheduledSending {
		t.Fatalf("reclamar: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPost, "/internal/mail-directory/scheduled-sends/claim", `{"lease_seconds":5}`), "lease_seconds")
	if rec := e.do(http.MethodDelete, path+"?username=ana@acme.test", ""); rec.Code != http.StatusConflict ||
		decodeEnvelope(t, rec, nil).Error.Code != codeScheduledNotPending {
		t.Fatalf("una fila en curso no se cancela: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPost, path+"/finish", `{"status":"pending"}`), "status")
	rec = e.do(http.MethodPost, path+"/finish", `{"status":"sent"}`)
	var finished domain.ScheduledSend
	decodeEnvelope(t, rec, &finished)
	if rec.Code != http.StatusOK || finished.Status != domain.ScheduledSent {
		t.Fatalf("cerrar: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPost, path+"/finish", `{"status":"sent"}`); rec.Code != http.StatusConflict ||
		decodeEnvelope(t, rec, nil).Error.Code != codeScheduledNotClaimed {
		t.Fatalf("cerrar dos veces: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPost, "/internal/mail-directory/scheduled-sends/no-uuid/finish", `{"status":"sent"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("id malformado: %d", rec.Code)
	}
}
