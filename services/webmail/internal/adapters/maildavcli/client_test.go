package maildavcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

var mailbox = domain.MailboxRef{TenantID: "11111111-1111-4111-8111-111111111111", MailboxID: "22222222-2222-4222-8222-222222222222"}

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/", "token-interno", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// checkIdentity exige las cabeceras que solo pone el webmail y ninguna de usuario de la plataforma.
func checkIdentity(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("X-Gateway-Token") != "token-interno" || r.Header.Get("X-Mailbox-Tenant-ID") != mailbox.TenantID ||
		r.Header.Get("X-Mailbox-ID") != mailbox.MailboxID || r.Header.Get("X-User-ID") != "" {
		t.Errorf("cabeceras: %v", r.Header)
	}
}

func TestContactosContraMailDav(t *testing.T) {
	var putBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkIdentity(t, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /internal/mail-dav/contacts":
			if r.URL.Query().Get("q") != "luis" || r.URL.Query().Get("page") != "2" {
				t.Errorf("query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"c1","etag":"\"v1\"","name":"Luis","emails":[{"value":"l@x.pe","type":"work"}],"phones":[],` +
				`"updated_at":"2026-09-24T10:00:00Z"}],"meta":{"page":2,"per_page":25,"total":26,"total_pages":2}}`))
		case "PUT /internal/mail-dav/contacts/c1":
			if r.Header.Get("If-Match") != `"v1"` {
				t.Errorf("If-Match: %q", r.Header.Get("If-Match"))
			}
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.Header().Set("ETag", `"v9"`)
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(`{"error":{"code":"PRECONDITION_FAILED","message":"cambio"}}`))
		case "GET /internal/mail-dav/contacts/c2":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no existe"}}`))
		case "POST /internal/mail-dav/contacts":
			w.WriteHeader(http.StatusInsufficientStorage)
			_, _ = w.Write([]byte(`{"error":{"code":"LIMIT_EXCEEDED","message":"cuota","details":{"limit":"10000"}}}`))
		case "DELETE /internal/mail-dav/contacts/c1":
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"RATE_LIMITED","message":"espera"}}`))
		default:
			t.Errorf("ruta inesperada %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	page, err := c.ListContacts(ctx, mailbox, domain.ContactQuery{Search: "luis", Page: 2})
	if err != nil || page.Total != 26 || page.Page != 2 || page.PerPage != 25 || page.Items[0].ETag != `"v1"` || page.Items[0].UpdatedAt == nil {
		t.Fatalf("listado: %+v %v", page, err)
	}

	_, err = c.UpdateContact(ctx, mailbox, "c1", domain.ContactInput{Name: "Luis"}, `"v1"`)
	var rejection *domain.ServiceRejection
	if !errors.As(err, &rejection) || rejection.Kind != domain.RejectPrecondition || rejection.ETag != `"v9"` || !errors.Is(err, domain.ErrPreconditionFailed) {
		t.Fatalf("412: %#v", err)
	}
	if putBody["name"] != "Luis" || putBody["emails"] == nil || len(putBody) != 9 {
		t.Fatalf("cuerpo del contacto, con las listas vacias como listas: %v", putBody)
	}
	if _, err := c.Contact(ctx, mailbox, "c2"); !errors.As(err, &rejection) || rejection.Code != "NOT_FOUND" || !errors.Is(err, domain.ErrResourceNotFound) {
		t.Fatalf("404: %v", err)
	}
	if _, err := c.CreateContact(ctx, mailbox, domain.ContactInput{}); !errors.As(err, &rejection) || rejection.Kind != domain.RejectQuota || rejection.Details["limit"] != "10000" {
		t.Fatalf("507: %v", err)
	}
	if err := c.DeleteContact(ctx, mailbox, "c1"); !errors.As(err, &rejection) || rejection.Kind != domain.RejectRateLimited || rejection.RetryAfter != "2" {
		t.Fatalf("429: %v", err)
	}
}

func TestImportarYExportarContraMailDav(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkIdentity(t, r)
		switch r.URL.Path {
		case "/internal/mail-dav/contacts/export":
			w.Header().Set("Content-Type", "text/vcard")
			_, _ = w.Write([]byte("BEGIN:VCARD\r\nEND:VCARD\r\n"))
		case "/internal/mail-dav/contacts/import":
			mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "multipart/form-data" {
				t.Errorf("tipo: %q", r.Header.Get("Content-Type"))
			}
			part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
			if err != nil || part.FormName() != "file" || part.FileName() != "agenda.vcf" {
				t.Errorf("parte: %v", err)
			} else if data, _ := io.ReadAll(part); string(data) != "BEGIN:VCARD" {
				t.Errorf("fichero: %q", data)
			}
			_, _ = w.Write([]byte(`{"data":{"imported":3,"updated":1,"skipped":[{"index":2,"reason":"sin FN"}]}}`))
		case "/internal/mail-dav/meta":
			if r.Header.Get("X-Mailbox-ID") != "" {
				t.Error("los topes no llevan buzon")
			}
			_, _ = w.Write([]byte(`{"data":{"limits":{"max_import_bytes":1048576,"max_event_window_days":62}}}`))
		}
	})
	ctx := context.Background()
	body, err := c.ExportContacts(ctx, mailbox)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(body)
	_ = body.Close()
	if !strings.HasPrefix(string(raw), "BEGIN:VCARD") {
		t.Fatalf("exportacion: %q", raw)
	}
	res, err := c.ImportContacts(ctx, mailbox, "agenda.vcf", []byte("BEGIN:VCARD"))
	if err != nil || res.Imported != 3 || res.Updated != 1 || res.Skipped[0].Index != 2 {
		t.Fatalf("importacion: %+v %v", res, err)
	}
}

func TestTopesDeMailDav(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/mail-dav/meta" || r.Header.Get("X-Mailbox-ID") != "" {
			t.Errorf("%s %v", r.URL.Path, r.Header)
		}
		_, _ = w.Write([]byte(`{"data":{"limits":{"max_import_bytes":1048576,"max_event_window_days":62}}}`))
	})
	limits, err := c.Limits(context.Background())
	if err != nil || limits["max_event_window_days"] != 62 || limits["max_import_bytes"] != 1<<20 {
		t.Fatalf("%v %v", limits, err)
	}
}

func TestCalendarioContraMailDav(t *testing.T) {
	var created map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkIdentity(t, r)
		switch r.Method + " " + r.URL.Path {
		case "GET /internal/mail-dav/calendar/events":
			if r.URL.Query().Get("start") != "2026-09-01T00:00:00Z" || r.URL.Query().Get("end") != "2026-10-01T05:00:00Z" {
				t.Errorf("ventana: %s", r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"code":"VALIDATION_ERROR","message":"ventana","details":{"field":"end","max_days":"62"}}}`))
		case "POST /internal/mail-dav/calendar/events":
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"e1","etag":"\"e1\"","title":"Revision","start":"2026-09-25T10:00:00Z","end":"2026-09-25T11:00:00Z",` +
				`"all_day":false,"location":"","description":"","recurrence":{"freq":"weekly","interval":1,"count":null,"until":null,"by_day":["MO"]},"reminder_minutes":15}}`))
		case "DELETE /internal/mail-dav/calendar/events/e%2F1", "DELETE /internal/mail-dav/calendar/events/e/1":
			t.Error("un id con barra nunca llega sin escapar")
		}
	})
	ctx := context.Background()
	w, _ := domain.NewEventWindow("2026-09-01T00:00:00Z", "2026-10-01T00:00:00-05:00")
	_, err := c.Occurrences(ctx, mailbox, w)
	var rejection *domain.ServiceRejection
	if !errors.As(err, &rejection) || rejection.Details["max_days"] != "62" || rejection.Kind != domain.RejectValidation {
		t.Fatalf("422: %v", err)
	}
	fifteen := 15
	e, err := c.CreateEvent(ctx, mailbox, domain.EventInput{Title: "Revision", Start: "2026-09-25T10:00:00Z", End: "2026-09-25T11:00:00Z",
		Recurrence: &domain.Recurrence{Freq: "weekly", Interval: 1, ByDay: []string{"MO"}}, ReminderMinutes: &fifteen})
	if err != nil || e.ID != "e1" || e.Recurrence == nil || e.Recurrence.ByDay[0] != "MO" || *e.ReminderMinutes != 15 {
		t.Fatalf("crear: %+v %v", e, err)
	}
	if created["recurrence"] == nil || len(created) != 8 {
		t.Fatalf("cuerpo del evento: %v", created)
	}
}

func TestFalloDeLaLlamadaEsIndisponibilidad(t *testing.T) {
	c, err := New("http://127.0.0.1:1", "t", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Event(context.Background(), mailbox, "e1"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if _, err := New("mail-dav:8058", "t", time.Second); err == nil {
		t.Fatal("URL sin esquema")
	}
}
