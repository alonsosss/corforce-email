package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

const unreachableAccessControl = "http://127.0.0.1:1"

func metaRequest(roles []string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/meta", nil)
	ctx := middleware.WithTenantID(req.Context(), "8f1b4b1e-6c1e-4f55-9a0c-3d2e1f0a9b7c")
	ctx = context.WithValue(ctx, middleware.CtxUserID, "5d0c9e7a-2b4f-4c1d-8e6a-1f2b3c4d5e6f")
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	return req.WithContext(ctx)
}

// El catalogo sustituye a las constantes que la interfaz copiaba: tipos, estados,
// variables reservadas y topes salen del dominio. /meta no debe caer en /{id}.
func TestMetaPublicaElCatalogoDelDominio(t *testing.T) {
	routes := NewHandler(nil, authz.NewChecker(unreachableAccessControl, "test")).Routes()
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
	checks := []struct {
		name      string
		got, want []string
	}{
		{"kinds", meta.Kinds, domain.Kinds()},
		{"statuses", meta.Statuses, domain.TemplateStatuses()},
		{"version_statuses", meta.VersionStatuses, domain.VersionStatuses()},
		{"variable_types", meta.VariableTypes, domain.VariableTypes()},
		{"field_types", meta.FieldTypes, domain.FieldTypes()},
	}
	for _, c := range checks {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s: %v, dominio %v", c.name, c.got, c.want)
		}
	}
	reserved := domain.ReservedVariables()
	if len(meta.ReservedVariables) != len(reserved) {
		t.Fatalf("reservadas %v, dominio %v", meta.ReservedVariables, reserved)
	}
	for i, r := range reserved {
		if meta.ReservedVariables[i].Name != r.Name || meta.ReservedVariables[i].Type != r.Type {
			t.Errorf("reservada %d: %+v, dominio %+v", i, meta.ReservedVariables[i], r)
		}
	}
	want := limitsMeta{
		MaxNameLength: domain.MaxNameLength, MaxDescriptionLength: domain.MaxDescription,
		MaxVariables: domain.MaxVariables, MaxSubjectBytes: domain.MaxSubjectBytes, MaxHTMLBytes: domain.MaxHTMLBytes,
		MaxEditorBytes: domain.MaxEditorBytes, MaxBrandColors: domain.MaxBrandColors, MaxBrandFonts: domain.MaxBrandFonts,
		MaxAssetBytes: domain.MaxAssetBytes, MaxAssetDimension: domain.MaxAssetDimension,
		MaxTestRecipients: domain.MaxTestRecipients,
		MaxListFields:     domain.MaxListFields, MaxListItems: domain.MaxListItems,
	}
	if !slices.Equal(meta.EditorKinds, domain.EditorKinds()) || !slices.Equal(meta.AssetContentTypes, domain.AssetContentTypes()) ||
		!slices.Equal(meta.BrandFonts, domain.BrandFonts()) {
		t.Errorf("catalogo del editor: %v %v %v", meta.EditorKinds, meta.AssetContentTypes, meta.BrandFonts)
	}
	if meta.Limits != want {
		t.Errorf("limites %+v, dominio %+v", meta.Limits, want)
	}
}

func TestMetaExigeElPermisoDeLectura(t *testing.T) {
	routes := NewHandler(nil, authz.NewChecker(unreachableAccessControl, "test")).Routes()
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, metaRequest([]string{"editor"}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sin politica comprobable debe fallar cerrado: status %d", rec.Code)
	}
}
