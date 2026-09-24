package maildirectorycli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type recorded struct {
	method, path, query string
	body                map[string]any
	header              http.Header
}

// fakeDirectory responde a cada ruta con el estado y el cuerpo que diga la prueba y anota la llamada.
func fakeDirectory(t *testing.T, replies map[string]struct {
	status int
	body   string
}) (*Client, *[]recorded) {
	t.Helper()
	var calls []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone()}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &rec.body); err != nil {
				t.Errorf("cuerpo no JSON: %s", raw)
			}
		}
		calls = append(calls, rec)
		reply, ok := replies[r.Method+" "+r.URL.Path]
		if !ok {
			t.Errorf("ruta inesperada %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "token-interno")
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

type reply = struct {
	status int
	body   string
}

func TestFirmaReglasYContrasenaContraElDirectorio(t *testing.T) {
	c, calls := fakeDirectory(t, map[string]reply{
		"GET " + signaturePath: {200, `{"data":{"enabled":true,"html":"<p>A</p>","text":"A","on_replies":false,"updated_at":"2026-09-24T10:00:00Z","limits":{"max_html_bytes":8192,"max_text_bytes":4096}}}`},
		"PUT " + signaturePath: {200, `{"data":{"enabled":true,"html":"<p>B</p>","text":"B","on_replies":true,"limits":{"max_html_bytes":8192,"max_text_bytes":4096}}}`},
		"PUT " + filtersPath:   {422, `{"error":{"code":"VALIDATION_ERROR","message":"demasiado largo","details":{"field":"rules[3].conditions[0].value"}}}`},
		"GET " + filtersPath:   {200, `{"data":{"rules":[{"id":"r1","name":"F","enabled":true,"match":"all","conditions":[{"field":"from","op":"is","value":"a@b.pe"}],"actions":[{"type":"forward","address":"c@d.pe","keep_copy":true}],"stop":true}],"forwarding":{"enabled":false,"addresses":null,"keep_copy":false},"limits":{"max_rules":50,"max_value_length":256}}}`},
		"PUT " + passwordPath:  {204, ``},
	})
	ctx := context.Background()
	sig, err := c.Signature(ctx, "ana@empresa.pe")
	if err != nil || !sig.Enabled || sig.Limits.MaxHTMLBytes != 8192 || sig.Limits.MaxTextBytes != 4096 || sig.UpdatedAt == nil {
		t.Fatalf("firma: %+v %v", sig, err)
	}
	if _, err := c.SetSignature(ctx, "ana@empresa.pe", domain.SignatureInput{Enabled: true, HTML: "<p>B</p>", Text: "B", OnReplies: true}); err != nil {
		t.Fatal(err)
	}
	put := (*calls)[1]
	if put.query != "username=ana%40empresa.pe" || put.header.Get("X-Gateway-Token") != "token-interno" || put.header.Get("X-User-ID") != "" {
		t.Fatalf("llamada interna: %+v", put)
	}
	if len(put.body) != 4 || put.body["html"] != "<p>B</p>" || put.body["text"] != "B" || put.body["on_replies"] != true {
		t.Fatalf("cuerpo de la firma, sin campos fuera del contrato: %v", put.body)
	}

	_, err = c.SetFilters(ctx, "ana@empresa.pe", domain.MailFiltersInput{})
	var verr *domain.ValidationError
	if !errors.As(err, &verr) || verr.Field != "rules[3].conditions[0].value" || verr.Reason != "demasiado largo" {
		t.Fatalf("422 con details.field: %v", err)
	}
	if body := (*calls)[2].body; body["rules"] == nil || body["forwarding"] == nil || len(body) != 2 {
		t.Fatalf("reglas vacias viajan como listas: %v", body)
	}
	f, err := c.Filters(ctx, "ana@empresa.pe")
	if err != nil || len(f.Rules) != 1 || f.Rules[0].Actions[0].Address != "c@d.pe" || !*f.Rules[0].Actions[0].KeepCopy ||
		f.Forwarding.Addresses == nil || f.Limits["max_value_length"] != 256 {
		t.Fatalf("reglas: %+v %v", f, err)
	}
	if err := c.SetPassword(ctx, "ana@empresa.pe", "nueva-segura"); err != nil {
		t.Fatal(err)
	}
	if body := (*calls)[4].body; len(body) != 1 || body["password"] != "nueva-segura" {
		t.Fatalf("contrasena: %v", body)
	}
}

const rowJSON = `{"id":"00000000-0000-4000-8000-000000000001","username":"ana@empresa.pe","message_id":"m1@empresa.pe","folder":"Scheduled",` +
	`"uid_validity":77,"uid":5,"send_at":"2026-09-25T10:00:00Z","subject":"Informe","recipients":["luis@x.pe"],"status":"pending",` +
	`"attempts":0,"last_error":null,"created_at":"2026-09-24T10:00:00Z","updated_at":"2026-09-24T10:00:00Z"}`

func TestEnviosProgramadosContraElDirectorio(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	c, calls := fakeDirectory(t, map[string]reply{
		"POST " + scheduledPath:                        {201, `{"data":` + rowJSON + `}`},
		"GET " + scheduledPath:                         {200, `{"data":[` + rowJSON + `]}`},
		"PATCH " + scheduledPath + "/" + id:            {409, `{"error":{"code":"SCHEDULED_SEND_NOT_PENDING","message":"en curso"}}`},
		"DELETE " + scheduledPath + "/" + id:           {204, ``},
		"POST " + scheduledPath + "/claim":             {200, `{"data":[` + rowJSON + `]}`},
		"POST " + scheduledPath + "/" + id + "/finish": {409, `{"error":{"code":"SCHEDULED_SEND_NOT_CLAIMED","message":"no reclamada"}}`},
	})
	ctx := context.Background()
	at := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	got, err := c.CreateScheduled(ctx, domain.NewScheduledSend{
		Username: "ana@empresa.pe", MessageID: "m1@empresa.pe", Folder: "Scheduled", UIDValidity: 77, UID: 5, SendAt: at,
		Subject: "Informe", Recipients: []string{"luis@x.pe"},
	})
	if err != nil || got != id {
		t.Fatalf("crear: %q %v", got, err)
	}
	create := (*calls)[0].body
	for _, key := range []string{"username", "message_id", "folder", "uid_validity", "uid", "send_at", "subject", "recipients"} {
		if _, ok := create[key]; !ok {
			t.Errorf("falta %s en %v", key, create)
		}
	}
	if len(create) != 8 || create["send_at"] != "2026-09-25T10:00:00Z" {
		t.Fatalf("cuerpo de la fila: %v", create)
	}

	rows, err := c.ListScheduled(ctx, "ana@empresa.pe")
	if err != nil || len(rows) != 1 || rows[0].UID != 5 || rows[0].UIDValidity != 77 || rows[0].Status != domain.ScheduledPending || rows[0].CreatedAt.IsZero() {
		t.Fatalf("listado: %+v %v", rows, err)
	}
	if _, err := c.RescheduleScheduled(ctx, "ana@empresa.pe", id, at); !errors.Is(err, domain.ErrScheduledNotPending) {
		t.Fatalf("409 por codigo: %v", err)
	}
	if err := c.CancelScheduled(ctx, "ana@empresa.pe", id); err != nil {
		t.Fatal(err)
	}
	claims, err := c.ClaimScheduled(ctx, 10, 390*time.Second)
	if err != nil || len(claims) != 1 || claims[0].Username != "ana@empresa.pe" || claims[0].MessageID != "m1@empresa.pe" {
		t.Fatalf("reclamar: %+v %v", claims, err)
	}
	if body := (*calls)[4].body; body["limit"] != float64(10) || body["lease_seconds"] != float64(390) || len(body) != 2 {
		t.Fatalf("cuerpo de claim: %v", body)
	}
	err = c.FinishScheduled(ctx, id, domain.ScheduledOutcome{Status: domain.ScheduledFailed, Error: "postfix caido", Retry: true})
	if !errors.Is(err, domain.ErrScheduledNotClaimed) {
		t.Fatalf("finish no reclamada: %v", err)
	}
	if body := (*calls)[5].body; body["status"] != "failed" || body["error"] != "postfix caido" || body["retry"] != true || len(body) != 3 {
		t.Fatalf("cuerpo de finish: %v", body)
	}
	for _, call := range *calls {
		if call.header.Get("X-Gateway-Token") != "token-interno" || call.header.Get("X-User-ID") != "" {
			t.Fatalf("cabeceras internas: %v", call.header)
		}
	}
	if strings.Contains((*calls)[0].query, "username") {
		t.Fatal("el alta lleva el buzon en el cuerpo")
	}
}

func TestEnviosProgramadosErroresDelDirectorio(t *testing.T) {
	c, _ := fakeDirectory(t, map[string]reply{
		"POST " + scheduledPath: {409, `{"error":{"code":"SCHEDULED_SEND_LIMIT","message":"200 pendientes"}}`},
		"GET " + scheduledPath:  {503, `{"error":{"code":"SERVICE_UNAVAILABLE","message":"x"}}`},
	})
	ctx := context.Background()
	if _, err := c.CreateScheduled(ctx, domain.NewScheduledSend{}); !errors.Is(err, domain.ErrScheduledLimit) {
		t.Fatalf("tope de pendientes: %v", err)
	}
	if _, err := c.ListScheduled(ctx, "ana@empresa.pe"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("503: %v", err)
	}
}
