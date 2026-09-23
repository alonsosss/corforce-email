package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Los rechazos del enlace de ver en el navegador se deciden antes de resolver la empresa:
// sin firma valida o con el enlace caducado no se toca ninguna base.
func TestViewInBrowserRejectsBeforeTouchingTheTenant(t *testing.T) {
	ts := newTestServer(t, "")
	valid := domain.ViewClaims{TenantID: uuid.New(), MessageID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)}
	expired := domain.ViewClaims{TenantID: uuid.New(), MessageID: uuid.New(), ExpiresAt: time.Now().Add(-time.Minute)}
	tampered := strings.Replace(ts.links.ViewInBrowserURL(valid), "sig=", "sig=0", 1)

	cases := map[string]struct {
		target string
		status int
	}{
		"sin parametros":      {"/api/v1/public/transactional/view", nethttp.StatusForbidden},
		"firma alterada":      {tampered, nethttp.StatusForbidden},
		"caducidad no leible": {strings.Replace(ts.links.ViewInBrowserURL(valid), "x=", "x=a", 1), nethttp.StatusForbidden},
		"caducado":            {ts.links.ViewInBrowserURL(expired), nethttp.StatusGone},
	}
	for name, tc := range cases {
		rec := ts.do(nethttp.MethodGet, strings.TrimPrefix(tc.target, "https://app.example.com"), "", "")
		if rec.Code != tc.status {
			t.Fatalf("%s: status %d, se esperaba %d", name, rec.Code, tc.status)
		}
		if strings.Contains(strings.ToLower(rec.Body.String()), "<script") || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: la pagina de error no lleva JavaScript ni se cachea", name)
		}
	}
}

func TestViewCSPForbidsScriptsFormsAndFraming(t *testing.T) {
	for _, directive := range []string{"default-src 'none'", "form-action 'none'", "frame-ancestors 'none'", "base-uri 'none'", "sandbox "} {
		if !strings.Contains(viewCSP, directive) {
			t.Fatalf("la politica del correo servido no lleva %q", directive)
		}
	}
	for _, forbidden := range []string{"script-src", "allow-scripts", "allow-forms", "allow-same-origin", "unsafe-eval"} {
		if strings.Contains(viewCSP, forbidden) {
			t.Fatalf("la politica del correo servido no puede llevar %q", forbidden)
		}
	}
}

func TestTemplateTestSendContract(t *testing.T) {
	ts := newTestServer(t, "")
	h := NewHandler(Deps{UC: app.New(app.Deps{Links: ts.links, Logger: zap.NewNop()}), Perms: allowAll{}, Logger: zap.NewNop()})
	call := func(tenant, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(nethttp.MethodPost, "/internal/transactional/test-send", strings.NewReader(body))
		if tenant != "" {
			req.Header.Set("X-Tenant-ID", tenant)
		}
		rec := httptest.NewRecorder()
		h.TemplateTestSend(rec, req)
		return rec
	}
	template := `"template_id":"` + uuid.New().String() + `","template_version":1`
	tenant := uuid.New().String()
	if rec := call("", `{`+template+`}`); rec.Code != nethttp.StatusUnauthorized {
		t.Fatalf("sin X-Tenant-ID: %d", rec.Code)
	}
	if rec := call(tenant, `{`+template+`,"desconocido":1}`); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("un campo desconocido se rechaza: %d", rec.Code)
	}
	six := `"to":["a@example.com","b@example.com","c@example.com","d@example.com","e@example.com","f@example.com"]`
	if rec := call(tenant, `{`+template+`,"from":{"email":"hola@shop.example.com"},`+six+`}`); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("mas de cinco destinatarios: %d", rec.Code)
	}
	if rec := call(tenant, `{`+template+`,"from":{"email":"no-es-correo"},"to":["a@example.com"]}`); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("remitente invalido: %d", rec.Code)
	}
}
