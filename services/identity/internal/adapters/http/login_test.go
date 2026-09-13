package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// loginNow es el reloj fijo del caso de uso: ninguna respuesta depende de la fecha real.
var loginNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

const (
	loginPassword = "Correcta-2026!"
	loginSlug     = "acme"
)

type lgUsers struct {
	ports.UserRepository
	users      map[uuid.UUID]*domain.User
	increments int
	deleteErr  error
}

func (s *lgUsers) GetByEmail(_ context.Context, tenant uuid.UUID, email string) (*domain.User, error) {
	for _, u := range s.users {
		if u.TenantID == tenant && u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, domain.ErrUserNotFound
}

func (s *lgUsers) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if u, ok := s.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, domain.ErrUserNotFound
}

func (s *lgUsers) IncrementFailedAttempts(context.Context, uuid.UUID) error {
	s.increments++
	return nil
}
func (s *lgUsers) ResetFailedAttempts(context.Context, uuid.UUID) error  { return nil }
func (s *lgUsers) UpdateLastLogin(context.Context, uuid.UUID) error      { return nil }
func (s *lgUsers) LockUser(context.Context, uuid.UUID, *time.Time) error { return nil }
func (s *lgUsers) Delete(_ context.Context, id uuid.UUID) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.users, id)
	return nil
}

// lgTenants resuelve la empresa solo por slug: sin slug, el correo no resuelve ninguna, que
// es lo que responde el registro a un correo desconocido (o de una cuenta inactive o pending).
type lgTenants struct{ id uuid.UUID }

func (t lgTenants) GetIDBySlug(_ context.Context, slug string) (uuid.UUID, error) {
	if slug == loginSlug {
		return t.id, nil
	}
	return uuid.Nil, domain.ErrTenantNotFound
}
func (t lgTenants) GetIDByEmail(context.Context, string) (uuid.UUID, error) {
	return uuid.Nil, domain.ErrTenantNotFound
}
func (t lgTenants) IsActive(context.Context, uuid.UUID) (bool, error) { return true, nil }

type lgSessions struct{ ports.SessionRepository }

func (lgSessions) Create(context.Context, *domain.Session) error { return nil }

type lgPolicies struct{}

func (lgPolicies) Get(_ context.Context, tenant uuid.UUID) (*domain.PasswordPolicy, error) {
	return domain.DefaultPasswordPolicy(tenant), nil
}
func (lgPolicies) Upsert(context.Context, *domain.PasswordPolicy) error { return nil }

type lgAudit struct{}

func (lgAudit) Log(context.Context, *domain.AuditEntry) error { return nil }

type lgEvents struct{}

func (lgEvents) PublishUserCreated(string, string, string) error                    { return nil }
func (lgEvents) PublishUserLoggedIn(string, string, string, string) error           { return nil }
func (lgEvents) PublishUserLoggedOut(string, string) error                          { return nil }
func (lgEvents) PublishUserLocked(string, string) error                             { return nil }
func (lgEvents) PublishLoginFailed(string, string, string, string, string) error    { return nil }
func (lgEvents) PublishSessionRevoked(string, string, string, string, string) error { return nil }
func (lgEvents) PublishPasswordChanged(string, string) error                        { return nil }

type lgTx struct{}

