package suppressionclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

func TestCheckAndAddContract(t *testing.T) {
	tenant := uuid.New()
	var added map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Gateway-Token") != "tok" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			t.Errorf("cabeceras internas ausentes: %v", r.Header)
		}
		switch r.URL.Path {
		case "/internal/suppression/check":
			var body struct{ Emails []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Emails) != 2 {
				t.Errorf("emails = %v", body.Emails)
			}
			_, _ = w.Write([]byte(`{"data":{"suppressed":[{"email":"eva@example.com","reason":"hard_bounce"}]}}`))
		case "/internal/suppression/add":
			_ = json.NewDecoder(r.Body).Decode(&added)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"entry":{},"added":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "tok")
	got, err := c.Check(context.Background(), tenant, []string{"ana@example.com", "eva@example.com"})
	if err != nil || len(got) != 1 || got[0].Email != "eva@example.com" || got[0].Reason != "hard_bounce" {
		t.Fatalf("Check: %+v %v", got, err)
	}
	err = c.Add(context.Background(), tenant, ports.SuppressionEntry{Email: "eva@example.com", Reason: "hard_bounce", Source: "transactional", MessageID: "m-1"})
	if err != nil {
		t.Fatal(err)
	}
	if added["email"] != "eva@example.com" || added["reason"] != "hard_bounce" || added["source"] != "transactional" || added["message_id"] != "m-1" {
		t.Fatalf("cuerpo de add: %v", added)
	}
	if _, ok := added["detail"]; ok {
		t.Fatal("detail vacio no se envia")
	}
}

func TestAddAlreadyPresentIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"entry":{},"added":false}}`))
	}))
	defer srv.Close()
	if err := New(srv.URL, "tok").Add(context.Background(), uuid.New(), ports.SuppressionEntry{Email: "a@example.com", Reason: "unsubscribe"}); err != nil {
		t.Fatalf("200 added:false es un alta idempotente, no un error: %v", err)
	}
}

func TestErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	if _, err := c.Check(context.Background(), uuid.New(), []string{"a@example.com"}); err == nil {
		t.Fatal("un 4xx de suppression es un error")
	}
	if got, err := c.Check(context.Background(), uuid.New(), nil); err != nil || len(got) != 0 {
		t.Fatal("sin destinatarios no se llama")
	}
	if _, err := New("", "tok").Check(context.Background(), uuid.New(), []string{"a@example.com"}); err == nil {
		t.Fatal("sin SUPPRESSION_URL es un error")
	}
}
