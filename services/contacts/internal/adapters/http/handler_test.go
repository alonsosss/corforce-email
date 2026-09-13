package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/contacts/internal/app"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

// recordingGuard deja pasar y anota el permiso que exigio cada peticion.
type recordingGuard struct{ seen [][3]string }

func (g *recordingGuard) RequirePermission(module, resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			g.seen = append(g.seen, [3]string{module, resource, action})
			next.ServeHTTP(w, r)
		})
	}
}

// attributesOnly es el unico repositorio que necesita el catalogo.
type attributesOnly struct{ defs []domain.AttributeDefinition }

func (a attributesOnly) List(context.Context, uuid.UUID) ([]domain.AttributeDefinition, error) {
	return a.defs, nil
}
func (a attributesOnly) Get(context.Context, uuid.UUID, string) (*domain.AttributeDefinition, error) {
	return nil, domain.ErrAttributeNotFound
}
func (a attributesOnly) Create(context.Context, *domain.AttributeDefinition) error { return nil }
func (a attributesOnly) Update(context.Context, *domain.AttributeDefinition) error { return nil }
func (a attributesOnly) Delete(context.Context, uuid.UUID, string) error           { return nil }

// El catalogo del DSL es el contrato del editor de segmentos: campos, operadores, valores
// de los enumerados y atributos declarados salen del dominio y del compilador.
func TestSegmentMetaPublicaElCatalogoDeLaEmpresa(t *testing.T) {
	tenant := uuid.New()
	uc := app.New(app.Deps{Attributes: attributesOnly{defs: []domain.AttributeDefinition{
		{TenantID: tenant, Key: "plan", Type: domain.AttrString},
		{TenantID: tenant, Key: "alta", Type: domain.AttrDate},
	}}})
	guard := &recordingGuard{}
	routes := NewHandler(Deps{UC: uc, Perms: guard}).SegmentRoutes()

	req := httptest.NewRequest(http.MethodGet, "/meta", nil)
	req = req.WithContext(middleware.WithTenantID(req.Context(), tenant.String()))
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if want := [3]string{modSegments, "segments", "read"}; len(guard.seen) != 1 || guard.seen[0] != want {
		t.Errorf("permiso exigido %v, esperado %v", guard.seen, want)
	}

	var envelope struct {
		Data segment.Catalog `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("JSON invalido: %v", err)
	}
	cat := envelope.Data
	values := map[string][]string{}
	for _, f := range cat.Fields {
		values[f.Field] = f.Values
	}
	statuses := make([]string, 0, len(domain.Statuses()))
	for _, s := range domain.Statuses() {
		statuses = append(statuses, string(s))
	}
	if !slices.Equal(values["status"], statuses) {
		t.Errorf("status: %v, dominio %v", values["status"], statuses)
	}
	sources := make([]string, 0, len(domain.Sources()))
	for _, s := range domain.Sources() {
		sources = append(sources, string(s))
	}
	if !slices.Equal(values["source"], sources) {
		t.Errorf("source: %v, dominio %v", values["source"], sources)
	}
	if !slices.Contains(values["consent"], string(domain.ConsentGranted)) {
		t.Errorf("consent sin granted: %v", values["consent"])
	}
	wantAttrs := []segment.AttributeInfo{{Key: "alta", Type: segment.AttrDate}, {Key: "plan", Type: segment.AttrString}}
	if !slices.Equal(cat.Attributes, wantAttrs) {
		t.Errorf("atributos %v, esperados %v", cat.Attributes, wantAttrs)
	}
	if cat.AttributePrefix == "" || len(cat.Operators) == 0 || cat.Limits.MaxRules != segment.MaxRules {
		t.Errorf("catalogo incompleto: %+v", cat)
	}
}
