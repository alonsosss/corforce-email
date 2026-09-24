package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// memForms guarda los formularios en memoria con las mismas reglas de unicidad que la base.
type memForms struct {
	items map[uuid.UUID]*domain.SubscriptionForm
}

func (m *memForms) Create(_ context.Context, f *domain.SubscriptionForm) error {
	for _, o := range m.items {
		if o.TenantID == f.TenantID && o.Name == f.Name {
			return domain.ErrFormExists
		}
	}
	f.ID, f.CreatedAt, f.UpdatedAt = uuid.New(), time.Now(), time.Now()
	cp := *f
	m.items[f.ID] = &cp
	return nil
}

func (m *memForms) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.SubscriptionForm, error) {
	f, ok := m.items[id]
	if !ok || f.TenantID != tenantID {
		return nil, domain.ErrFormNotFound
	}
	cp := *f
	return &cp, nil
}

func (m *memForms) Update(_ context.Context, f *domain.SubscriptionForm) error {
	cp := *f
	m.items[f.ID] = &cp
	return nil
}

func (m *memForms) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	if f, ok := m.items[id]; !ok || f.TenantID != tenantID {
		return domain.ErrFormNotFound
	}
	delete(m.items, id)
	return nil
}

func (m *memForms) List(_ context.Context, tenantID uuid.UUID, _, _ int) ([]domain.SubscriptionForm, int64, error) {
	out := []domain.SubscriptionForm{}
	for _, f := range m.items {
		if f.TenantID == tenantID {
			out = append(out, *f)
		}
	}
	return out, int64(len(out)), nil
}

func (m *memForms) UsingList(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }
func (m *memForms) InsertSubmission(context.Context, *domain.FormSubmissionRecord) error {
	return nil
}
func (m *memForms) ConfirmSubmission(context.Context, uuid.UUID, uuid.UUID, time.Time) (*uuid.UUID, error) {
	return nil, nil
}
func (m *memForms) Stats(_ context.Context, _, _ uuid.UUID, from, to time.Time) (*domain.FormStats, error) {
	return &domain.FormStats{From: from, To: to, Submitted: 3, Confirmed: 1, Daily: []domain.FormStatsDay{}}, nil
}

// oneList es la unica lista de la empresa.
type oneList struct{ id uuid.UUID }

