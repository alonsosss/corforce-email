package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// authNow es el reloj fijo de estas pruebas: ninguna depende de la fecha real.
var authNow = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

const authPassword = "Correcta-2026!"

var authHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte(authPassword), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}()

func authAt(d time.Duration) *time.Time {
	v := authNow.Add(d)
	return &v
}

type authUsers struct {
	ports.UserRepository
	users      map[uuid.UUID]*domain.User
	increments int
	resets     int
	lockedTo   *time.Time
}

func (s *authUsers) GetByEmail(_ context.Context, tenant uuid.UUID, email string) (*domain.User, error) {
	for _, u := range s.users {
		if u.TenantID == tenant && u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, domain.ErrUserNotFound
}

func (s *authUsers) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if u, ok := s.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, domain.ErrUserNotFound
}

func (s *authUsers) IncrementFailedAttempts(context.Context, uuid.UUID) error {
	s.increments++
	return nil
}
func (s *authUsers) ResetFailedAttempts(context.Context, uuid.UUID) error { s.resets++; return nil }
func (s *authUsers) UpdateLastLogin(context.Context, uuid.UUID) error     { return nil }
func (s *authUsers) LockUser(_ context.Context, _ uuid.UUID, until *time.Time) error {
	s.lockedTo = until
	return nil
}

type authTenants struct{ id uuid.UUID }

func (t authTenants) GetIDBySlug(context.Context, string) (uuid.UUID, error)  { return t.id, nil }
func (t authTenants) GetIDByEmail(context.Context, string) (uuid.UUID, error) { return t.id, nil }
func (t authTenants) IsActive(context.Context, uuid.UUID) (bool, error)       { return true, nil }

type authSessions struct {
	ports.SessionRepository
	byHash  map[string]*domain.Session
	created int
}

func (s *authSessions) Create(context.Context, *domain.Session) error { s.created++; return nil }
func (s *authSessions) GetByRefreshTokenHash(_ context.Context, hash string) (*domain.Session, error) {
	if sess, ok := s.byHash[hash]; ok {
		cp := *sess
		return &cp, nil
	}
	return nil, domain.ErrSessionNotFound
}
func (s *authSessions) RevokeForRotation(context.Context, uuid.UUID) error { return nil }
func (s *authSessions) Revoke(context.Context, uuid.UUID) error            { return nil }

type authPolicies struct{}

func (authPolicies) Get(_ context.Context, tenant uuid.UUID) (*domain.PasswordPolicy, error) {
	return domain.DefaultPasswordPolicy(tenant), nil
}
func (authPolicies) Upsert(context.Context, *domain.PasswordPolicy) error { return nil }

type nopAudit struct{}

func (nopAudit) Log(context.Context, *domain.AuditEntry) error { return nil }

type authEvents struct{ locked int }

func (e *authEvents) PublishUserCreated(string, string, string) error                 { return nil }
func (e *authEvents) PublishUserLoggedIn(string, string, string, string) error        { return nil }
func (e *authEvents) PublishUserLoggedOut(string, string) error                       { return nil }
func (e *authEvents) PublishUserLocked(string, string) error                          { e.locked++; return nil }
func (e *authEvents) PublishLoginFailed(string, string, string, string, string) error { return nil }
func (e *authEvents) PublishSessionRevoked(string, string, string, string, string) error {
	return nil
}
func (e *authEvents) PublishPasswordChanged(string, string) error { return nil }

type noRoles struct{}

func (noRoles) RoleNames(context.Context, uuid.UUID) ([]string, error) { return []string{}, nil }

