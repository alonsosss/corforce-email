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
	domains map[string]uuid.UUID
	// removing son las empresas con la baja en curso: no reclaman dominios.
	removing map[uuid.UUID]bool
	reads    int
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

// dominiosFake es el indice de dominios del mismo registro, con la semantica del repositorio.
type dominiosFake struct{ r *registroFake }

func (d dominiosFake) Claim(_ context.Context, name string, tenantID uuid.UUID) error {
	if d.r.domains == nil {
		d.r.domains = map[string]uuid.UUID{}
	}
	if d.r.removing[tenantID] {
		return domain.ErrTenantBeingRemoved
	}
	if owner, ok := d.r.domains[name]; ok && owner != tenantID {
		return domain.ErrMailDomainClaimed
	}
	d.r.domains[name] = tenantID
	return nil
}

func (d dominiosFake) Release(_ context.Context, name string, tenantID uuid.UUID) (bool, error) {
	if owner, ok := d.r.domains[name]; ok && owner == tenantID {
		delete(d.r.domains, name)
		return true, nil
	}
	return false, nil
}

func (d dominiosFake) ReleaseTenant(_ context.Context, tenantID uuid.UUID) (int64, error) {
	var released int64
	for name, owner := range d.r.domains {
		if owner == tenantID {
			delete(d.r.domains, name)
			released++
		}
	}
	return released, nil
}

func (d dominiosFake) TenantOf(_ context.Context, name string) (uuid.UUID, error) {
	d.r.reads++
	if owner, ok := d.r.domains[name]; ok {
		return owner, nil
	}
	return uuid.Nil, domain.ErrMailDomainNotFound
}

