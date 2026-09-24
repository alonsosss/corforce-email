package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

type assistantRepo struct {
	rows map[uuid.UUID]*domain.AssistantSettings
}

func (f *assistantRepo) Get(_ context.Context, tenantID uuid.UUID) (*domain.AssistantSettings, error) {
	s, ok := f.rows[tenantID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *s
	return &c, nil
}

func (f *assistantRepo) Upsert(_ context.Context, s *domain.AssistantSettings) error {
	s.UpdatedAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	c := *s
	f.rows[s.TenantID] = &c
	return nil
}

type assistantEnv struct {
	h      http.Handler
	repo   *assistantRepo
	m      *domain.Mailbox
	tenant uuid.UUID
}

func assistantServer(t *testing.T) *assistantEnv {
	t.Helper()
	m := &domain.Mailbox{ID: uuid.New(), TenantID: uuid.New(), Username: "ana@acme.test"}
	e := &assistantEnv{repo: &assistantRepo{rows: map[uuid.UUID]*domain.AssistantSettings{}}, m: m, tenant: m.TenantID}
	uc := app.New(app.Deps{
		Tx: vacTx{}, Retirements: vacRetirements{}, Locator: &vacLocator{m: m}, Assistant: e.repo,
		Clock: func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	e.h = middleware.InjectFromGateway(NewHandler(uc, authz.NewChecker("http://127.0.0.1:9", "")).Routes())
	return e
}

func (e *assistantEnv) call(method, path, body string, roles ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := middleware.WithIdentity(req.Context(), uuid.NewString(), e.tenant.String())
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// internal es la llamada del webmail: sin usuario ni empresa, solo el buzon en la consulta.
func (e *assistantEnv) internal(username string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/internal/mail-directory/assistant?username="+username, nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// assistantContract escribe a mano los nombres JSON que consume la interfaz.
type assistantContract struct {
	Enabled   bool       `json:"enabled"`
	EnabledAt *time.Time `json:"enabled_at"`
	UpdatedBy *string    `json:"updated_by"`
	UpdatedAt *time.Time `json:"updated_at"`
}

func TestAsistenteApagadoPorDefectoYActivablePorElAdministrador(t *testing.T) {
	e := assistantServer(t)
	rec := e.call(http.MethodGet, "/api/v1/mail-directory/assistant", "", middleware.RoleTenantAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	var got assistantContract
	decodeEnvelope(t, rec, &got)
	if got.Enabled || got.EnabledAt != nil || got.UpdatedAt != nil {
		t.Fatalf("una empresa sin fila debe tenerlo apagado: %+v", got)
	}

	rec = e.call(http.MethodPut, "/api/v1/mail-directory/assistant", `{"enabled":true}`, middleware.RoleTenantAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	decodeEnvelope(t, rec, &got)
	if !got.Enabled || got.EnabledAt == nil || got.UpdatedBy == nil || got.UpdatedAt == nil {
		t.Fatalf("activado: %+v", got)
	}

	rec = e.internal("ana@acme.test")
	var internal struct {
		Enabled bool `json:"enabled"`
	}
	decodeEnvelope(t, rec, &internal)
	if rec.Code != http.StatusOK || !internal.Enabled {
		t.Fatalf("el webmail debe verlo activado: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "updated_by") {
		t.Fatalf("la ruta interna solo dice si esta activado: %s", rec.Body)
	}
}

func TestAsistenteExigeEnabledExplicito(t *testing.T) {
	e := assistantServer(t)
	rec := e.call(http.MethodPut, "/api/v1/mail-directory/assistant", `{}`, middleware.RoleTenantAdmin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("un cuerpo sin enabled no cambia nada: %d %s", rec.Code, rec.Body)
	}
	if len(e.repo.rows) != 0 {
		t.Fatal("no debe guardar nada")
	}
}

// Sin rol de empresa el permiso se pregunta a access-control, que aqui no responde: falla cerrado.
func TestAsistenteSinPermisoNoSeLeeNiSeCambia(t *testing.T) {
	e := assistantServer(t)
	denied := func(code int) bool {
		return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusServiceUnavailable
	}
	if rec := e.call(http.MethodGet, "/api/v1/mail-directory/assistant", ""); !denied(rec.Code) {
		t.Fatalf("GET sin permiso: %d", rec.Code)
	}
	if rec := e.call(http.MethodPut, "/api/v1/mail-directory/assistant", `{"enabled":true}`); !denied(rec.Code) {
		t.Fatalf("PUT sin permiso: %d", rec.Code)
	}
	if len(e.repo.rows) != 0 {
		t.Fatal("no debe guardar nada")
	}
}

func TestAsistenteInternoBuzonDesconocido(t *testing.T) {
	e := assistantServer(t)
	if rec := e.internal("nadie@acme.test"); rec.Code != http.StatusNotFound {
		t.Fatalf("un buzon que no es de la celda: %d %s", rec.Code, rec.Body)
	}
}
