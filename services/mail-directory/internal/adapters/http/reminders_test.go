package http

import (
	"context"
	"net/http"
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

// httpReminders guarda las filas en memoria; la reclamacion toma todas las pendientes vencidas.
type httpReminders struct {
	rows map[uuid.UUID]*domain.Reminder
	now  func() time.Time
}

var _ ports.ReminderRepository = (*httpReminders)(nil)

func (f *httpReminders) Create(_ context.Context, r *domain.Reminder) error {
	c := *r
	f.rows[r.ID] = &c
	return nil
}

func (f *httpReminders) ListByUsername(_ context.Context, _ uuid.UUID, username, kind string, _ int) ([]domain.Reminder, error) {
	var out []domain.Reminder
	for _, r := range f.rows {
		if r.Username == username && r.Kind == kind && r.Status != domain.ReminderCanceled && r.Status != domain.ReminderDone {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *httpReminders) CountActive(context.Context, uuid.UUID, string) (int, error) { return 0, nil }

func (f *httpReminders) GetForUpdate(_ context.Context, _ uuid.UUID, username string, id uuid.UUID) (*domain.Reminder, error) {
	r, ok := f.rows[id]
	if !ok || r.Username != username {
		return nil, domain.ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *httpReminders) Reschedule(_ context.Context, _, id uuid.UUID, due time.Time) (*domain.Reminder, error) {
	f.rows[id].DueAt = due
	c := *f.rows[id]
	return &c, nil
}

func (f *httpReminders) Cancel(_ context.Context, _, id uuid.UUID) error {
	f.rows[id].Status = domain.ReminderCanceled
	return nil
}

func (f *httpReminders) DeleteByUsername(context.Context, uuid.UUID, string) error { return nil }

func (f *httpReminders) Claim(_ context.Context, p domain.ClaimParams, _ int, _ time.Duration) ([]domain.Reminder, error) {
	out := []domain.Reminder{}
	for _, r := range f.rows {
		if r.Status == domain.ReminderPending && !r.DueAt.After(f.now()) && len(out) < p.Limit {
			lease := f.now().Add(p.Lease)
			r.Status, r.Attempts, r.LeaseUntil = domain.ReminderRunning, r.Attempts+1, &lease
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *httpReminders) ClaimedForUpdate(_ context.Context, id uuid.UUID) (*domain.Reminder, error) {
	r, ok := f.rows[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *httpReminders) Close(_ context.Context, id uuid.UUID, t domain.ReminderTransition) (*domain.Reminder, error) {
	r := f.rows[id]
	r.Status, r.Result, r.LastError, r.LeaseUntil = t.Status, t.Result, t.Error, nil
	c := *r
	return &c, nil
}

type httpQuickReplies struct {
	rows  map[uuid.UUID]*domain.QuickReply
	limit int
}

var _ ports.QuickReplyRepository = (*httpQuickReplies)(nil)

func (f *httpQuickReplies) ListByUsername(_ context.Context, _ uuid.UUID, username string) ([]domain.QuickReply, error) {
	var out []domain.QuickReply
	for _, q := range f.rows {
		if q.Username == username {
			out = append(out, *q)
		}
	}
	return out, nil
}

func (f *httpQuickReplies) Count(context.Context, uuid.UUID, string) (int, error) {
	return f.limit, nil
}

func (f *httpQuickReplies) Create(_ context.Context, q *domain.QuickReply) error {
	for _, other := range f.rows {
		if strings.EqualFold(other.Name, q.Name) {
			return domain.ErrAlreadyExists
		}
	}
	c := *q
	f.rows[q.ID] = &c
	return nil
}

func (f *httpQuickReplies) Update(_ context.Context, q *domain.QuickReply) error {
	if _, ok := f.rows[q.ID]; !ok {
		return domain.ErrNotFound
	}
	c := *q
	f.rows[q.ID] = &c
	return nil
}

func (f *httpQuickReplies) Delete(_ context.Context, _ uuid.UUID, _ string, id uuid.UUID) error {
	if _, ok := f.rows[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.rows, id)
	return nil
}

func (f *httpQuickReplies) DeleteByUsername(context.Context, uuid.UUID, string) error { return nil }

type remindersEnv struct {
	*settingsEnv
	reminders    *httpReminders
	quickReplies *httpQuickReplies
}

func remindersServer(t *testing.T) *remindersEnv {
	t.Helper()
	base := settingsServer(t)
	e := &remindersEnv{settingsEnv: base, quickReplies: &httpQuickReplies{rows: map[uuid.UUID]*domain.QuickReply{}}}
	e.reminders = &httpReminders{rows: map[uuid.UUID]*domain.Reminder{}, now: func() time.Time { return e.now }}
	uc := app.New(app.Deps{
		Tx: vacTx{}, Mailboxes: base.mailboxes, Retirements: vacRetirements{}, Locator: &vacLocator{m: base.m},
		Reminders: e.reminders, QuickReplies: e.quickReplies, Clock: func() time.Time { return e.now },
	})
	e.h = middleware.InjectFromGateway(NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", "")).Routes())
	return e
}

func TestRecordatoriosInternos(t *testing.T) {
	e := remindersServer(t)
	due := e.now.Add(time.Hour).Format(time.RFC3339)
	rec := e.do(http.MethodPost, "/internal/mail-directory/reminders",
		`{"username":"ana@acme.test","kind":"snooze","message_id":"a@acme.test","folder":"Snoozed","uid_validity":3,"uid":8,"return_folder":"INBOX","due_at":"`+due+`","subject":"Hola","addresses":["b@otro.example"]}`)
	var created domain.Reminder
	decodeEnvelope(t, rec, &created)
	if rec.Code != http.StatusCreated || created.ID == uuid.Nil || created.Status != domain.ReminderPending || created.ReturnFolder != "INBOX" {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPost, "/internal/mail-directory/reminders",
		`{"username":"ana@acme.test","kind":"snooze","message_id":"b@acme.test","folder":"Snoozed","uid_validity":3,"uid":8,"due_at":"`+due+`"}`), "return_folder")

	rec = e.do(http.MethodGet, "/internal/mail-directory/reminders?username=ana@acme.test&kind=snooze", "")
	var list []domain.Reminder
	decodeEnvelope(t, rec, &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].Addresses[0] != "b@otro.example" {
		t.Fatalf("listar: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodGet, "/internal/mail-directory/reminders?username=ana@acme.test&kind=todo", ""), "kind")

	path := "/internal/mail-directory/reminders/" + created.ID.String()
	later := e.now.Add(2 * time.Hour).Format(time.RFC3339)
	rec = e.do(http.MethodPatch, path+"?username=ana@acme.test", `{"due_at":"`+later+`"}`)
	var moved domain.Reminder
	decodeEnvelope(t, rec, &moved)
	if rec.Code != http.StatusOK || moved.DueAt.Format(time.RFC3339) != later {
		t.Fatalf("reprogramar: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPatch, path+"?username=ana@acme.test", `{"due_at":"2020-01-01T00:00:00Z"}`), "due_at")
	if rec := e.do(http.MethodPatch, path+"?username=luis@acme.test", `{"due_at":"`+later+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("otro buzon: %d", rec.Code)
	}

	e.now = e.now.Add(3 * time.Hour)
	rec = e.do(http.MethodPost, "/internal/mail-directory/reminders/claim", `{"limit":5,"lease_seconds":120}`)
	var claimed []domain.Reminder
	decodeEnvelope(t, rec, &claimed)
	if rec.Code != http.StatusOK || len(claimed) != 1 || claimed[0].Status != domain.ReminderRunning {
		t.Fatalf("reclamar: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPost, "/internal/mail-directory/reminders/claim", `{"lease_seconds":5}`), "lease_seconds")
	if rec := e.do(http.MethodDelete, path+"?username=ana@acme.test", ""); rec.Code != http.StatusConflict ||
		decodeEnvelope(t, rec, nil).Error.Code != codeReminderNotPending {
		t.Fatalf("uno en curso no se cancela: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPost, path+"/finish", `{"status":"done","result":"replied"}`), "result")
	rec = e.do(http.MethodPost, path+"/finish", `{"status":"done","result":"returned"}`)
	var finished domain.Reminder
	decodeEnvelope(t, rec, &finished)
	if rec.Code != http.StatusOK || finished.Status != domain.ReminderDone || finished.Result != domain.ReminderReturned {
		t.Fatalf("cerrar: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPost, path+"/finish", `{"status":"done","result":"returned"}`); rec.Code != http.StatusConflict ||
		decodeEnvelope(t, rec, nil).Error.Code != codeReminderNotClaimed {
		t.Fatalf("cerrar dos veces: %d %s", rec.Code, rec.Body)
	}
}

func TestRespuestasRapidasInternas(t *testing.T) {
	e := remindersServer(t)
	base := "/internal/mail-directory/quick-replies?username=ana@acme.test"
	rec := e.do(http.MethodPost, base, `{"name":"Gracias","html":"<p>Hola {nombre}</p>","text":"Hola {nombre}"}`)
	var created quickReplyItem
	decodeEnvelope(t, rec, &created)
	if rec.Code != http.StatusCreated || created.ID == uuid.Nil || created.Name != "Gracias" {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "tenant_id") || strings.Contains(rec.Body.String(), "username") {
		t.Fatalf("la respuesta no lleva empresa ni buzon: %s", rec.Body)
	}
	if rec := e.do(http.MethodPost, base, `{"name":"gracias","text":"x"}`); rec.Code != http.StatusConflict {
		t.Fatalf("nombre repetido: %d %s", rec.Code, rec.Body)
	}
	expectField(t, e.do(http.MethodPost, base, `{"name":"","text":"x"}`), "name")

	rec = e.do(http.MethodGet, base, "")
	var list quickRepliesResponse
	decodeEnvelope(t, rec, &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 || list.Limits.MaxItems != domain.MaxQuickReplies ||
		list.Limits.MaxNameChars != domain.MaxQuickReplyNameRunes || list.Limits.MaxHTMLBytes != domain.MaxQuickReplyHTMLBytes {
		t.Fatalf("listar: %d %s", rec.Code, rec.Body)
	}

	item := "/internal/mail-directory/quick-replies/" + created.ID.String() + "?username=ana@acme.test"
	rec = e.do(http.MethodPut, item, `{"name":"Agradecer","text":"Gracias, {nombre}"}`)
	var updated quickReplyItem
	decodeEnvelope(t, rec, &updated)
	if rec.Code != http.StatusOK || updated.Name != "Agradecer" {
		t.Fatalf("cambiar: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodDelete, item, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("borrar: %d", rec.Code)
	}
	if rec := e.do(http.MethodDelete, item, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("borrar dos veces: %d", rec.Code)
	}
	e.quickReplies.limit = domain.MaxQuickReplies
	if rec := e.do(http.MethodPost, base, `{"name":"Otra","text":"x"}`); rec.Code != http.StatusConflict ||
		decodeEnvelope(t, rec, nil).Error.Code != codeQuickReplyLimit {
		t.Fatalf("tope: %d %s", rec.Code, rec.Body)
	}
}

func TestRutasDeRecordatoriosCerradasALasPersonas(t *testing.T) {
	e := remindersServer(t)
	id := uuid.NewString()
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/internal/mail-directory/reminders"},
		{http.MethodGet, "/internal/mail-directory/reminders?username=ana@acme.test&kind=snooze"},
		{http.MethodPost, "/internal/mail-directory/reminders/claim"},
		{http.MethodPost, "/internal/mail-directory/reminders/" + id + "/finish"},
		{http.MethodDelete, "/internal/mail-directory/reminders/" + id + "?username=ana@acme.test"},
		{http.MethodGet, "/internal/mail-directory/quick-replies?username=ana@acme.test"},
		{http.MethodPost, "/internal/mail-directory/quick-replies?username=ana@acme.test"},
		{http.MethodPut, "/internal/mail-directory/quick-replies/" + id + "?username=ana@acme.test"},
	} {
		if rec := e.do(c.method, c.path, `{}`, uuid.NewString()); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s con usuario: %d", c.method, c.path, rec.Code)
		}
	}
}