type authFixture struct {
	uc       *AuthUseCase
	users    *authUsers
	sessions *authSessions
	events   *authEvents
	tokens   *auth.TokenService
	tenant   uuid.UUID
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
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
	f := &authFixture{
		users:    &authUsers{users: map[uuid.UUID]*domain.User{}},
		sessions: &authSessions{byHash: map[string]*domain.Session{}},
		events:   &authEvents{},
		tokens:   auth.NewTokenService(signer, verifier, 5*time.Minute, time.Hour),
		tenant:   uuid.New(),
	}
	f.uc = NewAuthUseCase(AuthDeps{
		Users: f.users, Sessions: f.sessions, Policies: authPolicies{}, Audit: nopAudit{},
		Events: f.events, Tokens: f.tokens, Tenants: authTenants{id: f.tenant}, Roles: noRoles{},
		Logger: zap.NewNop(), Now: func() time.Time { return authNow },
	})
	return f
}

func (f *authFixture) account(status domain.UserStatus, lockedUntil *time.Time) *domain.User {
	u := &domain.User{
		ID: uuid.New(), TenantID: f.tenant, Email: uuid.NewString() + "@example.test",
		PasswordHash: authHash, Status: status, LockedUntil: lockedUntil,
	}
	f.users.users[u.ID] = u
	return u
}

func (f *authFixture) login(u *domain.User, password string) (*ports.LoginResponse, error) {
	return f.uc.Login(context.Background(), ports.LoginRequest{
		TenantSlug: "acme", Email: u.Email, Password: password, IPAddress: "203.0.113.7",
	})
}

// Una cuenta que no puede tener sesion se rechaza antes de mirar la contrasena: con la buena y
// con una mala se responde lo mismo, no cuenta un intento fallido ni abre sesion.
func TestLoginRechazaSinMirarLaContrasena(t *testing.T) {
	cases := []struct {
		name   string
		status domain.UserStatus
		until  *time.Time
		want   error
	}{
		{"pendiente", domain.UserStatusPending, nil, domain.ErrAccountInactive},
		{"inactiva", domain.UserStatusInactive, nil, domain.ErrAccountInactive},
		{"bloqueo vigente", domain.UserStatusLocked, authAt(time.Minute), domain.ErrAccountLocked},
		{"estado desconocido", domain.UserStatus("suspended"), nil, domain.ErrAccountInactive},
	}
	for _, c := range cases {
		f := newAuthFixture(t)
		u := f.account(c.status, c.until)
		for _, password := range []string{authPassword, "no-es-la-contrasena"} {
			if res, err := f.login(u, password); !errors.Is(err, c.want) || res != nil {
				t.Errorf("%s con contrasena %q: res=%v err=%v, se esperaba %v", c.name, password, res, err, c.want)
			}
		}
		if f.users.increments != 0 || f.sessions.created != 0 {
			t.Errorf("%s: %d intentos fallidos y %d sesiones, se esperaba ninguno", c.name, f.users.increments, f.sessions.created)
		}
	}
}

// Con el bloqueo ya vencido (o sin fecha) la contrasena buena entra y el bloqueo se retira.
func TestLoginConElBloqueoVencidoEntraYLoRetira(t *testing.T) {
	for name, until := range map[string]*time.Time{
		"vencido":           authAt(-time.Second),
		"vence justo ahora": authAt(0),
		"sin fecha":         nil,
	} {
		f := newAuthFixture(t)
		u := f.account(domain.UserStatusLocked, until)
		res, err := f.login(u, authPassword)
		if err != nil || res == nil || res.AccessToken == "" {
			t.Errorf("%s: res=%v err=%v, se esperaba sesion", name, res, err)
			continue
		}
		if f.users.resets != 1 || f.sessions.created != 1 {
			t.Errorf("%s: %d desbloqueos y %d sesiones, se esperaba uno de cada", name, f.users.resets, f.sessions.created)
		}
	}
}

