package templatesclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

func TestRenderContract(t *testing.T) {
	tenant, tpl := uuid.New(), uuid.New()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/templates/"+tpl.String()+"/render" || r.Method != http.MethodPost {
			t.Errorf("ruta %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Gateway-Token") != "tok" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			t.Errorf("cabeceras internas ausentes")
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"data":{"subject":"Hola","html":"<p>Hola</p>","text":"Hola","version":4}}`))
	}))
	defer srv.Close()

	version := 4
	out, err := New(srv.URL, "tok").Render(context.Background(), tenant, ports.RenderRequest{
		TemplateID: tpl, Version: &version, Variables: map[string]any{"name": "Ana"},
		Reserved: ports.ReservedVariables{UnsubscribeURL: "https://u", RecipientEmail: "ana@example.com"},
	})
	if err != nil || out.Subject != "Hola" || out.HTML != "<p>Hola</p>" || out.Text != "Hola" || out.Version != 4 {
		t.Fatalf("Render: %+v %v", out, err)
	}
	reserved, _ := body["reserved"].(map[string]any)
	if body["version"] != float64(4) || reserved["recipient_email"] != "ana@example.com" || reserved["unsubscribe_url"] != "https://u" {
		t.Fatalf("cuerpo enviado: %v", body)
	}
	if _, ok := reserved["view_in_browser_url"]; !ok {
		t.Fatal("las cuatro variables reservadas viajan siempre")
	}
}

func TestRenderErrors(t *testing.T) {
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"VALIDATION_ERROR","message":"falta la variable name"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	req := ports.RenderRequest{TemplateID: uuid.New()}

	if _, err := c.Render(context.Background(), uuid.New(), req); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("404: %v", err)
	}
	status = http.StatusUnprocessableEntity
	if _, err := c.Render(context.Background(), uuid.New(), req); !domain.IsValidation(err) || err.Error() != "template: falta la variable name" {
		t.Errorf("422: %v", err)
	}
	status = http.StatusInternalServerError
	if _, err := c.Render(context.Background(), uuid.New(), req); !errors.Is(err, domain.ErrTemplatesUnavailable) {
		t.Errorf("500: %v", err)
	}
}
