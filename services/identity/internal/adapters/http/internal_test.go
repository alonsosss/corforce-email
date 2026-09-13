package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// tuStore guarda las cuentas en memoria con la regla del repositorio real: el primer
// usuario solo entra en una empresa sin cuentas.
type tuStore struct {
	ports.UserRepository
	users map[uuid.UUID]*domain.User
}

func (s *tuStore) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if u, ok := s.users[id]; ok {
		return u, nil
	}
	return nil, domain.ErrUserNotFound
}

func (s *tuStore) CreateFirst(_ context.Context, u *domain.User) error {
	for _, e := range s.users {
		if e.TenantID == u.TenantID {
			return domain.ErrFirstUserConflict
		}
	}
	c := *u
	s.users[u.ID] = &c
	return nil
}

func (s *tuStore) DeleteByTenant(_ context.Context, tenantID uuid.UUID) (int64, error) {
	var n int64
	for id, u := range s.users {
		if u.TenantID == tenantID {
			delete(s.users, id)
			n++
		}
	}
	return n, nil
}

type tuTenants struct {
	ports.TenantRepository
	active map[uuid.UUID]bool
}

func (t tuTenants) IsActive(_ context.Context, id uuid.UUID) (bool, error) { return t.active[id], nil }

type tuPolicies struct{}

func (tuPolicies) Get(_ context.Context, id uuid.UUID) (*domain.PasswordPolicy, error) {
	return domain.DefaultPasswordPolicy(id), nil
}
func (tuPolicies) Upsert(context.Context, *domain.PasswordPolicy) error { return nil }

type tuBreach map[string]bool

func (b tuBreach) IsBreached(_ context.Context, p string) (bool, error) { return b[p], nil }
func (b tuBreach) Enabled() bool                                        { return true }

type tuAudit struct{ entries []*domain.AuditEntry }

func (a *tuAudit) Log(_ context.Context, e *domain.AuditEntry) error {
	a.entries = append(a.entries, e)
	return nil
}

type tuEvents struct {
	ports.EventPublisher
	created int
}

func (e *tuEvents) PublishUserCreated(string, string, string) error {
	e.created++
	return nil
}

const (
	tuToken    = "token-interno-de-prueba"
	tuPassword = "Primera-Clave-2030!"
	tuBreached = "Filtrada-Clave-1!"
)

type tuFixture struct {
	srv     http.Handler
	store   *tuStore
	tenants tuTenants
	audit   *tuAudit
	events  *tuEvents
}

// newTUFixture monta las rutas internas como en main: token interno en el router y usuario
// inyectado por el gateway, que es lo que las rutas deben rechazar.
func newTUFixture(t *testing.T) *tuFixture {
	t.Helper()
	t.Setenv("INTERNAL_GATEWAY_TOKEN", tuToken)
	f := &tuFixture{
		store:   &tuStore{users: map[uuid.UUID]*domain.User{}},
		tenants: tuTenants{active: map[uuid.UUID]bool{}},
		audit:   &tuAudit{},
		events:  &tuEvents{},
	}
	uc := app.NewTenantUsersUseCase(app.TenantUsersDeps{
		Users: f.store, TenantUsers: f.store, Tenants: f.tenants, Policies: tuPolicies{},
		Breach: tuBreach{tuBreached: true}, Hasher: testHasher(t), Audit: f.audit, Events: f.events, Logger: zap.NewNop(),
	})
	r := chi.NewRouter()
	r.Use(middleware.RequireGatewayToken)
	r.Use(middleware.InjectFromGateway)
	r.Mount("/internal/identity", NewInternalHandler(uc).Routes())
	f.srv = r
	return f
}

func (f *tuFixture) call(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *tuFixture) internal(method, path, body string) *httptest.ResponseRecorder {
	return f.call(method, path, body, map[string]string{"X-Gateway-Token": tuToken})
}

func firstUserPath(tenant uuid.UUID) string {
	return "/internal/identity/tenants/" + tenant.String() + "/first-user"
}

