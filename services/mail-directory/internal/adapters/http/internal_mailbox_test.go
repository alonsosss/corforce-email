package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func doInternalMailbox(h http.Handler, id, tenantID, userID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/internal/mail-directory/mailboxes/"+id, nil)
	if tenantID != "" {
		req.Header.Set("X-Tenant-ID", tenantID)
	}
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	middleware.InjectFromGateway(h).ServeHTTP(rec, req)
	return rec
}

func TestBuzonInternoDevuelveLoMinimoDeLaEmpresaDeLaPeticion(t *testing.T) {
	h, _, _, m := vacationServer(t)
	m.Active = domain.ActiveOn
	rec := doInternalMailbox(h, m.ID.String(), m.TenantID.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data["username"] != m.Username || env.Data["id"] != m.ID.String() || env.Data["active"] != float64(domain.ActiveOn) {
		t.Fatalf("respuesta: %v", env.Data)
	}
	if len(env.Data) != 4 {
		t.Fatalf("la respuesta expone mas de lo necesario: %v", env.Data)
	}
}

func TestBuzonInternoNoSirveElDeOtraEmpresa(t *testing.T) {
	h, _, _, m := vacationServer(t)
	if rec := doInternalMailbox(h, m.ID.String(), uuid.NewString(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("otra empresa: %d", rec.Code)
	}
	if rec := doInternalMailbox(h, m.ID.String(), "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin empresa: %d", rec.Code)
	}
	if rec := doInternalMailbox(h, "no-es-un-id", m.TenantID.String(), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("id malformado: %d", rec.Code)
	}
}

func TestBuzonInternoCierraLaRutaALasPersonas(t *testing.T) {
	h, _, _, m := vacationServer(t)
	if rec := doInternalMailbox(h, m.ID.String(), m.TenantID.String(), uuid.NewString()); rec.Code != http.StatusForbidden {
		t.Fatalf("con usuario: %d", rec.Code)
	}
}
