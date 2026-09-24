package maildirectorycli

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const reminderRowJSON = `{"id":"00000000-0000-4000-8000-000000000001","username":"ana@acme.pe","kind":"snooze","message_id":"a@b.pe",` +
	`"folder":"Snoozed","uid_validity":9,"uid":104,"return_folder":"INBOX","subject":"Factura","addresses":["p@x.pe"],` +
	`"due_at":"2030-01-02T03:04:05Z","status":"pending","created_at":"2030-01-01T00:00:00Z","tenant_id":"x","attempts":0}`

func TestRecordatoriosContraElDirectorio(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	c, calls := fakeDirectory(t, map[string]reply{
		"POST " + remindersPath:                        {http.StatusCreated, `{"data":` + reminderRowJSON + `}`},
		"GET " + remindersPath:                         {http.StatusOK, `{"data":[` + reminderRowJSON + `]}`},
		"PATCH " + remindersPath + "/" + id:            {http.StatusOK, `{"data":` + reminderRowJSON + `}`},
		"DELETE " + remindersPath + "/" + id:           {http.StatusNoContent, ``},
		"POST " + remindersPath + "/claim":             {http.StatusOK, `{"data":[` + reminderRowJSON + `]}`},
		"POST " + remindersPath + "/" + id + "/finish": {http.StatusOK, `{"data":` + reminderRowJSON + `}`},
	})
	ctx := context.Background()
	due := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("Lima", -5*3600))
	r, err := c.CreateReminder(ctx, domain.NewReminder{
		Username: "ana@acme.pe", Kind: domain.ReminderSnooze, MessageID: "a@b.pe", Folder: "Snoozed", UIDValidity: 9, UID: 104,
		ReturnFolder: "INBOX", DueAt: due, Subject: "Factura",
	})
	if err != nil || r.ID != id || r.UID != 104 || r.ReturnFolder != "INBOX" || r.Addresses[0] != "p@x.pe" {
		t.Fatalf("crear: %+v %v", r, err)
	}
	body := (*calls)[0].body
	if body["kind"] != "snooze" || body["due_at"] != "2030-01-02T08:04:05Z" || body["return_folder"] != "INBOX" {
		t.Fatalf("cuerpo: %+v", body)
	}
	if addresses, ok := body["addresses"].([]any); !ok || len(addresses) != 0 {
		t.Fatalf("sin direcciones viaja una lista vacia: %+v", body["addresses"])
	}
	list, err := c.ListReminders(ctx, "ana@acme.pe", domain.ReminderSnooze)
	if err != nil || len(list) != 1 || (*calls)[1].query != "kind=snooze&username=ana%40acme.pe" {
		t.Fatalf("listar: %+v %v %s", list, err, (*calls)[1].query)
	}
	if _, err := c.RescheduleReminder(ctx, "ana@acme.pe", id, due); err != nil || (*calls)[2].body["due_at"] != "2030-01-02T08:04:05Z" {
		t.Fatalf("reprogramar: %v %+v", err, (*calls)[2].body)
	}
	if err := c.CancelReminder(ctx, "ana@acme.pe", id); err != nil {
		t.Fatal(err)
	}
	claims, err := c.ClaimReminders(ctx, 4, 150*time.Second)
	if err != nil || len(claims) != 1 || claims[0].Username != "ana@acme.pe" || claims[0].Kind != domain.ReminderSnooze {
		t.Fatalf("reclamar: %+v %v", claims, err)
	}
	if b := (*calls)[4].body; b["limit"] != float64(4) || b["lease_seconds"] != float64(150) {
		t.Fatalf("cuerpo de la reclamacion: %+v", b)
	}
	if err := c.FinishReminder(ctx, id, domain.ReminderOutcome{Status: domain.ReminderDone, Result: domain.ReminderReturned, Retry: true}); err != nil {
		t.Fatal(err)
	}
	if b := (*calls)[5].body; b["status"] != "done" || b["result"] != "returned" || b["retry"] != false {
		t.Fatalf("cierre: %+v", b)
	}
}

