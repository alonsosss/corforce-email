package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/suppression/internal/app"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
)

// unreachableAccessControl hace que un usuario sin rol del sistema no pueda comprobarse:
// la ruta debe fallar cerrada en lugar de servir el catalogo.
const unreachableAccessControl = "http://127.0.0.1:1"

func metaRequest(roles []string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/meta", nil)
	ctx := middleware.WithTenantID(req.Context(), "8f1b4b1e-6c1e-4f55-9a0c-3d2e1f0a9b7c")
	ctx = context.WithValue(ctx, middleware.CtxUserID, "5d0c9e7a-2b4f-4c1d-8e6a-1f2b3c4d5e6f")
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	return req.WithContext(ctx)
}

// El catalogo es el contrato que la interfaz usa en lugar de copiar motivos y topes: si
// cambia de forma, los filtros y los formularios dejan de coincidir con el API.
func TestMetaPublicaMotivosYTopesDelDominio(t *testing.T) {
	routes := NewHandler(nil, authz.NewChecker(unreachableAccessControl, "test")).PublicRoutes()
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, metaRequest([]string{middleware.RoleTenantAdmin}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var envelope struct {
		Data metaResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("JSON invalido: %v", err)
	}
	meta := envelope.Data

	reasons := domain.Reasons()
	if len(meta.Reasons) != len(reasons) {
		t.Fatalf("motivos %v, dominio %v", meta.Reasons, reasons)
	}
	for i, r := range reasons {
		got := meta.Reasons[i]
		if got.Reason != r || got.Severity != r.Severity() || got.Removable != r.Removable() {
			t.Errorf("motivo %d: %+v, dominio %s (gravedad %d, retirable %v)", i, got, r, r.Severity(), r.Removable())
		}
	}
	if len(meta.ManualReasons) != 1 || meta.ManualReasons[0] != string(domain.ReasonManual) {
		t.Errorf("motivos del alta manual: %v", meta.ManualReasons)
	}
	if meta.MaxCheckEmails != app.MaxCheckEmails || meta.MaxImportEmails != app.MaxImportEmails {
		t.Errorf("topes %d/%d, app %d/%d", meta.MaxCheckEmails, meta.MaxImportEmails, app.MaxCheckEmails, app.MaxImportEmails)
	}
	if meta.MaxPerPage != maxPerPage || meta.MaxEmailLength != maxEmailLength || meta.MaxDetailLength != maxDetailLength {
		t.Errorf("topes del handler: %+v", meta)
	}
}

func TestMetaExigeElPermisoDeLectura(t *testing.T) {
	routes := NewHandler(nil, authz.NewChecker(unreachableAccessControl, "test")).PublicRoutes()
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, metaRequest([]string{"operador"}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sin politica comprobable debe fallar cerrado: status %d", rec.Code)
	}
}
