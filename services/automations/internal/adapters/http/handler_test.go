package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/automations/internal/app"
	"github.com/alonsosss/corforce-email/services/automations/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type allowAll struct{}

func (allowAll) RequirePermission(string, string, string) func(nethttp.Handler) nethttp.Handler {
	return func(next nethttp.Handler) nethttp.Handler { return next }
}

type server struct {
	h         nethttp.Handler
	uc        *app.UseCase
	templates *apptest.Templates
	tenant    string
	user      string
}

func newServer(t *testing.T) *server {
	t.Helper()
	clock := &apptest.Clock{T: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	store := apptest.NewStore(clock)
	tpl := &apptest.Templates{Kind: "marketing", Version: 2}
	uc := app.New(app.Deps{
		Settings: apptest.Settings{S: store}, Deliveries: apptest.Deliveries{S: store}, Workflows: apptest.Workflows{S: store},
		Runs: apptest.Runs{S: store}, Processed: apptest.Processed{S: store}, Tx: store, Events: store,
		Sender: &apptest.Sender{}, Contacts: apptest.NewContacts(), Templates: tpl,
		Config: app.Config{PublicBaseURL: "https://app.example.com"}, Now: clock.Now,
	})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/api/v1/automations", NewHandler(uc, allowAll{}).Routes())
	return &server{h: r, uc: uc, templates: tpl, tenant: uuid.NewString(), user: uuid.NewString()}
}

func (s *server) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/automations"+path, strings.NewReader(body))
	req.Header.Set("X-Tenant-ID", s.tenant)
	req.Header.Set("X-User-ID", s.user)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
	Meta struct {
		Total int `json:"total"`
	} `json:"meta"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible (%d): %s", rec.Code, rec.Body.String())
	}
	return env
}

func workflowBody(name string) string {
	return `{"name":"` + name + `","trigger":{"type":"contact.created"},"re_entry":false,"steps":[` +
		`{"type":"wait","duration":"1d"},` +
		`{"type":"send_email","template_id":"` + uuid.NewString() + `","from_email":"news@shop.example.com","from_name":"Tienda"}]}`
}

func TestFlujosPorLaAPI(t *testing.T) {
	s := newServer(t)
	rec := s.do(nethttp.MethodPost, "/workflows", workflowBody("Bienvenida"))
	if rec.Code != nethttp.StatusCreated {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body.String())
	}
	var w domain.Workflow
	_ = json.Unmarshal(decode(t, rec).Data, &w)
	if w.Status != domain.StatusDraft || len(w.Steps) != 2 {
		t.Fatalf("flujo: %+v", w)
	}
	if rec := s.do(nethttp.MethodPost, "/workflows", workflowBody("bienvenida")); rec.Code != nethttp.StatusConflict || decode(t, rec).Error.Code != "NAME_TAKEN" {
		t.Fatalf("nombre repetido: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodPost, "/workflows", `{"name":"X","trigger":{"type":"contact.created"},"steps":[{"type":"wait","duration":"91d"}]}`); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("paso invalido: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodPost, "/workflows", `{"name":"X","trigger":{"type":"contact.created"},"steps":[{"type":"wait","duration":"1d","branch":true}]}`); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("campo desconocido en un paso: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodPost, "/workflows", `{"name":"X","steps":[{"type":"wait","duration":"1d"}]}`); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("sin disparador: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodGet, "/workflows?status=running", ""); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("estado desconocido: %d", rec.Code)
	}
	if env := decode(t, s.do(nethttp.MethodGet, "/workflows?status=draft", "")); env.Meta.Total != 1 {
		t.Fatalf("listado: %+v", env.Meta)
	}

	list := uuid.NewString()
	if rec := s.do(nethttp.MethodPatch, "/workflows/"+w.ID.String(), `{"list_id":"`+list+`"}`); rec.Code != nethttp.StatusOK {
		t.Fatalf("poner lista: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(nethttp.MethodPatch, "/workflows/"+w.ID.String(), `{"list_id":null}`)
	_ = json.Unmarshal(decode(t, rec).Data, &w)
	if rec.Code != nethttp.StatusOK || w.ListID != nil {
		t.Fatalf("quitar lista con null: %d %+v", rec.Code, w.ListID)
	}

	rec = s.do(nethttp.MethodPost, "/workflows/"+w.ID.String()+"/activate", "")
	_ = json.Unmarshal(decode(t, rec).Data, &w)
	if rec.Code != nethttp.StatusOK || w.Status != domain.StatusActive || *w.Steps[1].TemplateVersion != 2 {
		t.Fatalf("activar: %d %+v", rec.Code, w)
	}
	if rec := s.do(nethttp.MethodPatch, "/workflows/"+w.ID.String(), `{"name":"Otro"}`); rec.Code != nethttp.StatusConflict || decode(t, rec).Error.Code != "NOT_EDITABLE" {
		t.Fatalf("editar activo: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodDelete, "/workflows/"+w.ID.String(), ""); rec.Code != nethttp.StatusConflict {
		t.Fatalf("borrar activo: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodPost, "/workflows/"+w.ID.String()+"/pause", `{"reason":"revision"}`); rec.Code != nethttp.StatusOK {
		t.Fatalf("pausar: %d %s", rec.Code, rec.Body.String())
	}
	if rec := s.do(nethttp.MethodPost, "/workflows/"+w.ID.String()+"/archive", ""); rec.Code != nethttp.StatusOK {
		t.Fatalf("archivar: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodGet, "/workflows/"+w.ID.String()+"/runs?status=waiting", ""); rec.Code != nethttp.StatusOK {
		t.Fatalf("ejecuciones: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodGet, "/runs/"+uuid.NewString(), ""); rec.Code != nethttp.StatusNotFound {
		t.Fatalf("ejecucion inexistente: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodDelete, "/workflows/"+w.ID.String(), ""); rec.Code != nethttp.StatusNoContent {
		t.Fatalf("borrar archivado: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodGet, "/workflows/no-es-uuid", ""); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("id no valido: %d", rec.Code)
	}
}

func TestDobleOptInPorLaAPI(t *testing.T) {
	s := newServer(t)
	tpl := uuid.NewString()
	body := `{"enabled":true,"template_id":"` + tpl + `","from_email":"hola@shop.example.com","from_name":"Tienda","reply_to":""}`
	if rec := s.do(nethttp.MethodPut, "/double-opt-in", body); rec.Code != nethttp.StatusUnprocessableEntity || decode(t, rec).Error.Code != "TEMPLATE_NOT_TRANSACTIONAL" {
		t.Fatalf("plantilla de marketing: %d %s", rec.Code, rec.Body.String())
	}
	s.templates.Kind = "transactional"
	if rec := s.do(nethttp.MethodPut, "/double-opt-in", body); rec.Code != nethttp.StatusOK {
		t.Fatalf("guardar: %d %s", rec.Code, rec.Body.String())
	}
	var got domain.DOISettings
	_ = json.Unmarshal(decode(t, s.do(nethttp.MethodGet, "/double-opt-in", "")).Data, &got)
	if !got.Enabled || got.TemplateID == nil || got.TemplateID.String() != tpl {
		t.Fatalf("leer: %+v", got)
	}

	tenant := uuid.MustParse(s.tenant)
	req := domain.ConsentRequest{
		EventID: uuid.NewString(), TenantID: tenant, ContactID: uuid.New(), Email: "ana@example.com",
		ConfirmURL: "https://app.example.com/api/v1/public/contacts/confirm?t=x&k=credencial-secreta",
	}
	if _, err := s.uc.HandleConsentRequested(ctxFor(tenant), req); err != nil {
		t.Fatal(err)
	}
	rec := s.do(nethttp.MethodGet, "/double-opt-in/deliveries?status=sent", "")
	if rec.Code != nethttp.StatusOK || decode(t, rec).Meta.Total != 1 {
		t.Fatalf("historial: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "credencial-secreta") || strings.Contains(rec.Body.String(), "confirm_url") {
		t.Fatalf("el historial no muestra el enlace: %s", rec.Body.String())
	}
	if rec := s.do(nethttp.MethodGet, "/double-opt-in/deliveries?status=lost", ""); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("estado desconocido: %d", rec.Code)
	}
	if rec := s.do(nethttp.MethodPut, "/double-opt-in", `{"enabled":true}`); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("activado sin plantilla: %d", rec.Code)
	}
}
