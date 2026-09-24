package http

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	hmacadapter "github.com/alonsosss/corforce-email/services/access-control/internal/adapters/hmac"
	"github.com/alonsosss/corforce-email/services/access-control/internal/app"
	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var sendPerm = &domain.Permission{ID: uuid.New(), Module: "transactional", Resource: "messages", Action: "create",
	Description: "Enviar correo transaccional", Scope: domain.PermissionScopeTenant}

// memKeys es un repositorio de claves en memoria para las rutas.
type memKeys struct{ keys map[uuid.UUID]*domain.APIKey }

func (m *memKeys) Create(_ context.Context, k *domain.APIKey, _ domain.APIKeyEvent) error {
	m.keys[k.ID] = k
	return nil
}
func (m *memKeys) List(_ context.Context, tenantID uuid.UUID) ([]*domain.APIKey, error) {
	out := []*domain.APIKey{}
	for _, k := range m.keys {
		if k.TenantID == tenantID {
			out = append(out, k)
		}
	}
	return out, nil
}
func (m *memKeys) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.APIKey, error) {
	if k, ok := m.keys[id]; ok && k.TenantID == tenantID {
		return k, nil
	}
	return nil, domain.ErrAPIKeyNotFound
}
func (m *memKeys) GetByPrefix(_ context.Context, prefix string) (*domain.APIKey, error) {
	for _, k := range m.keys {
		if k.Prefix == prefix {
			return k, nil
		}
	}
	return nil, domain.ErrAPIKeyNotFound
}
func (m *memKeys) CountActive(context.Context, uuid.UUID, time.Time) (int, error) { return 0, nil }
func (m *memKeys) Revoke(ctx context.Context, tenantID, id, actor uuid.UUID, at time.Time, _ func(*domain.APIKey) domain.APIKeyEvent) (*domain.APIKey, error) {
	k, err := m.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if k.RevokedAt != nil {
		return nil, domain.ErrAPIKeyRevoked
	}
	k.RevokedAt, k.RevokedBy = &at, &actor
	return k, nil
}
func (m *memKeys) TouchUsage(context.Context, uuid.UUID, time.Time, string, time.Duration) error {
	return nil
}
func (m *memKeys) Rehash(context.Context, uuid.UUID, []byte, string) error { return nil }
func (m *memKeys) GrantablePermissions(context.Context) ([]*domain.Permission, error) {
	return []*domain.Permission{sendPerm}, nil
}

// adminUsers: el creador es tenant_admin y su cuenta esta activa.
type adminUsers struct{ ports.UserRoleRepository }

func (adminUsers) UserAccount(context.Context, uuid.UUID, uuid.UUID) (domain.UserAccount, error) {
	return domain.UserAccount{Status: "active"}, nil
}
func (adminUsers) ListRoles(context.Context, uuid.UUID, uuid.UUID) ([]*domain.Role, error) {
	return []*domain.Role{{Name: middleware.RoleTenantAdmin}}, nil
}
func (adminUsers) GetAccessPolicy(context.Context, uuid.UUID, uuid.UUID) (*domain.AccessPolicy, error) {
	return &domain.AccessPolicy{}, nil
}

type activeTenants struct{}

func (activeTenants) IsActive(context.Context, uuid.UUID) (bool, error) { return true, nil }

func newKeysServer(t *testing.T, smtp SMTPSettings) (http.Handler, *memKeys) {
	t.Helper()
	t.Setenv("API_KEY_HASH_KEY", strings.Repeat("ab", 32))
	t.Setenv("API_KEY_HASH_KEYS_OLD", "")
	ring, err := crypto.LoadMACKeyRing("API_KEY_HASH_KEY", "API_KEY_HASH_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	keys := &memKeys{keys: map[uuid.UUID]*domain.APIKey{}}
	system := app.SystemRoles{Superadmin: middleware.RoleSuperadmin, TenantAdmin: middleware.RoleTenantAdmin}
	uc := app.NewAPIKeysUseCase(app.APIKeysDeps{
		Keys: keys, Hasher: hmacadapter.New(ring), Users: adminUsers{}, Tenants: activeTenants{},
		SystemRoles: system, Random: rand.Reader, Logger: zap.NewNop(),
	})
	rbac := app.NewRBACUseCase(nil, nil, nil, &policyRepo{}, nil, nil, system, nil, zap.NewNop())
	h := NewAPIKeysHandler(uc, rbac, nil, smtp)
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/api/v1/access/api-keys", h.Routes())
	r.Mount("/internal/access-control/api-keys", h.InternalRoutes())
	return r, keys
}

func resolve(t *testing.T, srv http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "client_ip": "198.51.100.7"})
	return (caller{}).do(t, srv, http.MethodPost, "/internal/access-control/api-keys/resolve", string(body))
}