func (lgTx) Transact(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type lgDeletions struct{ got []domain.UserDeletion }

func (d *lgDeletions) UserDeleted(_ context.Context, del domain.UserDeletion) error {
	d.got = append(d.got, del)
	return nil
}

type lgFixture struct {
	srv       http.Handler
	users     *lgUsers
	deletions *lgDeletions
	tokens    *auth.TokenService
	tenant    uuid.UUID
	hash      string
}

func newLoginFixture(t *testing.T) *lgFixture {
	t.Helper()
	t.Setenv("STEP_UP_MODE", "")
	signer, err := auth.NewEphemeralSigner()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := auth.IssuerKeySet(signer, "")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewVerifier(keys)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(loginPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	f := &lgFixture{
		users:     &lgUsers{users: map[uuid.UUID]*domain.User{}},
		deletions: &lgDeletions{},
		tokens:    auth.NewTokenService(signer, verifier, 5*time.Minute, time.Hour),
		tenant:    uuid.New(),
		hash:      string(hash),
	}
	authUC := app.NewAuthUseCase(app.AuthDeps{
		Users: f.users, Sessions: lgSessions{}, Policies: lgPolicies{}, Audit: lgAudit{}, Events: lgEvents{},
		Tokens: f.tokens, Tenants: lgTenants{id: f.tenant}, Roles: &roleStore{}, Logger: zap.NewNop(),
		Now: func() time.Time { return loginNow },
	})
	userUC := app.NewUserUseCase(app.UserDeps{Users: f.users, Tx: lgTx{}, AccountEvents: f.deletions, Logger: zap.NewNop()})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(authUC, userUC, nil, authz.NewChecker(unreachable, ""), Config{}).Routes())
	f.srv = r
	return f
}

func (f *lgFixture) account(status domain.UserStatus, lockedUntil *time.Time) *domain.User {
	u := &domain.User{
		ID: uuid.New(), TenantID: f.tenant, Email: uuid.NewString()[:8] + "@example.test",
		PasswordHash: f.hash, Status: status, LockedUntil: lockedUntil,
	}
	f.users.users[u.ID] = u
	return u
}

func (f *lgFixture) post(t *testing.T, path string, body map[string]string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *lgFixture) login(t *testing.T, email, password, slug string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]string{"email": email, "password": password}
	if slug != "" {
		body["tenant_slug"] = slug
	}
	return f.post(t, "/api/v1/auth/login", body, nil)
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("respuesta ilegible %q: %v", rec.Body.String(), err)
	}
	return env.Error.Code
}

// Contrasena mala, correo desconocido en la empresa y correo que no resuelve empresa se
// responden byte a byte igual: el inicio de sesion no dice si la cuenta existe.
func TestLoginNoDistingueCuentaInexistenteDeContrasenaMala(t *testing.T) {
	f := newLoginFixture(t)
	active := f.account(domain.UserStatusActive, nil)
	wrong := f.login(t, active.Email, "no-es-la-contrasena", loginSlug)
	missing := f.login(t, "nadie@example.test", "no-es-la-contrasena", loginSlug)
	noTenant := f.login(t, "nadie@example.test", "no-es-la-contrasena", "")
	if wrong.Code != http.StatusUnauthorized || errCode(t, wrong) != "UNAUTHORIZED" {
		t.Fatalf("contrasena mala: %d %s", wrong.Code, wrong.Body.String())
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{"correo desconocido": missing, "sin empresa": noTenant} {
		if rec.Code != wrong.Code || rec.Body.String() != wrong.Body.String() {
			t.Errorf("%s: %d %q; la contrasena mala da %d %q", name, rec.Code, rec.Body.String(), wrong.Code, wrong.Body.String())
		}
	}
}

// pending responde lo mismo que inactive, y los dos, como un bloqueo vigente, antes de mirar la
// contrasena: con la buena o con una mala, sin contar intentos.
func TestLoginDeCuentasSinSesion(t *testing.T) {
	f := newLoginFixture(t)
	until := loginNow.Add(time.Minute)
	cases := []struct {
		name string
		user *domain.User
		code string
	}{
		{"inactiva", f.account(domain.UserStatusInactive, nil), "ACCOUNT_INACTIVE"},
		{"pendiente", f.account(domain.UserStatusPending, nil), "ACCOUNT_INACTIVE"},
		{"bloqueo vigente", f.account(domain.UserStatusLocked, &until), "ACCOUNT_LOCKED"},
	}
	bodies := map[string]string{}
	for _, c := range cases {
		for _, password := range []string{loginPassword, "no-es-la-contrasena"} {
			rec := f.login(t, c.user.Email, password, loginSlug)
			if rec.Code != http.StatusForbidden || errCode(t, rec) != c.code {
				t.Errorf("%s con contrasena %q: %d %s, se esperaba 403 %s", c.name, password, rec.Code, rec.Body.String(), c.code)
			}
			bodies[c.name] = rec.Body.String()
		}
	}
	if bodies["pendiente"] != bodies["inactiva"] {
		t.Errorf("pendiente %q e inactiva %q deben responder igual", bodies["pendiente"], bodies["inactiva"])
	}
	if f.users.increments != 0 {
		t.Errorf("%d intentos fallidos contados, se esperaba ninguno", f.users.increments)
	}
}

func TestLoginConElBloqueoVencidoEntra(t *testing.T) {
	f := newLoginFixture(t)
	until := loginNow.Add(-time.Minute)
	rec := f.login(t, f.account(domain.UserStatusLocked, &until).Email, loginPassword, loginSlug)
	var env struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &env) != nil || env.Data.AccessToken == "" {
		t.Fatalf("bloqueo vencido: %d %s, se esperaba sesion", rec.Code, rec.Body.String())
	}
}

