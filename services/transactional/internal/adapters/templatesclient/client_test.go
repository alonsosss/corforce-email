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
		_, _ = w.Write([]byte(`{"data":{"subject":"Hola","html":"<p>Hola</p>","text":"Hola","version":4,"kind":"marketing"}}`))
	}))
	defer srv.Close()

	version := 4
	out, err := New(srv.URL, "tok").Render(context.Background(), tenant, ports.RenderRequest{
		TemplateID: tpl, Version: &version, Variables: map[string]any{"name": "Ana"},
		Reserved: ports.ReservedVariables{UnsubscribeURL: "https://u", RecipientEmail: "ana@example.com"},
	})
	if err != nil || out.Subject != "Hola" || out.HTML != "<p>Hola</p>" || out.Text != "Hola" || out.Version != 4 ||
		out.Kind != domain.TemplateKindMarketing {
		t.Fatalf("Render: %+v %v", out, err)
	}
	reserved, _ := body["reserved"].(map[string]any)
	if body["version"] != float64(4) || reserved["recipient_email"] != "ana@example.com" || reserved["unsubscribe_url"] != "https://u" {
		t.Fatalf("cuerpo enviado: %v", body)
	}
	if _, ok := reserved["view_in_browser_url"]; !ok {
		t.Fatal("las cuatro variables reservadas viajan siempre")
	}
	if _, ok := body["test"]; ok {
		t.Fatal("un render real no pide el modo de prueba")
	}
}

func TestRenderForATestSendAsksForTheTestMode(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"data":{"subject":"Hola","html":"<p>Hola</p>","text":"Hola","version":2,"kind":"transactional"}}`))
	}))
	defer srv.Close()
	version := 2
	if _, err := New(srv.URL, "tok").Render(context.Background(), uuid.New(), ports.RenderRequest{
		TemplateID: uuid.New(), Version: &version, Test: true,
	}); err != nil {
		t.Fatal(err)
	}
	if body["test"] != true || body["version"] != float64(2) {
		t.Fatalf("cuerpo del render de prueba: %v", body)
	}
}

// Una version de templates anterior al campo kind responde sin el: el cliente lo deja
// vacio y cada via decide (marketing falla cerrado, el transaccional sigue).
func TestRenderWithoutKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"subject":"Hola","html":"<p>Hola</p>","text":"Hola","version":1}}`))
	}))
	defer srv.Close()
	out, err := New(srv.URL, "tok").Render(context.Background(), uuid.New(), ports.RenderRequest{TemplateID: uuid.New()})
	if err != nil || out.Kind != "" || out.Subject != "Hola" {
		t.Fatalf("Render sin kind: %+v %v", out, err)
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

func TestResolveTemplateKeyContract(t *testing.T) {
	tenant, tpl := uuid.New(), uuid.New()
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/internal/templates/by-key/pedido.confirmado" || r.Method != http.MethodGet {
			t.Errorf("ruta %s %s", r.Method, r.URL.EscapedPath())
		}
		if r.Header.Get("X-Gateway-Token") != "tok" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			t.Errorf("cabeceras internas ausentes")
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"data":{"id":"` + tpl.String() + `","kind":"transactional","status":"active"}}`))
		} else {
			_, _ = w.Write([]byte(`{"error":{"message":"clave de plantilla no válida"}}`))
		}
	}))
	defer srv.Close()
	client := New(srv.URL, "tok")

	id, err := client.ResolveTemplateKey(context.Background(), tenant, "pedido.confirmado")
	if err != nil || id != tpl {
		t.Fatalf("ResolveTemplateKey: %s %v", id, err)
	}
	status = http.StatusNotFound
	if _, err := client.ResolveTemplateKey(context.Background(), tenant, "pedido.confirmado"); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("404: %v", err)
	}
	status = http.StatusUnprocessableEntity
	var ve *domain.ValidationError
	if _, err := client.ResolveTemplateKey(context.Background(), tenant, "pedido.confirmado"); !errors.As(err, &ve) {
		t.Errorf("422: %v", err)
	}
	status = http.StatusBadGateway
	if _, err := client.ResolveTemplateKey(context.Background(), tenant, "pedido.confirmado"); !errors.Is(err, domain.ErrTemplatesUnavailable) {
		t.Errorf("502: %v", err)
	}
}