func TestCicloDeUnaClavePorLasRutas(t *testing.T) {
	srv, _ := newKeysServer(t, SMTPSettings{})
	admin := caller{user: uuid.New(), tenant: uuid.New(), roles: middleware.RoleTenantAdmin}

	rec := admin.do(t, srv, http.MethodPost, "/api/v1/access/api-keys/",
		`{"name":"Tienda","scopes":[{"module":"transactional","resource":"messages","action":"create"}]}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("alta: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			Key    apiKeyDTO `json:"key"`
			Secret string    `json:"secret"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || !strings.HasPrefix(created.Data.Secret, "cfm_"+created.Data.Key.Prefix+"_") {
		t.Fatalf("respuesta del alta: %s", rec.Body.String())
	}

	list := admin.do(t, srv, http.MethodGet, "/api/v1/access/api-keys/", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), created.Data.Secret) || strings.Contains(list.Body.String(), "hash") {
		t.Fatalf("la lista nunca lleva el secreto: %s", list.Body.String())
	}

	res := resolve(t, srv, created.Data.Secret)
	var resolved struct {
		Data resolvedDTO `json:"data"`
	}
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &resolved) != nil || resolved.Data.TenantID != admin.tenant ||
		len(resolved.Data.Scopes) != 1 || resolved.Data.Scopes[0].Action != "create" {
		t.Fatalf("resolucion: %d %s", res.Code, res.Body.String())
	}
	if rec := resolve(t, srv, created.Data.Secret+"x"); rec.Code != http.StatusUnauthorized || errorCode(t, rec) != apiKeyInvalidCode {
		t.Fatalf("clave alterada: %d %s", rec.Code, rec.Body.String())
	}

	rev := admin.do(t, srv, http.MethodPost, "/api/v1/access/api-keys/"+created.Data.Key.ID.String()+"/revoke", "")
	if rev.Code != http.StatusOK || !strings.Contains(rev.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revocacion: %d %s", rev.Code, rev.Body.String())
	}
	if rec := resolve(t, srv, created.Data.Secret); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revocada: %d", rec.Code)
	}
	if rec := admin.do(t, srv, http.MethodPost, "/api/v1/access/api-keys/"+created.Data.Key.ID.String()+"/revoke", ""); rec.Code != http.StatusConflict {
		t.Fatalf("revocar dos veces: %d", rec.Code)
	}
	other := caller{user: uuid.New(), tenant: uuid.New(), roles: middleware.RoleTenantAdmin}
	if rec := other.do(t, srv, http.MethodPost, "/api/v1/access/api-keys/"+created.Data.Key.ID.String()+"/revoke", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("la clave de otra empresa no existe para ella: %d", rec.Code)
	}
}

func TestAltaDeClaveRechazada(t *testing.T) {
	srv, _ := newKeysServer(t, SMTPSettings{})
	admin := caller{user: uuid.New(), tenant: uuid.New(), roles: middleware.RoleTenantAdmin}
	for body, want := range map[string]int{
		`{"name":"","scopes":[{"module":"transactional","resource":"messages","action":"create"}]}`: http.StatusUnprocessableEntity,
		`{"name":"x","scopes":[{"module":"access","resource":"roles","action":"create"}]}`:          http.StatusUnprocessableEntity,
		`{"name":"x","scopes":[]}`: http.StatusUnprocessableEntity,
		`{"name":`:                 http.StatusBadRequest,
	} {
		if rec := admin.do(t, srv, http.MethodPost, "/api/v1/access/api-keys/", body); rec.Code != want {
			t.Errorf("%s: %d, se esperaba %d", body, rec.Code, want)
		}
	}
	// Sin el permiso de crear, 403 antes de leer el cuerpo.
	user := caller{user: uuid.New(), tenant: uuid.New(), roles: "operador"}
	if rec := user.do(t, srv, http.MethodPost, "/api/v1/access/api-keys/", `{`); rec.Code != http.StatusForbidden {
		t.Fatalf("sin permiso: %d", rec.Code)
	}
}

// Una clave de API no gestiona claves ni resuelve claves, aunque llegara hasta aqui.
func TestUnaClaveNoGestionaClaves(t *testing.T) {
	srv, _ := newKeysServer(t, SMTPSettings{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/access/api-keys/"},
		{http.MethodPost, "/api/v1/access/api-keys/"},
		{http.MethodPost, "/internal/access-control/api-keys/resolve"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		req.Header.Set(middleware.HeaderAPIKeyID, uuid.NewString())
		req.Header.Set(middleware.HeaderAPIKeyScopes, "access:api_keys:create")
		req.Header.Set("X-Tenant-ID", uuid.NewString())
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s con clave: %d", tc.method, tc.path, rec.Code)
		}
	}
	// Una persona tampoco llega a la resolucion interna.
	person := caller{user: uuid.New(), tenant: uuid.New(), roles: middleware.RoleTenantAdmin}
	if rec := person.do(t, srv, http.MethodPost, "/internal/access-control/api-keys/resolve", `{"token":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("persona en la ruta interna: %d", rec.Code)
	}
}

func TestAjustesYAlcances(t *testing.T) {
	srv, _ := newKeysServer(t, SMTPSettings{Host: "smtp.ejemplo.test", StartTLSPort: 2525, TLSPort: 2465})
	admin := caller{user: uuid.New(), tenant: uuid.New(), roles: middleware.RoleTenantAdmin}
	rec := admin.do(t, srv, http.MethodGet, "/api/v1/access/api-keys/settings", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"host":"smtp.ejemplo.test"`) {
		t.Fatalf("ajustes: %s", rec.Body.String())
	}
	rec = admin.do(t, srv, http.MethodGet, "/api/v1/access/api-keys/scopes", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"action":"create"`) {
		t.Fatalf("alcances: %s", rec.Body.String())
	}
	none, _ := newKeysServer(t, SMTPSettings{})
	if rec := admin.do(t, none, http.MethodGet, "/api/v1/access/api-keys/settings", ""); !strings.Contains(rec.Body.String(), `"smtp":null`) {
		t.Fatalf("sin SMTP: %s", rec.Body.String())
	}
}