// Una contrasena mala tras vencer el bloqueo vuelve a bloquear, con el plazo contado desde
// ahora: los intentos no se reinician solos al vencer.
func TestLoginMaloTrasElBloqueoVuelveABloquear(t *testing.T) {
	f := newAuthFixture(t)
	u := f.account(domain.UserStatusLocked, authAt(-time.Minute))
	u.FailedLoginAttempts = domain.DefaultPasswordPolicy(f.tenant).MaxFailedAttempts
	if _, err := f.login(u, "no-es-la-contrasena"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("err = %v, se esperaba ErrInvalidCredentials", err)
	}
	lockout := time.Duration(domain.DefaultPasswordPolicy(f.tenant).LockoutDurationMinutes) * time.Minute
	if f.users.increments != 1 || f.users.lockedTo == nil || !f.users.lockedTo.Equal(authNow.Add(lockout)) || f.events.locked != 1 {
		t.Fatalf("intentos=%d bloqueo=%v eventos=%d; se esperaba un bloqueo hasta %v", f.users.increments, f.users.lockedTo, f.events.locked, authNow.Add(lockout))
	}
}

// Renovar aplica la misma regla que el inicio de sesion: un bloqueo vencido ya no la impide.
func TestRenovarAplicaLaMismaRegla(t *testing.T) {
	cases := []struct {
		name   string
		status domain.UserStatus
		until  *time.Time
		want   error
	}{
		{"activa", domain.UserStatusActive, nil, nil},
		{"bloqueo vencido", domain.UserStatusLocked, authAt(-time.Minute), nil},
		{"bloqueada sin fecha", domain.UserStatusLocked, nil, nil},
		{"bloqueo vigente", domain.UserStatusLocked, authAt(time.Minute), domain.ErrAccountLocked},
		{"inactiva", domain.UserStatusInactive, nil, domain.ErrAccountInactive},
		{"pendiente", domain.UserStatusPending, nil, domain.ErrAccountInactive},
	}
	for _, c := range cases {
		f := newAuthFixture(t)
		u := f.account(c.status, c.until)
		f.sessions.byHash[hashToken("refresh-1")] = &domain.Session{
			ID: uuid.New(), UserID: u.ID, ExpiresAt: authNow.Add(time.Hour),
			CreatedAt: authNow.Add(-5 * time.Minute), LoginAt: authNow.Add(-time.Hour),
		}
		res, err := f.uc.RefreshToken(context.Background(), "refresh-1")
		if c.want == nil {
			if err != nil || res == nil || res.AccessToken == "" {
				t.Errorf("%s: res=%v err=%v, se esperaba renovar", c.name, res, err)
			}
			continue
		}
		if !errors.Is(err, c.want) || f.sessions.created != 0 {
			t.Errorf("%s: err=%v sesiones=%d, se esperaba %v sin sesion nueva", c.name, err, f.sessions.created, c.want)
		}
	}
}

// El segundo factor tampoco termina de abrir la sesion de una cuenta que dejo de poder
// tenerla durante los 5 minutos del reto, y no cuenta el codigo como intento fallido.
func TestElSegundoFactorAplicaLaMismaRegla(t *testing.T) {
	cases := []struct {
		name   string
		status domain.UserStatus
		until  *time.Time
		want   error
	}{
		{"pendiente", domain.UserStatusPending, nil, domain.ErrAccountInactive},
		{"inactiva", domain.UserStatusInactive, nil, domain.ErrAccountInactive},
		{"bloqueo vigente", domain.UserStatusLocked, authAt(time.Minute), domain.ErrAccountLocked},
	}
	for _, c := range cases {
		f := newAuthFixture(t)
		u := f.account(c.status, c.until)
		u.MFAEnabled, u.MFASecret = true, "JBSWY3DPEHPK3PXP"
		challenge, err := f.tokens.GenerateMFAChallenge(u.ID.String(), f.tenant.String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.uc.VerifyMFAChallenge(context.Background(), challenge, "000000", "203.0.113.7", "prueba"); !errors.Is(err, c.want) {
			t.Errorf("%s: err=%v, se esperaba %v", c.name, err, c.want)
		}
		if f.users.increments != 0 || f.sessions.created != 0 {
			t.Errorf("%s: %d intentos y %d sesiones, se esperaba ninguno", c.name, f.users.increments, f.sessions.created)
		}
	}
}