func firstUserBody(userID uuid.UUID, email, password string) string {
	b, _ := json.Marshal(map[string]string{
		"user_id": userID.String(), "email": email, "password": password, "first_name": "Ana", "last_name": "Perez",
	})
	return string(b)
}

func tuCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible %q: %v", rec.Body.String(), err)
	}
	return env.Error.Code
}

func TestRutasInternasDeIdentitySoloParaServicios(t *testing.T) {
	f := newTUFixture(t)
	tenant := uuid.New()
	body := firstUserBody(uuid.New(), "admin@acme.test", tuPassword)
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"sin token", nil, http.StatusUnauthorized},
		{"token equivocado", map[string]string{"X-Gateway-Token": "otro"}, http.StatusUnauthorized},
		{"una persona por el gateway", map[string]string{"X-Gateway-Token": tuToken, "X-User-ID": uuid.NewString(),
			"X-Tenant-ID": tenant.String(), "X-User-Roles": middleware.RoleSuperadmin}, http.StatusForbidden},
	}
	for _, c := range cases {
		if rec := f.call(http.MethodPut, firstUserPath(tenant), body, c.headers); rec.Code != c.want {
			t.Errorf("%s: %d, se esperaba %d", c.name, rec.Code, c.want)
		}
		if rec := f.call(http.MethodDelete, "/internal/identity/tenants/"+tenant.String()+"/users", "", c.headers); rec.Code != c.want {
			t.Errorf("%s (retirar cuentas): %d, se esperaba %d", c.name, rec.Code, c.want)
		}
	}
	if len(f.store.users) != 0 {
		t.Fatal("una peticion rechazada no crea nada")
	}
	if rec := f.internal(http.MethodPut, firstUserPath(tenant), body); rec.Code != http.StatusCreated {
		t.Fatalf("servicio con token: %d %s", rec.Code, rec.Body.String())
	}
}

