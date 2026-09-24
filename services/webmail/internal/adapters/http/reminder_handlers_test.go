package http

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func (m *stubMailbox) MoveTracked(_ context.Context, _ string, uid uint32, _ string) (domain.AppendedMessage, error) {
	m.moved = append(m.moved, uid)
	return domain.AppendedMessage{UID: uid + 100, UIDValidity: 1}, nil
}

func (m *stubMailbox) HasReply(context.Context, string, string) (bool, error) { return false, nil }

// stubReminders guarda lo que el webmail pide al directorio.
type stubReminders struct {
	mu      sync.Mutex
	rows    map[string]domain.Reminder
	created []domain.NewReminder
	quick   map[string]domain.QuickReply
	quickIn []domain.QuickReply
	seq     int
	err     error
}

func newStubReminders() *stubReminders {
	return &stubReminders{rows: map[string]domain.Reminder{}, quick: map[string]domain.QuickReply{}}
}

func (s *stubReminders) id() string {
	s.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", s.seq)
}

func (s *stubReminders) CreateReminder(_ context.Context, in domain.NewReminder) (domain.Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return domain.Reminder{}, s.err
	}
	s.created = append(s.created, in)
	r := domain.Reminder{
		ID: s.id(), Kind: in.Kind, MessageID: in.MessageID, Folder: in.Folder, UIDValidity: in.UIDValidity, UID: in.UID,
		ReturnFolder: in.ReturnFolder, Subject: in.Subject, Addresses: in.Addresses, DueAt: in.DueAt, Status: domain.ReminderPending,
	}
	s.rows[r.ID] = r
	return r, nil
}

func (s *stubReminders) ListReminders(_ context.Context, _ string, kind domain.ReminderKind) ([]domain.Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.Reminder{}
	for _, r := range s.rows {
		if r.Kind == kind && r.Status == domain.ReminderPending {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *stubReminders) RescheduleReminder(_ context.Context, _, id string, at time.Time) (domain.Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return domain.Reminder{}, domain.ErrReminderNotFound
	}
	r.DueAt = at
	s.rows[id] = r
	return r, nil
}

func (s *stubReminders) CancelReminder(_ context.Context, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return domain.ErrReminderNotFound
	}
	r.Status = domain.ReminderCanceled
	s.rows[id] = r
	return nil
}

func (s *stubReminders) ClaimReminders(context.Context, int, time.Duration) ([]domain.ReminderClaim, error) {
	return nil, nil
}

func (s *stubReminders) FinishReminder(context.Context, string, domain.ReminderOutcome) error {
	return nil
}

func (s *stubReminders) QuickReplies(context.Context, string) (domain.QuickReplyList, error) {
	list := domain.QuickReplyList{Items: []domain.QuickReply{}, Limits: domain.QuickReplyLimits{MaxItems: 100, MaxNameChars: 80, MaxHTMLBytes: 16384, MaxTextBytes: 16384}}
	for _, q := range s.quick {
		list.Items = append(list.Items, q)
	}
	return list, nil
}

func (s *stubReminders) CreateQuickReply(_ context.Context, _ string, q domain.QuickReply) (domain.QuickReply, error) {
	for _, other := range s.quick {
		if strings.EqualFold(other.Name, q.Name) {
			return domain.QuickReply{}, domain.ErrQuickReplyExists
		}
	}
	s.quickIn = append(s.quickIn, q)
	q.ID = s.id()
	s.quick[q.ID] = q
	return q, nil
}

func (s *stubReminders) UpdateQuickReply(_ context.Context, _ string, q domain.QuickReply) (domain.QuickReply, error) {
	if _, ok := s.quick[q.ID]; !ok {
		return domain.QuickReply{}, domain.ErrQuickReplyNotFound
	}
	s.quick[q.ID] = q
	return q, nil
}

func (s *stubReminders) DeleteQuickReply(_ context.Context, _, id string) error {
	if _, ok := s.quick[id]; !ok {
		return domain.ErrQuickReplyNotFound
	}
	delete(s.quick, id)
	return nil
}

func TestPosponerPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	env.mb.raw = "Subject: x\r\n\r\ny"
	cookie := login(t, env.h)
	rem := env.settings.reminders
	until := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)

	rec := do(env.h, http.MethodPost, BasePath+"/snooze", body(`{"folder":"INBOX","uids":[4,4,5],"until":"`+until+`"}`), jsonOrigin, cookie)
	var res snoozeResultDTO
	envelopeData(t, rec, &res)
	if rec.Code != http.StatusOK || len(res.Snoozed) != 2 || len(res.Failed) != 0 || res.Snoozed[0].Until != until ||
		res.Snoozed[0].ReturnFolder != "INBOX" || res.Snoozed[0].Folder != domain.SnoozedFolderName || res.Snoozed[0].UID != 104 {
		t.Fatalf("posponer: %d %s", rec.Code, rec.Body)
	}
	if len(rem.created) != 2 || rem.created[0].Kind != domain.ReminderSnooze || rem.created[0].Username != testUser {
		t.Fatalf("filas: %+v", rem.created)
	}
	if strings.Join(env.mb.created, ",") != domain.SnoozedFolderName {
		t.Fatalf("la carpeta de pospuestos se crea al primer uso: %v", env.mb.created)
	}
	for name, b := range map[string]string{
		"hora ilegible": `{"folder":"INBOX","uids":[4],"until":"manana"}`,
		"hora pasada":   `{"folder":"INBOX","uids":[4],"until":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `"}`,
		"hora lejana":   `{"folder":"INBOX","uids":[4],"until":"` + time.Now().AddDate(0, 0, 40).UTC().Format(time.RFC3339) + `"}`,
		"sin mensajes":  `{"folder":"INBOX","uids":[],"until":"` + until + `"}`,
	} {
		if rec := do(env.h, http.MethodPost, BasePath+"/snooze", body(b), jsonOrigin, cookie); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	rec = do(env.h, http.MethodGet, BasePath+"/snooze", nil, nil, cookie)
	var rows []snoozedDTO
	envelopeData(t, rec, &rows)
	if rec.Code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("listado: %d %s", rec.Code, rec.Body)
	}
	later := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	rec = do(env.h, http.MethodPatch, BasePath+"/snooze/"+rows[0].ID, body(`{"until":"`+later+`"}`), jsonOrigin, cookie)
	var moved snoozedDTO
	envelopeData(t, rec, &moved)
	if rec.Code != http.StatusOK || moved.Until != later {
		t.Fatalf("cambiar la hora: %d %s", rec.Code, rec.Body)
	}
	if rec := do(env.h, http.MethodDelete, BasePath+"/snooze/"+rows[1].ID, nil, jsonOrigin, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("devolver ya: %d %s", rec.Code, rec.Body)
	}
	rec = do(env.h, http.MethodDelete, BasePath+"/snooze/00000000-0000-4000-8000-000000000999", nil, jsonOrigin, cookie)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "REMINDER_NOT_FOUND" {
		t.Fatalf("inexistente: %d %s", rec.Code, rec.Body)
	}
	if rec := do(env.h, http.MethodPatch, BasePath+"/snooze/..%2Fx", body(`{"until":"`+later+`"}`), jsonOrigin, cookie); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("id invalido: %d %s", rec.Code, rec.Body)
	}
}

func TestSeguimientoAlEnviarPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	rem := env.settings.reminders

	form, ct := multipartForm(t, map[string]string{"to": "luis@x.pe", "cc": "eva@x.pe", "subject": "Presupuesto", "text": "x", "follow_up_days": "3"})
	rec := do(env.h, http.MethodPost, BasePath+"/send", form, sendHeaders(ct), cookie)
	var res sendDTO
	envelopeData(t, rec, &res)
	if rec.Code != http.StatusAccepted || res.FollowUp == nil || res.FollowUpError != "" {
		t.Fatalf("envio con seguimiento: %d %s", rec.Code, rec.Body)
	}
	if len(rem.created) != 1 || rem.created[0].Kind != domain.ReminderFollowUp || rem.created[0].MessageID != res.MessageID ||
		strings.Join(rem.created[0].Addresses, ",") != "luis@x.pe,eva@x.pe" || rem.created[0].Folder != "Sent" {
		t.Fatalf("fila: %+v", rem.created)
	}
	due, _ := time.Parse(time.RFC3339, res.FollowUp.DueAt)
	if d := time.Until(due); d < 71*time.Hour || d > 73*time.Hour {
		t.Fatalf("vence a los tres dias: %s", res.FollowUp.DueAt)
	}

	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	form, ct = multipartForm(t, map[string]string{"to": "luis@x.pe", "subject": "Luego", "text": "x", "send_at": at.Format(time.RFC3339), "follow_up_days": "1"})
	rec = do(env.h, http.MethodPost, BasePath+"/send", form, sendHeaders(ct), cookie)
	var sched scheduleDTO
	envelopeData(t, rec, &sched)
	if rec.Code != http.StatusAccepted || sched.FollowUp == nil || sched.FollowUp.DueAt != at.Add(24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("programado con seguimiento: %d %s", rec.Code, rec.Body)
	}

	for name, days := range map[string]string{"cero": "0", "texto": "x", "lejos": "31"} {
		form, ct := multipartForm(t, map[string]string{"to": "luis@x.pe", "text": "x", "follow_up_days": days})
		rec := do(env.h, http.MethodPost, BasePath+"/send", form, sendHeaders(ct), cookie)
		if _, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || details["field"] != "follow_up_days" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	form, ct = multipartForm(t, map[string]string{"text": "x", "follow_up_days": "3"})
	rec = do(env.h, http.MethodPost, BasePath+"/drafts", form, map[string]string{"Origin": allowedOrigin, "Content-Type": ct}, cookie)
	if _, details := errorDetails(t, rec); rec.Code != http.StatusUnprocessableEntity || details["field"] != "follow_up_days" {
		t.Errorf("un borrador no pide seguimiento: %d %s", rec.Code, rec.Body)
	}

	// Si el directorio no lo registra, el mensaje sale igual y la respuesta lo dice.
	rem.err = domain.ErrReminderLimit
	form, ct = multipartForm(t, map[string]string{"to": "luis@x.pe", "text": "x", "follow_up_days": "3"})
	rec = do(env.h, http.MethodPost, BasePath+"/send", form, sendHeaders(ct), cookie)
	res = sendDTO{}
	envelopeData(t, rec, &res)
	if rec.Code != http.StatusAccepted || res.FollowUp != nil || res.FollowUpError != "REMINDER_LIMIT" {
		t.Fatalf("seguimiento fallido: %d %s", rec.Code, rec.Body)
	}
	rem.err = nil

	rec = do(env.h, http.MethodGet, BasePath+"/follow-ups", nil, nil, cookie)
	var rows []followUpDTO
	envelopeData(t, rec, &rows)
	if rec.Code != http.StatusOK || len(rows) != 2 || rows[0].Subject != "Presupuesto" {
		t.Fatalf("listado: %d %s", rec.Code, rec.Body)
	}
	if rec := do(env.h, http.MethodDelete, BasePath+"/follow-ups/"+rows[0].ID, nil, jsonOrigin, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("cancelar: %d %s", rec.Code, rec.Body)
	}
}

