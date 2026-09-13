package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
	"github.com/alonsosss/corforce-email/services/templates/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/google/uuid"
)

// El render interno es el contrato que transactional consume por cada envio: si cambia
// de forma, los correos salen sin asunto o sin cuerpo y nada falla en compilacion.
func TestInternalRenderRespetaElContratoDeTransactional(t *testing.T) {
	uc := app.New(app.Deps{Repo: apptest.NewRepo(), Tx: &apptest.Tx{}, Renderer: &apptest.Renderer{}, Events: &apptest.Events{}})
	tenant, user := uuid.New(), uuid.New()
	ctx := context.Background()
	tpl, _, err := uc.CreateTemplate(ctx, tenant, user, app.CreateTemplateInput{
		Name: "codigo", Kind: domain.KindTransactional,
		Content: domain.Content{Subject: "Codigo", HTML: "<p>{{.name}}</p>", Variables: []domain.Variable{{Name: "name", Type: domain.VarString}}},
	})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if _, err := uc.PublishVersion(ctx, tenant, tpl.ID, 1); err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}

	promo, _, err := uc.CreateTemplate(ctx, tenant, user, app.CreateTemplateInput{
		Name: "otono", Kind: domain.KindMarketing,
		Content: domain.Content{Subject: "Otono", HTML: `<p>{{.name}}</p><a href="{{.unsubscribe_url}}">Baja</a>`, Variables: []domain.Variable{{Name: "name", Type: domain.VarString}}},
	})
	if err != nil {
		t.Fatalf("CreateTemplate marketing: %v", err)
	}
	if _, err := uc.PublishVersion(ctx, tenant, promo.ID, 1); err != nil {
		t.Fatalf("PublishVersion marketing: %v", err)
	}

	routes := NewHandler(uc, nil).InternalRoutes()
	call := func(templateID uuid.UUID, tenantID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/"+templateID.String()+"/render", strings.NewReader(body))
		req = req.WithContext(middleware.WithTenantID(req.Context(), tenantID))
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("JSON invalido: %v", err)
		}
		if _, hasErr := envelope["error"]; hasErr {
			t.Fatalf("una respuesta correcta no lleva error: %s", rec.Body)
		}
		var data map[string]any
		if err := json.Unmarshal(envelope["data"], &data); err != nil {
			t.Fatalf("data invalido: %v", err)
		}
		return data
	}

	const body = `{"variables":{"name":"Ana"},"reserved":{"recipient_email":"ana@acme.test","unsubscribe_url":"https://acme.test/baja"}}`
	data := decode(call(tpl.ID, tenant.String(), body))
	for _, key := range []string{"subject", "html", "text", "version", "kind"} {
		if _, ok := data[key]; !ok {
			t.Errorf("falta %q en data: %v", key, data)
		}
	}
	if data["subject"] != "Codigo|Ana" || data["version"] != float64(1) {
		t.Errorf("render inesperado: %v", data)
	}
	// El tipo real de la plantilla, no lo que se deduzca del cuerpo: una plantilla
	// transaccional puede usar unsubscribe_url y sigue siendo transaccional.
	if data["kind"] != domain.KindTransactional {
		t.Errorf("kind de la plantilla transaccional: %v", data["kind"])
	}
	if got := decode(call(promo.ID, tenant.String(), body)); got["kind"] != domain.KindMarketing {
		t.Errorf("kind de la plantilla de marketing: %v", got["kind"])
	}

	if rec := call(tpl.ID, uuid.New().String(), `{"variables":{"name":"Ana"}}`); rec.Code != http.StatusNotFound {
		t.Errorf("otra empresa no debe ver la plantilla: status %d", rec.Code)
	}
	if rec := call(tpl.ID, tenant.String(), `{"variables":{"name":"Ana"},"reserved":{"otra":"x"}}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("una variable reservada desconocida debe rechazarse: status %d", rec.Code)
	}
}