// Quien presenta el reto ya probo la contrasena: si su cuenta dejo de estar activa se le dice.
func TestElSegundoFactorDeUnaCuentaPendiente(t *testing.T) {
	f := newLoginFixture(t)
	u := f.account(domain.UserStatusPending, nil)
	u.MFAEnabled, u.MFASecret = true, "JBSWY3DPEHPK3PXP"
	challenge, err := f.tokens.GenerateMFAChallenge(u.ID.String(), f.tenant.String())
	if err != nil {
		t.Fatal(err)
	}
	rec := f.post(t, "/api/v1/auth/mfa/challenge", map[string]string{"mfa_token": challenge, "code": "123456"}, nil)
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "ACCOUNT_INACTIVE" {
		t.Fatalf("reto de una cuenta pendiente: %d %s, se esperaba 403 ACCOUNT_INACTIVE", rec.Code, rec.Body.String())
	}
}

func TestBorrarUnaCuentaAnunciaLaBaja(t *testing.T) {
	f := newLoginFixture(t)
	admin := f.account(domain.UserStatusActive, nil)
	del := func(target uuid.UUID) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+target.String(), nil)
		req.Header.Set("X-User-ID", admin.ID.String())
		req.Header.Set("X-Tenant-ID", f.tenant.String())
		req.Header.Set("X-User-Roles", middleware.RoleTenantAdmin)
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)
		return rec.Code
	}

	member := f.account(domain.UserStatusActive, nil)
	if code := del(member.ID); code != http.StatusNoContent {
		t.Fatalf("baja: %d, se esperaba 204", code)
	}
	want := domain.UserDeletion{UserID: member.ID, TenantID: f.tenant, ActorID: admin.ID}
	if len(f.deletions.got) != 1 || f.deletions.got[0].UserID != want.UserID ||
		f.deletions.got[0].TenantID != want.TenantID || f.deletions.got[0].ActorID != want.ActorID {
		t.Fatalf("baja anunciada %+v, se esperaba %+v", f.deletions.got, want)
	}

	// Otra baja gano la carrera: la fila ya no estaba cuando se fue a borrar.
	racing := f.account(domain.UserStatusActive, nil)
	f.users.deleteErr = domain.ErrUserNotFound
	if code := del(racing.ID); code != http.StatusNotFound {
		t.Fatalf("baja simultanea: %d, se esperaba 404", code)
	}
	f.users.deleteErr = errors.New("registro no disponible")
	if code := del(racing.ID); code != http.StatusInternalServerError {
		t.Fatalf("fallo de la base: %d, se esperaba 500", code)
	}
	if len(f.deletions.got) != 1 {
		t.Fatalf("solo la baja que borro se anuncia: %+v", f.deletions.got)
	}
}
