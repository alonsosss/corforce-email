package http

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

func formsHandler() *Handler {
	return NewHandler(Deps{UC: app.New(app.Deps{}), Perms: &recordingGuard{}, PublicBaseURL: "https://app.plataforma.io"})
}

func sampleForm() *domain.SubscriptionForm {
	redirect := "https://acme.pe/gracias"
	return &domain.SubscriptionForm{
		ID: uuid.New(), TenantID: uuid.New(), Name: "Portada", Status: domain.FormActive,
		Fields:         []domain.FormField{{Key: "email", Label: "Correo", Required: true}},
		Texts:          domain.FormTexts{Title: "Boletin <b>", ConsentText: "Acepto <script>", SuccessMessage: "Gracias"},
		AllowedOrigins: []string{"https://acme.pe"}, RedirectURL: &redirect,
	}
}

func TestParseFormKey(t *testing.T) {
	tenant, form := uuid.New(), uuid.New()
	tid, fid, ok := parseFormKey(FormKey(tenant, form))
	if !ok || tid != tenant || fid != form {
		t.Fatal("la clave publica no se recupera")
	}
	for _, bad := range []string{"", tenant.String(), tenant.String() + "." + "x", "x." + form.String(), uuid.Nil.String() + "." + form.String()} {
		if _, _, ok := parseFormKey(bad); ok {
			t.Errorf("clave aceptada: %q", bad)
		}
	}
}