func TestRecordatoriosErroresDelDirectorio(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusConflict, `{"error":{"code":"REMINDER_NOT_PENDING","message":"x"}}`, domain.ErrReminderNotPending},
		{http.StatusConflict, `{"error":{"code":"REMINDER_LIMIT","message":"x"}}`, domain.ErrReminderLimit},
		{http.StatusConflict, `{"error":{"code":"CONFLICT","message":"x"}}`, domain.ErrReminderExists},
		{http.StatusNotFound, `{"error":{"code":"NOT_FOUND","message":"x"}}`, domain.ErrReminderNotFound},
		{http.StatusInternalServerError, `{"error":{"code":"INTERNAL","message":"x"}}`, domain.ErrUnavailable},
	}
	for _, tc := range cases {
		c, _ := fakeDirectory(t, map[string]reply{"PATCH " + remindersPath + "/" + id: {tc.status, tc.body}})
		if _, err := c.RescheduleReminder(context.Background(), "ana@acme.pe", id, time.Now()); !errors.Is(err, tc.want) {
			t.Errorf("%d %s: %v", tc.status, tc.body, err)
		}
	}
	c, _ := fakeDirectory(t, map[string]reply{"POST " + remindersPath: {http.StatusUnprocessableEntity,
		`{"error":{"code":"VALIDATION_ERROR","message":"x","details":{"field":"return_folder"}}}`}})
	var verr *domain.ValidationError
	if _, err := c.CreateReminder(context.Background(), domain.NewReminder{}); !errors.As(err, &verr) || verr.Field != "return_folder" {
		t.Fatalf("validacion: %v", err)
	}
	c, _ = fakeDirectory(t, map[string]reply{"POST " + remindersPath: {http.StatusCreated, `{"data":{"id":"no-uuid"}}`}})
	if _, err := c.CreateReminder(context.Background(), domain.NewReminder{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("id invalido: %v", err)
	}
}

func TestRespuestasRapidasContraElDirectorio(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000002"
	item := `{"id":"` + id + `","name":"Gracias","html":"<p>Hola {nombre}</p>","text":"Hola {nombre}","created_at":"2030-01-01T00:00:00Z","updated_at":"2030-01-02T00:00:00Z"}`
	c, calls := fakeDirectory(t, map[string]reply{
		"GET " + quickRepliesPath:               {http.StatusOK, `{"data":{"items":[` + item + `],"limits":{"max_items":100,"max_name_chars":80,"max_html_bytes":16384,"max_text_bytes":16384}}}`},
		"POST " + quickRepliesPath:              {http.StatusCreated, `{"data":` + item + `}`},
		"PUT " + quickRepliesPath + "/" + id:    {http.StatusOK, `{"data":` + item + `}`},
		"DELETE " + quickRepliesPath + "/" + id: {http.StatusNoContent, ``},
	})
	ctx := context.Background()
	list, err := c.QuickReplies(ctx, "ana@acme.pe")
	if err != nil || len(list.Items) != 1 || list.Items[0].Text != "Hola {nombre}" || list.Limits.MaxNameChars != 80 || list.Limits.MaxItems != 100 {
		t.Fatalf("listar: %+v %v", list, err)
	}
	q, err := c.CreateQuickReply(ctx, "ana@acme.pe", domain.QuickReply{Name: "Gracias", HTML: "<p>x</p>", Text: "x"})
	if err != nil || q.ID != id || (*calls)[1].body["text"] != "x" || (*calls)[1].query != "username=ana%40acme.pe" {
		t.Fatalf("crear: %+v %v %+v", q, err, (*calls)[1])
	}
	if _, err := c.UpdateQuickReply(ctx, "ana@acme.pe", domain.QuickReply{ID: id, Name: "Otra", HTML: "y"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteQuickReply(ctx, "ana@acme.pe", id); err != nil {
		t.Fatal(err)
	}
	for code, want := range map[string]error{"QUICK_REPLY_LIMIT": domain.ErrQuickReplyLimit, "CONFLICT": domain.ErrQuickReplyExists} {
		c, _ := fakeDirectory(t, map[string]reply{"POST " + quickRepliesPath: {http.StatusConflict, `{"error":{"code":"` + code + `","message":"x"}}`}})
		if _, err := c.CreateQuickReply(ctx, "ana@acme.pe", domain.QuickReply{}); !errors.Is(err, want) {
			t.Errorf("%s: %v", code, err)
		}
	}
	c, _ = fakeDirectory(t, map[string]reply{"DELETE " + quickRepliesPath + "/" + id: {http.StatusNotFound, `{"error":{"code":"NOT_FOUND","message":"x"}}`}})
	if err := c.DeleteQuickReply(ctx, "ana@acme.pe", id); !errors.Is(err, domain.ErrQuickReplyNotFound) {
		t.Fatalf("inexistente: %v", err)
	}
}
