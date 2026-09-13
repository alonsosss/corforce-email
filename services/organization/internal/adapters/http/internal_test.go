package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/organization/internal/app"
	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// registroFake es el registro de empresas y celdas en memoria; cuenta las lecturas para
// comprobar que una peticion rechazada no llega a el.
type registroFake struct {
	tenants map[uuid.UUID]*domain.Tenant
	cells   map[uuid.UUID]*domain.Cell
	reads   int
}

func (f *registroFake) GetByID(_ context.Context, id uuid.UUID) (*domain.Tenant, error) {
	f.reads++
	if t, ok := f.tenants[id]; ok {
		return t, nil
	}
	return nil, domain.ErrTenantNotFound
}

func (f *registroFake) GetBySlug(context.Context, string) (*domain.Tenant, error) {
	return nil, domain.ErrTenantNotFound
}

func (f *registroFake) List(context.Context, int, int) ([]*domain.Tenant, int64, error) {
	return nil, 0, nil
}

func (f *registroFake) Update(context.Context, *domain.Tenant) error { return nil }

// celdasFake adapta el mismo registro al puerto de celdas.
type celdasFake struct{ r *registroFake }

func (c celdasFake) Create(context.Context, *domain.Cell) error { return nil }

func (c celdasFake) GetByID(_ context.Context, id uuid.UUID) (*domain.Cell, error) {
	if cell, ok := c.r.cells[id]; ok {
		return cell, nil
	}
	return nil, domain.ErrCellNotFound
}

func (c celdasFake) GetByCode(context.Context, string) (*domain.Cell, error) {
	return nil, domain.ErrCellNotFound
}

func (c celdasFake) List(context.Context) ([]*domain.Cell, error) { return nil, nil }
func (c celdasFake) Update(context.Context, *domain.Cell) error   { return nil }

func internalServer(reg *registroFake) http.Handler {
	uc := app.NewOrganizationUseCase(app.Dependencies{Tenants: reg, Cells: celdasFake{reg}})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/internal/organization", NewInternalHandler(uc).Routes())
	return r
}

func TestCeldaDeLaEmpresaParaElGateway(t *testing.T) {
	pe02 := &domain.Cell{ID: uuid.New(), Code: "pe-02", DBHost: "pg-pe-02.internal", DBPort: 5432}
	beta := &domain.Tenant{ID: uuid.New(), Slug: "beta", CellID: pe02.ID, Status: domain.TenantStatusActive}
	rota := &domain.Tenant{ID: uuid.New(), Slug: "rota", CellID: uuid.New(), Status: domain.TenantStatusActive}
	reg := &registroFake{
		tenants: map[uuid.UUID]*domain.Tenant{beta.ID: beta, rota.ID: rota},
		cells:   map[uuid.UUID]*domain.Cell{pe02.ID: pe02},
	}
	srv := internalServer(reg)
	pedir := func(tenant string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/internal/organization/tenants/"+tenant+"/cell", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := pedir(beta.ID.String(), nil)
	var ok struct {
		Data map[string]string `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ok) != nil {
		t.Fatalf("celda de beta: %d %s", rec.Code, rec.Body)
	}
	if ok.Data["tenant_id"] != beta.ID.String() || ok.Data["cell_code"] != "pe-02" || len(ok.Data) != 2 {
		t.Fatalf("contrato: %v", ok.Data)
	}
	if strings.Contains(rec.Body.String(), pe02.DBHost) {
		t.Fatalf("el host de la base de la celda no sale de organization: %s", rec.Body)
	}

	if rec := pedir(uuid.NewString(), nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"TENANT_NOT_FOUND"`) {
		t.Fatalf("empresa inexistente: %d %s", rec.Code, rec.Body)
	}
	if rec := pedir("no-es-uuid", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("identificador invalido: %d", rec.Code)
	}
	// Un registro incoherente es un fallo, no "empresa sin celda": el gateway falla cerrado.
	if rec := pedir(rota.ID.String(), nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("celda que falta en el directorio: %d %s", rec.Code, rec.Body)
	}

	// Una persona, aunque el gateway la haya autenticado, no pregunta por celdas.
	antes := reg.reads
	rec = pedir(beta.ID.String(), map[string]string{"X-User-ID": uuid.NewString(), "X-Tenant-ID": beta.ID.String()})
	if rec.Code != http.StatusForbidden || reg.reads != antes {
		t.Fatalf("peticion con usuario: %d, lecturas %d -> %d", rec.Code, antes, reg.reads)
	}
}