func TestCheckOriginCORS(t *testing.T) {
	h := formsHandler()
	f := sampleForm()
	cases := []struct {
		origin  string
		allowed bool
		cors    string
	}{
		{"", true, ""},
		{"https://acme.pe", true, "https://acme.pe"},
		{"https://app.plataforma.io", true, "https://app.plataforma.io"},
		{"https://evil.test", false, ""},
		{"null", false, ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		err := h.checkOrigin(rec, req, f)
		if (err == nil) != c.allowed || (err != nil && !errors.Is(err, errOriginNotAllowed)) {
			t.Errorf("%q: %v", c.origin, err)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != c.cors {
			t.Errorf("%q: Allow-Origin %q", c.origin, got)
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%q: CORS con credenciales", c.origin)
		}
	}
}

func TestFormCSP(t *testing.T) {
	h := formsHandler()
	csp := h.formCSP(sampleForm())
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'self' https://acme.pe", "form-action 'self' https://acme.pe", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("la CSP no lleva %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "script-src") {
		t.Fatal("la pagina del formulario no ejecuta scripts")
	}
	if !strings.Contains(h.formCSP(nil), "frame-ancestors 'none'") {
		t.Fatal("una pagina sin formulario no se incrusta")
	}
}

func TestEmbedPageSinScriptsYConCampoTrampa(t *testing.T) {
	h := formsHandler()
	f := sampleForm()
	rec := httptest.NewRecorder()
	h.writeFormPage(rec, http.StatusOK, f, embedTemplate, embedPage{
		Title: f.Texts.Title, ConsentText: f.Texts.ConsentText, SubmitLabel: "Suscribirme",
		Action: "https://app.plataforma.io/api/v1/public/contacts/forms/k/submit", Token: "tok", TopTarget: true,
		TokenField: formTokenField, ConsentField: formConsentField, HoneypotField: formHoneypotField,
		Fields: []embedField{{Name: "field.email", Label: "Correo", Required: true, Input: "email", Autocomplete: "email"}},
	})
	body := rec.Body.String()
	if strings.Contains(body, "<script") || strings.Contains(body, "<b>") {
		t.Fatalf("texto sin escapar en el formulario: %s", body)
	}
	for _, want := range []string{`name="homepage"`, `tabindex="-1"`, `name="_token" value="tok"`, `name="consent"`, `target="_top"`, `name="field.email"`} {
		if !strings.Contains(body, want) {
			t.Errorf("el formulario no lleva %s", want)
		}
	}
	hdr := rec.Header()
	if hdr.Get("X-Frame-Options") != "" || !strings.Contains(hdr.Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatalf("cabeceras de marco: %v", hdr)
	}
	if hdr.Get("Cache-Control") != "no-store" || !strings.Contains(hdr.Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("cabeceras: %v", hdr)
	}
}

func TestDecodeSubmission(t *testing.T) {
	form := url.Values{"_token": {"tok"}, "consent": {"on"}, "homepage": {""}, "field.email": {"a@acme.pe"}, "extra": {"x"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mode, ok := submitModeOf(req)
	if !ok || mode != submitHTML {
		t.Fatal("modo del formulario HTML")
	}
	in, err := decodeSubmission(req, mode)
	if err != nil {
		t.Fatal(err)
	}
	if in.Token != "tok" || !in.Consent || in.Strings["email"] != "a@acme.pe" || len(in.Strings) != 1 {
		t.Fatalf("envio %+v", in)
	}
	rep := url.Values{"field.email": {"a@acme.pe", "b@acme.pe"}}
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(rep.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := decodeSubmission(req, submitHTML); err == nil {
		t.Fatal("un campo repetido se acepta")
	}
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"token":"t","fields":{"email":"a@acme.pe"},"consent":true,"otro":1}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := decodeSubmission(req, submitJSON); err == nil {
		t.Fatal("un campo JSON desconocido se acepta")
	}
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	if _, ok := submitModeOf(req); ok {
		t.Fatal("multipart se acepta")
	}
}

func TestPublicFormErrorCodes(t *testing.T) {
	cases := map[error]int{
		&app.RateLimitedError{}:       http.StatusTooManyRequests,
		domain.ErrFormNotFound:        http.StatusNotFound,
		errOriginNotAllowed:           http.StatusForbidden,
		app.ErrFormTokenInvalid:       http.StatusBadRequest,
		domain.ErrConsentNotAccepted:  http.StatusUnprocessableEntity,
		app.ErrSuppressionUnavailable: http.StatusServiceUnavailable,
		app.ErrFormsUnavailable:       http.StatusServiceUnavailable,
	}
	for err, want := range cases {
		rec := httptest.NewRecorder()
		publicFormError(rec, err)
		if rec.Code != want {
			t.Errorf("%v: %d", err, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	publicFormError(rec, &app.RateLimitedError{})
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 sin Retry-After")
	}
}

func TestFormRoutesExigenSuPermiso(t *testing.T) {
	guard := &recordingGuard{}
	routes := NewHandler(Deps{UC: app.New(app.Deps{}), Perms: guard, PublicBaseURL: "https://app.plataforma.io"}).ContactRoutes()
	tenant := uuid.New()
	for _, c := range []struct{ method, path, action string }{
		{http.MethodGet, "/forms", "read"},
		{http.MethodPost, "/forms", "create"},
		{http.MethodGet, "/forms/meta", "read"},
		{http.MethodPatch, "/forms/" + uuid.NewString(), "update"},
		{http.MethodDelete, "/forms/" + uuid.NewString(), "delete"},
		{http.MethodGet, "/forms/" + uuid.NewString() + "/stats", "read"},
	} {
		guard.seen = nil
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		req = req.WithContext(middleware.WithTenantID(req.Context(), tenant.String()))
		routes.ServeHTTP(httptest.NewRecorder(), req)
		if len(guard.seen) != 1 || guard.seen[0] != [3]string{modContacts, "forms", c.action} {
			t.Errorf("%s %s exigio %v", c.method, c.path, guard.seen)
		}
	}
}

func TestEmbedScriptEscapaElTitulo(t *testing.T) {
	h := formsHandler()
	f := sampleForm()
	f.Texts.Title = `"</script><script>alert(1)</script>`
	js := h.embedScriptFor(f, domain.Definitions{})
	if strings.Contains(js, "</script>") || strings.Contains(js, `"</`) {
		t.Fatalf("el titulo escapa del literal: %s", js)
	}
	key := FormKey(f.TenantID, f.ID)
	if !strings.Contains(js, `"https://app.plataforma.io/api/v1/public/contacts/forms/`+key+`/embed"`) {
		t.Fatalf("direccion del iframe: %s", js)
	}
	if !strings.Contains(js, "allow-forms allow-same-origin") || strings.Contains(js, "allow-scripts") {
		t.Fatalf("sandbox del iframe: %s", js)
	}
}
