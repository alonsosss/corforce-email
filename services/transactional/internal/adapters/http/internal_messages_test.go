package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/transactional/internal/app"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Las rutas se prueban sin base: cada caso falla en una validacion anterior a la primera
// consulta, y eso basta para fijar el contrato.
func TestInternalCreateMessagesContract(t *testing.T) {
	ts := newTestServer(t, "")
	h := NewHandler(Deps{UC: app.New(app.Deps{Links: ts.links, Logger: zap.NewNop()}), Perms: allowAll{}, Logger: zap.NewNop()})
	call := func(tenant, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(nethttp.MethodPost, "/internal/transactional/messages", strings.NewReader(body))
		if tenant != "" {
			req.Header.Set("X-Tenant-ID", tenant)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		h.InternalCreateMessages(rec, req)
		return rec
	}
	valid := `{"from":{"email":"no-reply@shop.example.com","name":"Tienda"},"to":[{"email":"ana@example.com","name":"Ana"}],` +
		`"template_id":"` + uuid.New().String() + `","variables":{"confirm_url":"https://app.example.com/c"},"purpose":"double_opt_in"}`
	tenant := uuid.New().String()

	if rec := call("", "doi:1", valid); rec.Code != nethttp.StatusUnauthorized {
		t.Fatalf("sin X-Tenant-ID: %d", rec.Code)
	}
	rec := call(tenant, "", valid)
	var body errorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != nethttp.StatusUnprocessableEntity || !strings.Contains(body.Error.Message, "Idempotency-Key") {
		t.Fatalf("la clave es obligatoria: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(tenant, "doi:1", strings.Replace(valid, `"double_opt_in"`, `"marketing"`, 1)); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("un proposito desconocido se rechaza: %d", rec.Code)
	}
	two := strings.Replace(valid, `"to":[{"email":"ana@example.com","name":"Ana"}]`, `"to":[{"email":"ana@example.com"},{"email":"eva@example.com"}]`, 1)
	if rec := call(tenant, "doi:1", two); rec.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("el proposito exige un solo destinatario: %d", rec.Code)
	}
	if rec := call(tenant, "doi:1", strings.Replace(valid, `"purpose"`, `"proposito"`, 1)); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("un campo fuera del contrato se rechaza: %d", rec.Code)
	}
}

// El API publico no admite el proposito: relajar la supresion es cosa de la plataforma.
func TestPublicCreateMessagesRejectsPurpose(t *testing.T) {
	ts := newTestServer(t, "")
	h := NewHandler(Deps{UC: app.New(app.Deps{Links: ts.links, Logger: zap.NewNop()}), Perms: allowAll{}, Logger: zap.NewNop()})
	body := `{"from":{"email":"no-reply@shop.example.com"},"to":[{"email":"ana@example.com"}],` +
		`"template_id":"` + uuid.New().String() + `","purpose":"double_opt_in"}`
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/transactional/messages", strings.NewReader(body))
	req = req.WithContext(middleware.WithTenantID(context.Background(), uuid.New().String()))
	rec := httptest.NewRecorder()
	h.CreateMessages(rec, req)
	if rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("purpose en el API publico: %d %s", rec.Code, rec.Body.String())
	}
}