func TestRespuestasRapidasPorHTTP(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	rec := do(env.h, http.MethodPost, BasePath+"/quick-replies", body(`{"name":"Gracias","html":"<p>Hola {nombre}</p>"}`), jsonOrigin, cookie)
	var created quickReplyDTO
	envelopeData(t, rec, &created)
	if rec.Code != http.StatusCreated || created.ID == "" || created.Name != "Gracias" {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body)
	}
	if rec := do(env.h, http.MethodPost, BasePath+"/quick-replies", body(`{"name":"gracias","html":"x"}`), jsonOrigin, cookie); rec.Code != http.StatusConflict || errorCode(t, rec) != "QUICK_REPLY_EXISTS" {
		t.Fatalf("repetida: %d %s", rec.Code, rec.Body)
	}
	if rec := do(env.h, http.MethodPost, BasePath+"/quick-replies", body(`{"name":"","html":"x"}`), jsonOrigin, cookie); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("sin nombre: %d %s", rec.Code, rec.Body)
	}
	rec = do(env.h, http.MethodGet, BasePath+"/quick-replies", nil, nil, cookie)
	var list quickRepliesDTO
	envelopeData(t, rec, &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 || list.Limits.MaxItems != 100 || list.Limits.MaxNameChars != 80 {
		t.Fatalf("listado: %d %s", rec.Code, rec.Body)
	}
	rec = do(env.h, http.MethodPut, BasePath+"/quick-replies/"+created.ID, body(`{"name":"Agradecer","html":"<p>Gracias</p>"}`), jsonOrigin, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("cambiar: %d %s", rec.Code, rec.Body)
	}
	if rec := do(env.h, http.MethodDelete, BasePath+"/quick-replies/"+created.ID, nil, jsonOrigin, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("borrar: %d %s", rec.Code, rec.Body)
	}
	if rec := do(env.h, http.MethodDelete, BasePath+"/quick-replies/"+created.ID, nil, jsonOrigin, cookie); rec.Code != http.StatusNotFound || errorCode(t, rec) != "QUICK_REPLY_NOT_FOUND" {
		t.Fatalf("borrar dos veces: %d %s", rec.Code, rec.Body)
	}
}

func TestRutasDeRecordatoriosExigenSesionYOrigen(t *testing.T) {
	env := newTestEnv(t, nopSender{})
	cookie := login(t, env.h)
	id := "00000000-0000-4000-8000-000000000001"
	for _, w := range []struct{ method, path, body string }{
		{http.MethodPost, "/snooze", `{"folder":"INBOX","uids":[1],"until":"2030-01-01T00:00:00Z"}`},
		{http.MethodPatch, "/snooze/" + id, `{"until":"2030-01-01T00:00:00Z"}`},
		{http.MethodDelete, "/snooze/" + id, ""},
		{http.MethodDelete, "/follow-ups/" + id, ""},
		{http.MethodPost, "/quick-replies", `{"name":"a","html":"b"}`},
		{http.MethodPut, "/quick-replies/" + id, `{"name":"a","html":"b"}`},
		{http.MethodDelete, "/quick-replies/" + id, ""},
	} {
		if rec := do(env.h, w.method, BasePath+w.path, body(w.body), map[string]string{"Content-Type": "application/json"}, cookie); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s sin Origin: %d", w.method, w.path, rec.Code)
		}
		if rec := do(env.h, w.method, BasePath+w.path, body(w.body), jsonOrigin, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s sin sesion: %d", w.method, w.path, rec.Code)
		}
	}
	for _, path := range []string{"/snooze", "/follow-ups", "/quick-replies"} {
		if rec := do(env.h, http.MethodGet, BasePath+path, nil, nil, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s sin sesion: %d", path, rec.Code)
		}
	}
	if len(env.settings.reminders.created) != 0 || len(env.settings.reminders.quickIn) != 0 {
		t.Fatal("nada llega al directorio sin sesion u origen")
	}
}
