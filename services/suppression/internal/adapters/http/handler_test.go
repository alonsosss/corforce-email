package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/suppression/internal/app"
	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
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

// checkRepo sirve solo lo que lee la consulta previa; cualquier otra operacion no esta
// implementada y haria fallar la prueba.
type checkRepo struct {
	ports.EntryRepository
	entries []domain.Entry
}

func (r checkRepo) FindByEmails(_ context.Context, tenantID uuid.UUID, emails []string) ([]domain.Entry, error) {
	var out []domain.Entry
	for _, e := range r.entries {
		if e.TenantID == tenantID && slices.Contains(emails, e.Email) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Contrato de POST /internal/suppression/check que leen contacts (suppressionclient) y
// transactional: email, reason y reasons no cambian; causes anade, para las mismas
// causas de reasons y en su orden, la hora de alta de cada una.
func TestCheckContrato(t *testing.T) {
	tenant := uuid.New()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	baja := time.Date(2026, 9, 10, 8, 30, 0, 123456000, time.UTC)
	manual := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	uc := app.New(app.Deps{
		Entries: checkRepo{entries: []domain.Entry{
			{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonManual, CreatedAt: manual},
			{ID: uuid.New(), TenantID: tenant, Email: "ana@example.com", Reason: domain.ReasonUnsubscribe, CreatedAt: baja},
			{ID: uuid.New(), TenantID: tenant, Email: "caducada@example.com", Reason: domain.ReasonManual, ExpiresAt: &past, CreatedAt: past},
		}},
		Now: func() time.Time { return now },
	})
	req := httptest.NewRequest(http.MethodPost, "/check",
		strings.NewReader(`{"emails":["ana@example.com","caducada@example.com","libre@example.com"]}`))
	req = req.WithContext(middleware.WithTenantID(req.Context(), tenant.String()))
	rec := httptest.NewRecorder()
	NewHandler(uc, nil).InternalRoutes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var envelope struct {
		Data struct {
			Suppressed []map[string]json.RawMessage `json:"suppressed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("JSON invalido: %v", err)
	}
	if len(envelope.Data.Suppressed) != 1 {
		t.Fatalf("solo la direccion con causas vigentes: %s", rec.Body)
	}
	item := envelope.Data.Suppressed[0]
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"causes", "email", "reason", "reasons"}) {
		t.Fatalf("campos: %v", keys)
	}
	if string(item["email"]) != `"ana@example.com"` || string(item["reason"]) != `"unsubscribe"` ||
		string(item["reasons"]) != `["unsubscribe","manual"]` {
		t.Fatalf("campos existentes: %s", rec.Body)
	}
	var causes []map[string]json.RawMessage
	if err := json.Unmarshal(item["causes"], &causes); err != nil || len(causes) != 2 {
		t.Fatalf("causes: %s (%v)", item["causes"], err)
	}
	want := []struct {
		reason string
		at     time.Time
	}{{"unsubscribe", baja}, {"manual", manual}}
	for i, c := range causes {
		if len(c) != 2 || string(c["reason"]) != `"`+want[i].reason+`"` {
			t.Fatalf("causa %d: %v", i, c)
		}
		var at time.Time
		if err := json.Unmarshal(c["created_at"], &at); err != nil || !at.Equal(want[i].at) {
			t.Fatalf("created_at de %s: %s (%v)", want[i].reason, c["created_at"], err)
		}
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