func internalServer(reg *registroFake) http.Handler {
	uc := app.NewOrganizationUseCase(app.Dependencies{Tenants: reg, Cells: celdasFake{reg}, MailDomains: dominiosFake{reg}})
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

// El indice de dominios: domain-service reclama y suelta con la empresa en la ruta, el gateway
// pregunta la celda de un dominio sin saber de que empresa es. Ninguna respuesta lleva la empresa
// duena de un dominio a quien no lo es, ni el host de la base de la celda.
func TestIndiceDeDominiosPorLaAPIInterna(t *testing.T) {
	pe02 := &domain.Cell{ID: uuid.New(), Code: "pe-02", DBHost: "pg-pe-02.internal", DBPort: 5432}
	beta := &domain.Tenant{ID: uuid.New(), Slug: "beta", CellID: pe02.ID, Status: domain.TenantStatusActive}
	otra := &domain.Tenant{ID: uuid.New(), Slug: "otra", CellID: pe02.ID, Status: domain.TenantStatusActive}
	reg := &registroFake{
		tenants: map[uuid.UUID]*domain.Tenant{beta.ID: beta, otra.ID: otra},
		cells:   map[uuid.UUID]*domain.Cell{pe02.ID: pe02},
	}
	srv := internalServer(reg)
	pedir := func(method, path string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/internal/organization"+path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	datos := func(rec *httptest.ResponseRecorder) map[string]string {
		var body struct {
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("cuerpo: %s", rec.Body)
		}
		return body.Data
	}
	reclamo := "/tenants/" + beta.ID.String() + "/mail-domains/beta.test"

	for i := 0; i < 2; i++ {
		rec := pedir(http.MethodPut, reclamo, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("reclamo %d: %d %s", i, rec.Code, rec.Body)
		}
		if d := datos(rec); d["domain"] != "beta.test" || d["tenant_id"] != beta.ID.String() || d["cell_code"] != "pe-02" || len(d) != 3 {
			t.Fatalf("contrato del reclamo: %v", d)
		}
	}
	rec := pedir(http.MethodPut, "/tenants/"+otra.ID.String()+"/mail-domains/beta.test", nil)
	if rec.Code != http.StatusConflict || codigo(rec) != "MAIL_DOMAIN_CLAIMED" || strings.Contains(rec.Body.String(), beta.ID.String()) {
		t.Fatalf("reclamo de otra empresa: %d %s", rec.Code, rec.Body)
	}
	if rec := pedir(http.MethodPut, "/tenants/"+uuid.NewString()+"/mail-domains/gamma.test", nil); rec.Code != http.StatusNotFound || codigo(rec) != "TENANT_NOT_FOUND" {
		t.Fatalf("empresa desconocida: %d %s", rec.Code, rec.Body)
	}
	if rec := pedir(http.MethodPut, "/tenants/no-es-uuid/mail-domains/gamma.test", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empresa invalida: %d", rec.Code)
	}
	if rec := pedir(http.MethodPut, "/tenants/"+beta.ID.String()+"/mail-domains/sin_punto", nil); rec.Code != http.StatusUnprocessableEntity || codigo(rec) != "INVALID_MAIL_DOMAIN" {
		t.Fatalf("dominio invalido: %d %s", rec.Code, rec.Body)
	}

	rec = pedir(http.MethodGet, "/mail-domains/beta.test/cell", nil)
	if d := datos(rec); rec.Code != http.StatusOK || d["domain"] != "beta.test" || d["cell_code"] != "pe-02" || len(d) != 2 {
		t.Fatalf("celda de beta.test: %d %v", rec.Code, d)
	}
	if strings.Contains(rec.Body.String(), pe02.DBHost) || strings.Contains(rec.Body.String(), beta.ID.String()) {
		t.Fatalf("la celda de un dominio no lleva empresa ni host: %s", rec.Body)
	}
	for _, nombre := range []string{"nadie.test", "sin_punto"} {
		if rec := pedir(http.MethodGet, "/mail-domains/"+nombre+"/cell", nil); rec.Code != http.StatusNotFound || codigo(rec) != "MAIL_DOMAIN_NOT_FOUND" {
			t.Fatalf("%s: %d %s", nombre, rec.Code, rec.Body)
		}
	}

	if rec := pedir(http.MethodDelete, "/tenants/"+otra.ID.String()+"/mail-domains/beta.test", nil); rec.Code != http.StatusNoContent || reg.domains["beta.test"] != beta.ID {
		t.Fatalf("otra empresa suelta beta.test: %d %v", rec.Code, reg.domains)
	}

	// Una persona, aunque el gateway la haya autenticado, no toca el indice.
	persona := map[string]string{"X-User-ID": uuid.NewString(), "X-Tenant-ID": beta.ID.String()}
	antes := reg.reads
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		if rec := pedir(method, reclamo, persona); rec.Code != http.StatusForbidden {
			t.Fatalf("%s con usuario: %d", method, rec.Code)
		}
	}
	if rec := pedir(http.MethodGet, "/mail-domains/beta.test/cell", persona); rec.Code != http.StatusForbidden || reg.reads != antes {
		t.Fatalf("consulta con usuario: %d, lecturas %d -> %d", rec.Code, antes, reg.reads)
	}
	if reg.domains["beta.test"] != beta.ID {
		t.Fatalf("indice tras las peticiones con usuario: %v", reg.domains)
	}

	for i := 0; i < 2; i++ {
		if rec := pedir(http.MethodDelete, reclamo, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("beta suelta beta.test (%d): %d", i, rec.Code)
		}
	}
	if rec := pedir(http.MethodGet, "/mail-domains/beta.test/cell", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("dominio soltado: %d", rec.Code)
	}
}

// Una empresa con la baja en curso no reclama dominios: 409 con su propio codigo, distinto del de
// un dominio de otra empresa, y el indice no cambia.
func TestUnaEmpresaEnBajaNoReclamaDominios(t *testing.T) {
	pe02 := &domain.Cell{ID: uuid.New(), Code: "pe-02"}
	beta := &domain.Tenant{ID: uuid.New(), Slug: "beta", CellID: pe02.ID, Status: domain.TenantStatusInactive}
	reg := &registroFake{
		tenants:  map[uuid.UUID]*domain.Tenant{beta.ID: beta},
		cells:    map[uuid.UUID]*domain.Cell{pe02.ID: pe02},
		removing: map[uuid.UUID]bool{beta.ID: true},
	}
	req := httptest.NewRequest(http.MethodPut, "/internal/organization/tenants/"+beta.ID.String()+"/mail-domains/beta.test", nil)
	rec := httptest.NewRecorder()
	internalServer(reg).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || codigo(rec) != "TENANT_BEING_REMOVED" || len(reg.domains) != 0 {
		t.Fatalf("reclamo durante la baja: %d %s, indice %v", rec.Code, rec.Body, reg.domains)
	}
}

func codigo(rec *httptest.ResponseRecorder) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Error.Code
}
