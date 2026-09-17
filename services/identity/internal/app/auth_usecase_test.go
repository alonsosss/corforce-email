package app

import (
	"context"
	"errors"
	"slices"
	"strings"
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
	logins     []*ports.PasswordRehash
	// listErr es el fallo de la lectura de las cuentas de un correo, para el caso en que la
	// base no responde.
	listErr error
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

// ListLoginCandidates devuelve lo que devolveria la sentencia real: las cuentas del correo que
// pueden tener sesion, en orden estable por alta, hasta el tope.
func (s *authUsers) ListLoginCandidates(_ context.Context, email string, limit int) ([]*domain.User, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
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

func (s *authUsers) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	if u, ok := s.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, domain.ErrUserNotFound
}

// IncrementFailedAttempts, LockUser y RecordLogin dejan en la cuenta lo que dejaria la base, con
// la regla del dominio: las pruebas de integracion comprueban que las sentencias reales la siguen.
func (s *authUsers) IncrementFailedAttempts(_ context.Context, id uuid.UUID, now time.Time) (int, error) {
	s.increments++
	u, ok := s.users[id]
	if !ok {
		return 0, domain.ErrUserNotFound
	}
	u.FailedLoginAttempts = u.Failures().AttemptsAfterFailure(now)
	u.LastFailedLoginAt = &now
	return u.FailedLoginAttempts, nil
}
func (s *authUsers) ResetFailedAttempts(context.Context, uuid.UUID) error { s.resets++; return nil }
func (s *authUsers) LockUser(_ context.Context, id uuid.UUID, until *time.Time) error {
	s.lockedTo = until
	if u, ok := s.users[id]; ok && (u.Status == domain.UserStatusActive || u.Status == domain.UserStatusLocked) {
		u.Status, u.LockedUntil = domain.UserStatusLocked, until
	}
	return nil
}
func (s *authUsers) RecordLogin(_ context.Context, id uuid.UUID, rehash *ports.PasswordRehash) error {
	s.resets++
	s.logins = append(s.logins, rehash)
	if u, ok := s.users[id]; ok {
		u.FailedLoginAttempts, u.LastFailedLoginAt, u.LockedUntil = 0, nil, nil
		if u.Status == domain.UserStatusLocked {
			u.Status = domain.UserStatusActive
		}
		if rehash != nil && u.PasswordHash == rehash.Current {
			u.PasswordHash = rehash.Replacement
		}
	}
	return nil
}

// authUnknown guarda los contadores de los correos sin cuenta con la regla del dominio.
type authUnknown struct {
	counters map[string]domain.LoginFailures
	calls    int
}

func (s *authUnknown) RecordFailure(_ context.Context, subject string, now time.Time, maxAttempts int, lockout time.Duration) (bool, error) {
	s.calls++
	if s.counters == nil {
		s.counters = map[string]domain.LoginFailures{}
	}
	next, wasLocked := s.counters[subject].RecordFailure(now, maxAttempts, lockout)
	s.counters[subject] = next
	return wasLocked, nil
}
func (s *authUnknown) PruneForgotten(context.Context, time.Time, int) (int64, error) { return 0, nil }

// authTenants resuelve la misma empresa por slug y por correo; con missing, ninguna. emailCalls
// cuenta las resoluciones por correo: el inicio de sesion no puede volver a resolver la empresa
// asi, porque un correo dado de alta en varias solo resuelve una.
type authTenants struct {
	id         uuid.UUID
	missing    bool
	emailCalls int
}

func (t *authTenants) GetIDBySlug(context.Context, string) (uuid.UUID, error) { return t.resolve() }
func (t *authTenants) GetIDByEmail(context.Context, string) (uuid.UUID, error) {
	t.emailCalls++
	return t.resolve()
}
func (t *authTenants) IsActive(context.Context, uuid.UUID) (bool, error) { return true, nil }

func (t *authTenants) resolve() (uuid.UUID, error) {
	if t.missing {
		return uuid.Nil, domain.ErrTenantNotFound
	}
	return t.id, nil
}

// countingHasher es bcrypt con el coste que se le da y anota que hashes compara: las pruebas
// comprueban que cada fallo compara una contrasena, no cuanto tarda.
type countingHasher struct {
	cost     int
	hashes   int
	compared []string
}

func (h *countingHasher) Hash(password string) (string, error) {
	h.hashes++
	out, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	return string(out), err
}

func (h *countingHasher) Compare(hash, password string) error {
	h.compared = append(h.compared, hash)
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

func (h *countingHasher) NeedsRehash(hash string) bool {
	cost, err := bcrypt.Cost([]byte(hash))
	return err == nil && cost != h.cost
}

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
	tenants  *authTenants
	hasher   *countingHasher
	unknown  *authUnknown
	// clock es la hora del caso de uso; empieza en authNow y la prueba la adelanta.
	clock time.Time
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
		hasher:   &countingHasher{cost: bcrypt.MinCost},
		unknown:  &authUnknown{},
		clock:    authNow,
	}
	f.tenants = &authTenants{id: f.tenant}
	uc, err := NewAuthUseCase(AuthDeps{
		Users: f.users, Sessions: f.sessions, Policies: authPolicies{}, Audit: nopAudit{},
		Events: f.events, Tokens: f.tokens, Tenants: f.tenants, Roles: noRoles{}, Hasher: f.hasher,
		UnknownLogins: f.unknown, Logger: zap.NewNop(), Now: func() time.Time { return f.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.uc = uc
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

// accountIn da de alta una cuenta de otra empresa con el correo, la contrasena, el estado y la
// fecha de alta que se le piden: es la misma direccion en varias empresas. La fecha de alta
// ordena las candidatas, asi que cada una lleva la suya.
func (f *authFixture) accountIn(t *testing.T, tenant uuid.UUID, email, password string, status domain.UserStatus, created time.Time) *domain.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := &domain.User{
		ID: uuid.New(), TenantID: tenant, Email: email, PasswordHash: string(hash),
		Status: status, CreatedAt: created,
	}
	f.users.users[u.ID] = u
	return u
}

// loginByEmail es el inicio de sesion que no indica la empresa: el que la resuelve por la
// credencial.
func (f *authFixture) loginByEmail(email, password string) (*ports.LoginResponse, error) {
	return f.uc.Login(context.Background(), ports.LoginRequest{
		Email: email, Password: password, IPAddress: "203.0.113.7",
	})
}

func (f *authFixture) login(u *domain.User, password string) (*ports.LoginResponse, error) {
	return f.uc.Login(context.Background(), ports.LoginRequest{
		TenantSlug: "acme", Email: u.Email, Password: password, IPAddress: "203.0.113.7",
	})
}

// Una cuenta que no puede tener sesion se rechaza sin mirar su contrasena: con la buena y con
// una mala se responde lo mismo, no cuenta un intento fallido ni abre sesion. Compara una vez
// contra el hash de relleno, nunca contra el de la cuenta, para tardar lo que otro fallo.
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
			before := len(f.hasher.compared)
			if res, err := f.login(u, password); !errors.Is(err, c.want) || res != nil {
				t.Errorf("%s con contrasena %q: res=%v err=%v, se esperaba %v", c.name, password, res, err, c.want)
			}
			if got := f.hasher.compared[before:]; len(got) != 1 || got[0] != f.uc.decoyHash {
				t.Errorf("%s con contrasena %q: comparo %d hashes %v, se esperaba solo el de relleno", c.name, password, len(got), got)
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
		// El reto firmado solo sale con la contrasena correcta: aqui no se compara ninguna.
		if len(f.hasher.compared) != 0 {
			t.Errorf("%s: el segundo factor comparo %d contrasenas", c.name, len(f.hasher.compared))
		}
	}
}

// Todo inicio de sesion gasta las comparaciones de su camino, falle donde falle: una si indica
// la empresa (una sola cuenta posible) y loginCandidateLimit si solo trae el correo (ese es el
// tope de cuentas que puede resolver). Solo una cuenta que puede entrar se compara contra su
// hash; lo que sobra va contra el de relleno. Asi el tiempo no dice si el correo existe, ni si
// resuelve empresa, ni en cuantas empresas esta.
func TestCadaLoginGastaLasComparacionesDeSuCamino(t *testing.T) {
	cases := []struct {
		name     string
		slug     string
		noTenant bool
		ownEmail bool
		password string
		want     error
		// wantOwn es si se compara contra el hash de la cuenta; el resto del camino va contra
		// el de relleno.
		wantOwn bool
	}{
		{"slug que no resuelve empresa", "no-existe", true, false, authPassword, domain.ErrTenantNotFound, false},
		{"correo sin cuenta y sin empresa", "", true, false, authPassword, domain.ErrTenantNotFound, false},
		{"correo desconocido en la empresa", "acme", false, false, authPassword, domain.ErrInvalidCredentials, false},
		{"contrasena mala con slug", "acme", false, true, "no-es-la-contrasena", domain.ErrInvalidCredentials, true},
		{"contrasena mala sin slug", "", false, true, "no-es-la-contrasena", domain.ErrInvalidCredentials, true},
		{"contrasena buena con slug", "acme", false, true, authPassword, nil, true},
		{"contrasena buena sin slug", "", false, true, authPassword, nil, true},
	}
	for _, c := range cases {
		f := newAuthFixture(t)
		u := f.account(domain.UserStatusActive, nil)
		f.tenants.missing = c.noTenant
		email := "nadie@example.test"
		if c.ownEmail {
			email = u.Email
		}
		res, err := f.uc.Login(context.Background(), ports.LoginRequest{
			TenantSlug: c.slug, Email: email, Password: c.password, IPAddress: "203.0.113.7",
		})
		if !errors.Is(err, c.want) || (c.want == nil) != (res != nil) {
			t.Errorf("%s: res=%v err=%v, se esperaba %v", c.name, res, err, c.want)
		}
		want := []string{f.uc.decoyHash}
		if c.slug == "" {
			want = slices.Repeat([]string{f.uc.decoyHash}, loginCandidateLimit)
		}
		if c.wantOwn {
			want[0] = u.PasswordHash
		}
		if got := f.hasher.compared; !slices.Equal(got, want) {
			t.Errorf("%s: comparo %v, se esperaba %v", c.name, got, want)
		}
	}
}

// El hash de relleno sale del hasher al construir el caso de uso, con el coste de ese hasher:
// no es una constante que pueda quedarse con otro coste. Sin hasher no hay caso de uso.
func TestElHashDeRellenoTieneElCosteDelHasher(t *testing.T) {
	for _, cost := range []int{bcrypt.MinCost, bcrypt.MinCost + 1} {
		h := &countingHasher{cost: cost}
		uc, err := NewAuthUseCase(AuthDeps{Hasher: h, UnknownLogins: &authUnknown{}, Logger: zap.NewNop()})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := bcrypt.Cost([]byte(uc.decoyHash)); h.hashes != 1 || err != nil || got != cost {
			t.Errorf("coste %d: %d hashes al construir, relleno con coste %d (%v)", cost, h.hashes, got, err)
		}
	}
	if _, err := NewAuthUseCase(AuthDeps{UnknownLogins: &authUnknown{}, Logger: zap.NewNop()}); err == nil {
		t.Fatal("sin hasher el caso de uso no debe construirse")
	}
	// Sin los contadores de los correos sin cuenta, el bloqueo delataria las cuentas.
	if _, err := NewAuthUseCase(AuthDeps{Hasher: &countingHasher{cost: bcrypt.MinCost}, Logger: zap.NewNop()}); err == nil {
		t.Fatal("sin contadores de correos sin cuenta el caso de uso no debe construirse")
	}
}