// El primer usuario nace con el hash de la contrasena, activo y sin roles; repetir la misma
// peticion responde lo mismo sin crear otra cuenta, y la contrasena no sale nunca.
func TestPrimerUsuarioIdempotenteSinDevolverLaContrasena(t *testing.T) {
	f := newTUFixture(t)
	tenant, user := uuid.New(), uuid.New()
	body := firstUserBody(user, "admin@acme.test", tuPassword)

	first := f.internal(http.MethodPut, firstUserPath(tenant), body)
	if first.Code != http.StatusCreated {
		t.Fatalf("alta: %d %s", first.Code, first.Body.String())
	}
	for _, leak := range []string{tuPassword, "$2a$", "password"} {
		if strings.Contains(first.Body.String(), leak) {
			t.Fatalf("la respuesta lleva %q: %s", leak, first.Body.String())
		}
	}
	stored := f.store.users[user]
	if stored == nil || stored.TenantID != tenant || stored.Status != domain.UserStatusActive ||
		bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte(tuPassword)) != nil {
		t.Fatalf("cuenta guardada = %+v", stored)
	}

	second := f.internal(http.MethodPut, firstUserPath(tenant), body)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"created":false`) {
		t.Fatalf("repeticion: %d %s", second.Code, second.Body.String())
	}
	if f.events.created != 1 || len(f.audit.entries) != 1 {
		t.Errorf("eventos %d, apuntes %d; la repeticion no publica ni apunta otra vez", f.events.created, len(f.audit.entries))
	}

	otherPassword := f.internal(http.MethodPut, firstUserPath(tenant), firstUserBody(user, "admin@acme.test", "Otra-Clave-2030!"))
	otherUser := f.internal(http.MethodPut, firstUserPath(tenant), firstUserBody(uuid.New(), "otro@acme.test", tuPassword))
	for name, rec := range map[string]*httptest.ResponseRecorder{"otra contrasena": otherPassword, "otro usuario": otherUser} {
		if rec.Code != http.StatusConflict || tuCode(t, rec) != "FIRST_USER_CONFLICT" {
			t.Errorf("%s: %d %s; se esperaba 409 FIRST_USER_CONFLICT", name, rec.Code, rec.Body.String())
		}
	}
	if len(f.store.users) != 1 {
		t.Fatalf("cuentas = %d; want 1", len(f.store.users))
	}
}

// La contrasena del primer usuario pasa por la misma puerta que cualquier otra: la politica
// de la empresa y las filtraciones.
func TestPrimerUsuarioAplicaLaPoliticaYLasFiltraciones(t *testing.T) {
	f := newTUFixture(t)
	tenant := uuid.New()
	cases := map[string]struct {
		password, code string
	}{
		"sin mayusculas ni simbolos": {"solominusculas12", "PASSWORD_POLICY"},
		"filtrada":                   {tuBreached, "PASSWORD_BREACHED"},
	}
	for name, c := range cases {
		rec := f.internal(http.MethodPut, firstUserPath(tenant), firstUserBody(uuid.New(), "admin@acme.test", c.password))
		if rec.Code != http.StatusUnprocessableEntity || tuCode(t, rec) != c.code {
			t.Errorf("%s: %d %s; se esperaba 422 %s", name, rec.Code, rec.Body.String(), c.code)
		}
		if strings.Contains(rec.Body.String(), c.password) {
			t.Errorf("%s: el rechazo devuelve la contrasena", name)
		}
	}
	if len(f.store.users) != 0 {
		t.Fatal("una contrasena rechazada no crea la cuenta")
	}
}

func TestPrimerUsuarioValidaLaPeticion(t *testing.T) {
	f := newTUFixture(t)
	tenant := uuid.New()
	cases := []struct {
		name, path, body string
		want             int
	}{
		{"empresa no valida", "/internal/identity/tenants/no-es-uuid/first-user", firstUserBody(uuid.New(), "a@b.test", tuPassword), http.StatusBadRequest},
		{"campo desconocido", firstUserPath(tenant), `{"user_id":"` + uuid.NewString() + `","email":"a@b.test","password":"x","first_name":"A","last_name":"B","role":"superadmin"}`, http.StatusBadRequest},
		{"sin user_id", firstUserPath(tenant), `{"email":"a@b.test","password":"` + tuPassword + `","first_name":"A","last_name":"B"}`, http.StatusUnprocessableEntity},
		{"correo no valido", firstUserPath(tenant), firstUserBody(uuid.New(), "no-es-correo", tuPassword), http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		if rec := f.internal(http.MethodPut, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s; se esperaba %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if len(f.store.users) != 0 {
		t.Fatal("una peticion no valida no crea nada")
	}
}

func TestRetirarLasCuentasDeUnaEmpresa(t *testing.T) {
	f := newTUFixture(t)
	tenant, other := uuid.New(), uuid.New()
	f.internal(http.MethodPut, firstUserPath(tenant), firstUserBody(uuid.New(), "admin@acme.test", tuPassword))
	f.internal(http.MethodPut, firstUserPath(other), firstUserBody(uuid.New(), "admin@otra.test", tuPassword))
	path := "/internal/identity/tenants/" + tenant.String() + "/users"

	f.tenants.active[tenant] = true
	if rec := f.internal(http.MethodDelete, path, ""); rec.Code != http.StatusConflict || tuCode(t, rec) != "TENANT_ACTIVE" {
		t.Fatalf("empresa activa: %d %s", rec.Code, rec.Body.String())
	}

	f.tenants.active[tenant] = false
	rec := f.internal(http.MethodDelete, path, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"users_removed":1`) {
		t.Fatalf("retirar: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.store.users) != 1 {
		t.Fatal("solo se retiran las cuentas de esa empresa")
	}
	if last := f.audit.entries[len(f.audit.entries)-1]; last.Action != "remove_tenant_users" || last.TenantID != tenant {
		t.Errorf("apunte = %+v; want remove_tenant_users de la empresa", last)
	}
	if rec := f.internal(http.MethodDelete, path, ""); !strings.Contains(rec.Body.String(), `"users_removed":0`) {
		t.Errorf("repetir no retira nada: %s", rec.Body.String())
	}
}
