package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const pemJSON = `-----BEGIN RSA PRIVATE KEY-----\neA==\n-----END RSA PRIVATE KEY-----`

func TestPutDKIMConElJuegoCompletoYConLaFormaAnterior(t *testing.T) {
	dir, store := apptest.NewDirectory(), apptest.NewStore()
	tenant := uuid.New()
	dir.Domains["acme.com"] = tenant
	dir.InactiveDomains["baja.com"] = tenant
	sync := app.NewRedisSync(store, dir, apptest.NewPolicyReader(), zap.NewNop())
	uc := app.NewDKIMUseCase(app.DKIMDeps{Directory: dir, Lock: &apptest.DKIMLock{}, Sync: sync, Tenants: &apptest.Tenants{}, Logger: zap.NewNop()})
	routes := NewHandler(nil, nil, nil, uc, authz.NewCheckerFromEnv()).Routes()

	put := func(name, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/internal/mail-security/dkim/"+name, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(middleware.WithIdentity(req.Context(), "", tenant.String()))
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec
	}
	juego := `{"keys":[{"selector":"nuevo","private_key_pem":"` + pemJSON + `"},{"selector":"anterior","private_key_pem":"` + pemJSON + `"}]}`
	una := `{"selector":"otra","private_key_pem":"` + pemJSON + `"}`

	if rec := put("acme.com", juego); rec.Code != http.StatusNoContent {
		t.Fatalf("juego completo: %d %s", rec.Code, rec.Body)
	}
	if got := store.Hashes[domain.RedisDKIMSelectors]["acme.com"]; got != "anterior" {
		t.Fatalf("firma la ultima del juego: %q", got)
	}
	if rec := put("acme.com", una); rec.Code != http.StatusNoContent || store.Hashes[domain.RedisDKIMSelectors]["acme.com"] != "otra" {
		t.Fatalf("forma anterior: %d %s", rec.Code, rec.Body)
	}
	if rec := put("acme.com", `{"selector":"otra","private_key_pem":"`+pemJSON+`","keys":[]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("las dos formas juntas: %d %s", rec.Code, rec.Body)
	}

	rec := put("baja.com", juego)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusConflict || body.Error.Code != "DKIM_DOMAIN_NOT_ACTIVE" {
		t.Fatalf("dominio que la celda no sirve: %d %s", rec.Code, rec.Body)
	}
	if _, ok := store.Hashes[domain.RedisDKIMSelectors]["baja.com"]; ok {
		t.Fatal("y no se escribe nada")
	}
}