func (l oneList) Create(context.Context, *domain.List) error { return nil }
func (l oneList) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.List, error) {
	if id != l.id {
		return nil, domain.ErrListNotFound
	}
	return &domain.List{ID: id, TenantID: tenantID, Name: "boletin"}, nil
}
func (l oneList) Update(context.Context, *domain.List) error         { return nil }
func (l oneList) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (l oneList) List(context.Context, uuid.UUID, int, int) ([]domain.List, int64, error) {
	return nil, 0, nil
}
func (l oneList) ExistingIDs(context.Context, uuid.UUID, []uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (l oneList) AddMembers(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) (int, error) {
	return 0, nil
}
func (l oneList) RemoveMembers(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) (int, error) {
	return 0, nil
}
func (l oneList) MembersAmong(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (l oneList) ListsOf(context.Context, uuid.UUID, uuid.UUID) ([]domain.List, error) {
	return nil, nil
}

type crudFixture struct {
	routes http.Handler
	tenant uuid.UUID
	list   uuid.UUID
	forms  *memForms
}

func newCRUDFixture() *crudFixture {
	f := &crudFixture{tenant: uuid.New(), list: uuid.New(), forms: &memForms{items: map[uuid.UUID]*domain.SubscriptionForm{}}}
	uc := app.New(app.Deps{Forms: f.forms, Lists: oneList{id: f.list}, Attributes: attributesOnly{}})
	f.routes = NewHandler(Deps{UC: uc, Perms: &recordingGuard{}, PublicBaseURL: "https://app.plataforma.io"}).ContactRoutes()
	return f
}

func (f *crudFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithIdentity(req.Context(), uuid.NewString(), f.tenant.String()))
	rec := httptest.NewRecorder()
	f.routes.ServeHTTP(rec, req)
	return rec
}

func (f *crudFixture) body(name string) string {
	return `{"name":"` + name + `","list_id":"` + f.list.String() + `",
		"fields":[{"key":"email","label":"Correo"},{"key":"first_name","label":"Nombre","placeholder":"Ana"}],
		"texts":{"title":"Boletin","consent_text":"Acepto","success_message":"Gracias"},
		"redirect_url":"https://acme.pe/gracias","allowed_origins":["https://Acme.PE"]}`
}

type formEnvelope struct {
	Data struct {
		ID             string   `json:"id"`
		Name           string   `json:"name"`
		Status         string   `json:"status"`
		RedirectURL    *string  `json:"redirect_url"`
		AllowedOrigins []string `json:"allowed_origins"`
		Fields         []struct {
			Key      string `json:"key"`
			Required bool   `json:"required"`
		} `json:"fields"`
		Embed formEmbed `json:"embed"`
	} `json:"data"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func decodeForm(t *testing.T, rec *httptest.ResponseRecorder) formEnvelope {
	t.Helper()
	var out formEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("respuesta %d: %s", rec.Code, rec.Body)
	}
	return out
}

func TestFormCRUDPorElAPI(t *testing.T) {
	f := newCRUDFixture()
	rec := f.do(t, http.MethodPost, "/forms", f.body("Portada"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body)
	}
	created := decodeForm(t, rec).Data
	if created.Status != "active" || !created.Fields[0].Required || created.AllowedOrigins[0] != "https://acme.pe" {
		t.Fatalf("alta normalizada: %+v", created)
	}
	key := f.tenant.String() + "." + created.ID
	base := "https://app.plataforma.io/api/v1/public/contacts/forms/" + key
	if created.Embed.Key != key || created.Embed.IframeURL != base+"/embed" || created.Embed.ScriptURL != base+"/embed.js" ||
		created.Embed.DefinitionURL != base || created.Embed.SubmitURL != base+"/submit" {
		t.Fatalf("direcciones para incrustar: %+v", created.Embed)
	}

	if rec := f.do(t, http.MethodPost, "/forms", f.body("Portada")); decodeForm(t, rec).Error.Code != "FORM_EXISTS" || rec.Code != http.StatusConflict {
		t.Fatalf("nombre repetido: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, http.MethodPost, "/forms", `{"name":"x"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("alta incompleta: %d", rec.Code)
	}
	if rec := f.do(t, http.MethodPost, "/forms", strings.Replace(f.body("Otro"), f.list.String(), uuid.NewString(), 1)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("lista ajena: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, http.MethodPost, "/forms", strings.Replace(f.body("Otro"), `"https://Acme.PE"`, `"http://acme.pe"`, 1)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("origen http: %d", rec.Code)
	}

	rec = f.do(t, http.MethodPatch, "/forms/"+created.ID, `{"redirect_url":null,"status":"disabled"}`)
	updated := decodeForm(t, rec).Data
	if rec.Code != http.StatusOK || updated.RedirectURL != nil || updated.Status != "disabled" || updated.Name != "Portada" {
		t.Fatalf("parche: %d %+v", rec.Code, updated)
	}
	if rec := f.do(t, http.MethodPatch, "/forms/"+created.ID, `{"status":"borrado"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("estado no valido: %d", rec.Code)
	}
	if rec := f.do(t, http.MethodPatch, "/forms/"+created.ID, `{"redirect_url":5}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("redireccion no textual: %d", rec.Code)
	}

	if rec := f.do(t, http.MethodGet, "/forms/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("detalle: %d", rec.Code)
	}
	if rec := f.do(t, http.MethodGet, "/forms/"+uuid.NewString(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("inexistente: %d", rec.Code)
	}
	if rec := f.do(t, http.MethodGet, "/forms", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("listado: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, http.MethodGet, "/forms/"+created.ID+"/stats?days=7", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"submitted":3`) {
		t.Fatalf("estadisticas: %d %s", rec.Code, rec.Body)
	}
	for _, days := range []string{"0", "366", "x"} {
		if rec := f.do(t, http.MethodGet, "/forms/"+created.ID+"/stats?days="+days, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("days=%s: %d", days, rec.Code)
		}
	}
	if rec := f.do(t, http.MethodDelete, "/forms/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("borrado: %d", rec.Code)
	}
	if rec := f.do(t, http.MethodDelete, "/forms/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("borrado repetido: %d", rec.Code)
	}
}

func TestFormMetaPublicaLosTopes(t *testing.T) {
	f := newCRUDFixture()
	rec := f.do(t, http.MethodGet, "/forms/meta", "")
	var out struct {
		Data formMetaResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("meta: %d %s", rec.Code, rec.Body)
	}
	if out.Data.Limits.MaxFields != domain.MaxFormFields || len(out.Data.BuiltinFields) != 3 ||
		out.Data.MinFillSeconds != int(app.DefaultFormMinFill.Seconds()) || len(out.Data.Statuses) != 2 {
		t.Fatalf("meta %+v", out.Data)
	}
}

func TestFormsSinRepositorioResponden503(t *testing.T) {
	routes := NewHandler(Deps{UC: app.New(app.Deps{}), Perms: &recordingGuard{}}).ContactRoutes()
	req := httptest.NewRequest(http.MethodGet, "/forms", nil)
	req = req.WithContext(middleware.WithTenantID(req.Context(), uuid.NewString()))
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "FORMS_UNAVAILABLE") {
		t.Fatalf("sin formularios: %d %s", rec.Code, rec.Body)
	}
}

func TestListInUseByFormEs409(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, domain.ErrListInUseByForm)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "LIST_IN_USE") {
		t.Fatalf("lista en uso: %d %s", rec.Code, rec.Body)
	}
}
