package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/passwordhash"
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

// ListLoginCandidates devuelve lo que devolveria la sentencia real: las cuentas del correo que
// pueden tener sesion, en orden estable por alta, hasta el tope.
func (s *lgUsers) ListLoginCandidates(_ context.Context, email string, limit int) ([]*domain.User, error) {
	var out []*domain.User
	for _, u := range s.users {
		if u.Email != email || (u.Status != domain.UserStatusActive && u.Status != domain.UserStatusLocked) {
			continue
		}
		cp := *u
		out = append(out, &cp)
	}
	slices.SortFunc(out, func(a, b *domain.User) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *lgUsers) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if u, ok := s.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, domain.ErrUserNotFound
}

// IncrementFailedAttempts y LockUser dejan en la cuenta lo que dejaria la base, con la regla del
// dominio.
func (s *lgUsers) IncrementFailedAttempts(_ context.Context, id uuid.UUID, now time.Time) (int, error) {
	s.increments++
	u, ok := s.users[id]
	if !ok {
		return 0, domain.ErrUserNotFound
	}
	u.FailedLoginAttempts = u.Failures().AttemptsAfterFailure(now)
	u.LastFailedLoginAt = &now
	return u.FailedLoginAttempts, nil
}
func (s *lgUsers) ResetFailedAttempts(context.Context, uuid.UUID) error { return nil }
func (s *lgUsers) RecordLogin(context.Context, uuid.UUID, *ports.PasswordRehash) error {
	return nil
}
func (s *lgUsers) LockUser(_ context.Context, id uuid.UUID, until *time.Time) error {
	if u, ok := s.users[id]; ok && (u.Status == domain.UserStatusActive || u.Status == domain.UserStatusLocked) {
		u.Status, u.LockedUntil = domain.UserStatusLocked, until
	}
	return nil
}

func (s *lgUsers) Delete(_ context.Context, id uuid.UUID) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.users, id)
	return nil
}

// lgUnknown guarda los contadores de los correos sin cuenta con la regla del dominio.
type lgUnknown struct {
	counters map[string]domain.LoginFailures
}

func (s *lgUnknown) RecordFailure(_ context.Context, subject string, now time.Time, maxAttempts int, lockout time.Duration) (bool, error) {
	if s.counters == nil {
		s.counters = map[string]domain.LoginFailures{}
	}
	next, wasLocked := s.counters[subject].RecordFailure(now, maxAttempts, lockout)
	s.counters[subject] = next
	return wasLocked, nil
}
func (s *lgUnknown) PruneForgotten(context.Context, time.Time, int) (int64, error) { return 0, nil }

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

// testHasher es el hasher de identity con el coste minimo: estas pruebas no miden tiempos.
func testHasher(t *testing.T) *passwordhash.Bcrypt {
	t.Helper()
	h, err := passwordhash.NewBcrypt(bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return h
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
	authUC, err := app.NewAuthUseCase(app.AuthDeps{
		Users: f.users, Sessions: lgSessions{}, Policies: lgPolicies{}, Audit: lgAudit{}, Events: lgEvents{},
		Tokens: f.tokens, Tenants: lgTenants{id: f.tenant}, Roles: &roleStore{}, Hasher: testHasher(t),
		UnknownLogins: &lgUnknown{}, Sealer: testSealer(t), Logger: zap.NewNop(), Now: func() time.Time { return loginNow },
	})
	if err != nil {
		t.Fatal(err)
	}
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

// Un correo sin cuenta llega al bloqueo tras los mismos intentos que una cuenta real y con la
// misma respuesta, byte a byte, busque con la empresa, sin ella o con un slug que no existe: el
// 403 ACCOUNT_LOCKED ya no confirma que la cuenta exista.
func TestUnCorreoSinCuentaLlegaAlBloqueoComoUnaCuenta(t *testing.T) {
	maxAttempts := domain.DefaultPasswordPolicy(uuid.Nil).MaxFailedAttempts
	attempts := func(f *lgFixture, email, slug string) []string {
		out := make([]string, 0, maxAttempts+2)
		for range maxAttempts + 2 {
			rec := f.login(t, email, "no-es-la-contrasena", slug)
			out = append(out, fmt.Sprintf("%d %s", rec.Code, rec.Body.String()))
		}
		return out
	}
	known := newLoginFixture(t)
	want := attempts(known, known.account(domain.UserStatusActive, nil).Email, loginSlug)
	locked := fmt.Sprintf("%d ", http.StatusForbidden)
	for i, got := range want {
		if (i < maxAttempts) == strings.HasPrefix(got, locked) || (i >= maxAttempts && !strings.Contains(got, `"ACCOUNT_LOCKED"`)) {
			t.Fatalf("cuenta real, intento %d: %s", i+1, got)
		}
	}
	for name, slug := range map[string]string{"en la empresa": loginSlug, "sin empresa": "", "con un slug que no existe": "no-existe"} {
		if got := attempts(newLoginFixture(t), "nadie@example.test", slug); !slices.Equal(got, want) {
			t.Errorf("correo sin cuenta %s:\n%v\nla cuenta real da\n%v", name, got, want)
		}
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
	u.MFAEnabled, u.MFASecretLegacy = true, "JBSWY3DPEHPK3PXP"
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

// testSealer cifra el secreto del segundo factor con una llave de la prueba, como
// MAIL_ENCRYPTION_KEY en produccion.
func testSealer(t *testing.T) *crypto.KeyRing {
	t.Helper()
	t.Setenv("IDENTITY_TEST_KEY", strings.Repeat("3c", 32))
	kr, err := crypto.LoadKeyRing("IDENTITY_TEST_KEY", "IDENTITY_TEST_KEYS_OLD")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}
