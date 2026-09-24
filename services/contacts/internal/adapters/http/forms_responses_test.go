package http

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

type openGuard struct{}

func (openGuard) AllowIP(context.Context, string) (bool, time.Duration) { return true, 0 }
func (openGuard) AllowForm(context.Context, uuid.UUID, uuid.UUID) (bool, time.Duration) {
	return true, 0
}
func (openGuard) FirstUse(context.Context, string) bool { return true }

// publicHandler tiene todo lo que necesitan las paginas publicas salvo la base de la empresa.
func publicHandler(t *testing.T) *Handler {
	t.Helper()
	signer, err := app.NewFormTokenSigner(strings.Repeat("s", 40), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uc := app.New(app.Deps{
		Forms: &memForms{items: map[uuid.UUID]*domain.SubscriptionForm{}}, FormGuard: openGuard{}, FormTokens: signer,
		Attributes: attributesOnly{},
	})
	return NewHandler(Deps{UC: uc, Perms: &recordingGuard{}, PublicBaseURL: "https://app.plataforma.io"})
}

func TestRespuestaDeExitoSegunElModo(t *testing.T) {
	h := publicHandler(t)
	f := sampleForm()

	rec := httptest.NewRecorder()
	h.submitAccepted(rec, submitJSON, f)
	var out struct {
		Data struct {
			Status      string  `json:"status"`
			Message     string  `json:"message"`
			RedirectURL *string `json:"redirect_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusAccepted {
		t.Fatalf("JSON: %d %s", rec.Code, rec.Body)
	}
	if out.Data.Status != "received" || out.Data.Message != "Gracias" || out.Data.RedirectURL == nil {
		t.Fatalf("respuesta %+v", out.Data)
	}

	rec = httptest.NewRecorder()
	h.submitAccepted(rec, submitHTML, f)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://acme.pe/gracias" {
		t.Fatalf("redireccion: %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "form-action 'self' https://acme.pe") {
		t.Fatalf("la redireccion tras el envio la permite form-action: %s", rec.Header().Get("Content-Security-Policy"))
	}

	f.RedirectURL = nil
	rec = httptest.NewRecorder()
	h.submitAccepted(rec, submitHTML, f)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Gracias") || strings.Contains(rec.Body.String(), "<b>") {
		t.Fatalf("pagina de gracias: %d %s", rec.Code, rec.Body)
	}
}

func TestFallosDelFormularioHTML(t *testing.T) {
	h := publicHandler(t)
	f := sampleForm()
	f.RedirectURL = nil

	rec := httptest.NewRecorder()
	err := fmt.Errorf("%w: Correo es obligatorio", domain.ErrInvalidSubmission)
	h.submitFailed(rec, submitHTML, f, domain.Definitions{}, map[string]string{"email": "ana@"}, err)
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, `role="alert">Correo es obligatorio`) {
		t.Fatalf("el formulario vuelve con el aviso: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, `value="ana@"`) || !strings.Contains(body, `name="_token" value="`) {
		t.Fatalf("el formulario conserva lo escrito y lleva un token nuevo: %s", body)
	}

	rec = httptest.NewRecorder()
	h.submitFailed(rec, submitHTML, f, domain.Definitions{}, nil, &app.RateLimitedError{RetryAfter: 90 * time.Second})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "90" {
		t.Fatalf("cupo: %d %v", rec.Code, rec.Header())
	}

	rec = httptest.NewRecorder()
	h.submitFailed(rec, submitHTML, f, domain.Definitions{}, nil, errOriginNotAllowed)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("origen: %d %v", rec.Code, rec.Header())
	}

	for err, want := range map[error]int{
		domain.ErrFormNotFound:        http.StatusNotFound,
		app.ErrSuppressionUnavailable: http.StatusServiceUnavailable,
		errTenantUnavailable:          http.StatusServiceUnavailable,
	} {
		rec = httptest.NewRecorder()
		h.submitFailed(rec, submitHTML, nil, nil, nil, err)
		if rec.Code != want || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%v: %d", err, rec.Code)
		}
	}

	rec = httptest.NewRecorder()
	h.submitFailed(rec, submitJSON, f, nil, nil, app.ErrFormTokenInvalid)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "FORM_TOKEN_INVALID") {
		t.Fatalf("JSON: %d %s", rec.Code, rec.Body)
	}
}

func TestEmbedConLosCamposActivos(t *testing.T) {
	h := publicHandler(t)
	f := sampleForm()
	f.Fields = append(f.Fields,
		domain.FormField{Key: "vip", Label: "Quiero ofertas"},
		domain.FormField{Key: "retirado", Label: "Retirado"},
		domain.FormField{Key: "alta", Label: "Fecha"})
	defs := domain.Definitions{"vip": {Key: "vip", Type: domain.AttrBoolean}, "alta": {Key: "alta", Type: domain.AttrDate}}
	rec := httptest.NewRecorder()
	h.writeEmbed(rec, http.StatusOK, f, defs, "", map[string]string{"vip": "on"})
	body := rec.Body.String()
	if strings.Contains(body, "Retirado") {
		t.Fatal("un atributo retirado se sigue pidiendo")
	}
	for _, want := range []string{`type="checkbox" name="field.vip" value="on" checked`, `type="date" name="field.alta"`, `type="email" name="field.email"`} {
		if !strings.Contains(body, want) {
			t.Errorf("el formulario no lleva %s", want)
		}
	}
	if strings.Contains(body, `target="_top"`) != (f.RedirectURL != nil) {
		t.Fatal("el envio sale del marco solo si hay pagina de gracias propia")
	}
}

func TestFormularioNoDisponible(t *testing.T) {
	h := publicHandler(t)
	rec := httptest.NewRecorder()
	h.writeFormUnavailable(rec, domain.ErrFormNotFound)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Formulario no disponible") {
		t.Fatalf("404: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.writeFormUnavailable(rec, app.ErrFormsUnavailable)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("503: %d", rec.Code)
	}
}

func TestInputTypeYAutocompletado(t *testing.T) {
	for typ, want := range map[string]string{"email": "email", "number": "number", "date": "date", "boolean": "checkbox", "string": "text"} {
		if got := inputType(typ); got != want {
			t.Errorf("%s: %s", typ, got)
		}
	}
	if autocompleteFor("first_name") != "given-name" || autocompleteFor("empresa") != "off" {
		t.Fatal("autocompletado")
	}
	f := sampleForm()
	f.Texts.SubmitLabel = ""
	if submitLabel(f) == "" {
		t.Fatal("un boton sin texto")
	}
}
