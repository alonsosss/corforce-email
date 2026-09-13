package contactsclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

func TestAudienceContract(t *testing.T) {
	tenant, list, contact := uuid.New(), uuid.New(), uuid.New()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/contacts/audience" || r.Header.Get("X-Tenant-ID") != tenant.String() || r.Header.Get("X-Gateway-Token") != "tok" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["cursor"]; !ok {
			_, _ = w.Write([]byte(`{"data":{"contacts":[{"id":"` + contact.String() + `","email":"ana@example.com","first_name":"Ana","last_name":"Diaz","locale":"es","timezone":"America/Lima","attributes":{"puntos":120}}],"next_cursor":"c2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"contacts":[],"next_cursor":null}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	audience := domain.Audience{ListIDs: []uuid.UUID{list}}

	page, err := c.Audience(context.Background(), tenant, ports.AudienceQuery{Audience: audience, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Contacts) != 1 || page.Contacts[0].ID != contact || page.Contacts[0].Timezone != "America/Lima" ||
		string(page.Contacts[0].Attributes["puntos"]) != "120" || page.NextCursor == nil || *page.NextCursor != "c2" {
		t.Fatalf("pagina: %+v", page)
	}
	first := bodies[0]
	if first["limit"] != float64(500) || first["list_ids"].([]any)[0] != list.String() {
		t.Fatalf("cuerpo: %v", first)
	}
	if segs, ok := first["segment_ids"].([]any); !ok || len(segs) != 0 {
		t.Fatalf("listas vacias como []: %v", first["segment_ids"])
	}

	page, err = c.Audience(context.Background(), tenant, ports.AudienceQuery{Audience: audience, Cursor: page.NextCursor, Limit: 500})
	if err != nil || page.NextCursor != nil || len(page.Contacts) != 0 {
		t.Fatalf("fin de la audiencia: %+v %v", page, err)
	}
	if bodies[1]["cursor"] != "c2" {
		t.Fatalf("el cursor viaja: %v", bodies[1])
	}
}

func TestAudienceErrors(t *testing.T) {
	status, body := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	q := ports.AudienceQuery{Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, Limit: 1}

	status, body = 422, `{"error":{"code":"VALIDATION_ERROR","message":"segmento inexistente"}}`
	var rejected *ports.RejectedError
	if _, err := c.Audience(context.Background(), uuid.New(), q); !errors.As(err, &rejected) {
		t.Fatalf("422: %v", err)
	}
	status, body = 200, `{"data":{"contacts":[{"id":"`+uuid.NewString()+`","email":"a@x.com"},{"id":"`+uuid.NewString()+`","email":"b@x.com"}],"next_cursor":null}}`
	if _, err := c.Audience(context.Background(), uuid.New(), q); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("pagina mayor que el limite: %v", err)
	}
	status, body = 500, `{}`
	if _, err := c.Audience(context.Background(), uuid.New(), q); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("500: %v", err)
	}
}
