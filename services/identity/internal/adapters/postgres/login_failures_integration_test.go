//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/services/identity/internal/adapters/passwordhash"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// Contadores de inicios fallidos sobre el registro real (IDENTITY_TEST_DSN): las sentencias de
// una cuenta y de un correo sin cuenta siguen la regla del dominio, y el caso de uso responde lo
// mismo a las dos. Todas las horas se pasan explicitas: nada depende de la fecha real.

var failBase = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func newSubject() string {
	sum := sha256.Sum256([]byte(uuid.NewString()))
	return hex.EncodeToString(sum[:])
}

func cleanupSubjects(t *testing.T, pool *pgxpool.Pool, subjects ...string) {
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.unknown_login_failures WHERE subject_hash = ANY($1)`, subjects)
	})
}

func readUnknown(ctx context.Context, t *testing.T, pool *pgxpool.Pool, subject string) (domain.LoginFailures, bool) {
	t.Helper()
	var f domain.LoginFailures
	var last time.Time
	err := pool.QueryRow(ctx,
		`SELECT failed_attempts, last_failed_at, locked_until FROM identity.unknown_login_failures WHERE subject_hash = $1`,
		subject).Scan(&f.Attempts, &last, &f.LockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, false
	}
	if err != nil {
		t.Fatal(err)
	}
	f.LastFailedAt = &last
	return f, true
}

func sameTime(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
}

// La sentencia del contador de un correo sin cuenta da, paso a paso, lo mismo que
// domain.LoginFailures.RecordFailure: umbral, intento bloqueado que no cuenta, vuelta a bloquear
// al vencer, limite exacto de la ventana y olvido.
func TestContadorDeUnCorreoSinCuentaIgualQueElDominio(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	repo := NewUnknownLoginRepo(pool)
	subject := newSubject()
	cleanupSubjects(t, pool, subject)

	const lockout = 10 * time.Minute
	w := domain.FailedLoginWindow
	steps := []struct {
		at      time.Duration
		max     int
		lockout time.Duration
	}{
		{0, 3, lockout},
		{time.Second, 3, lockout},
		{2 * time.Second, 3, lockout},
		{3 * time.Second, 3, lockout},
		{2*time.Second + lockout, 3, lockout},
		{2*time.Second + 2*lockout + w, 3, lockout},
		{2*time.Second + 3*lockout + 2*w + time.Second, 3, lockout},
		{2*time.Second + 3*lockout + 2*w + 2*time.Second, 1, 0},
		{2*time.Second + 3*lockout + 2*w + 2*time.Second, 1, 0},
	}
	var model domain.LoginFailures
	for i, s := range steps {
		now := failBase.Add(s.at)
		want, wantLocked := model.RecordFailure(now, s.max, s.lockout)
		gotLocked, err := repo.RecordFailure(ctx, subject, now, s.max, s.lockout)
		if err != nil {
			t.Fatalf("paso %d: %v", i, err)
		}
		got, ok := readUnknown(ctx, t, pool, subject)
		if !ok || gotLocked != wantLocked || got.Attempts != want.Attempts ||
			!sameTime(got.LastFailedAt, want.LastFailedAt) || !sameTime(got.LockedUntil, want.LockedUntil) {
			t.Fatalf("paso %d: base %+v bloqueado=%v; dominio %+v bloqueado=%v", i, got, gotLocked, want, wantLocked)
		}
		model = want
	}
}

// Los fallos simultaneos contra el mismo correo cuentan todos: la fila en conflicto queda
// bloqueada hasta que termina cada sentencia.
func TestFallosSimultaneosDeUnCorreoSinCuentaCuentanTodos(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	repo := NewUnknownLoginRepo(pool)
	subject := newSubject()
	cleanupSubjects(t, pool, subject)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			_, err := repo.RecordFailure(ctx, subject, failBase, 1000, time.Minute)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := readUnknown(ctx, t, pool, subject); got.Attempts != n {
		t.Fatalf("%d fallos simultaneos dejaron el contador en %d", n, got.Attempts)
	}
}

// La poda borra, en lotes, justo los contadores que el siguiente fallo reiniciaria: los que
// tienen el ultimo fallo y el final del bloqueo fuera de la ventana.
func TestLaPodaSoloBorraLosContadoresOlvidados(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	repo := NewUnknownLoginRepo(pool)
	now := failBase.Add(10 * domain.FailedLoginWindow)
	w := domain.FailedLoginWindow

	type row struct {
		lastFailed time.Duration
		lockout    time.Duration
		forgotten  bool
	}
	rows := map[string]row{
		"fallo fuera de la ventana":            {-w - time.Second, 0, true},
		"fallo en el limite":                   {-w, 0, false},
		"fallo reciente":                       {-time.Hour, 0, false},
		"bloqueo que termino dentro":           {-w - time.Hour, 2 * time.Hour, false},
		"bloqueo que termino fuera":            {-2 * w, time.Hour, true},
		"otro fallo fuera de la ventana":       {-3 * w, 0, true},
		"bloqueo vigente con el fallo antiguo": {-w - time.Hour, w + 2*time.Hour, false},
	}
	subjects := map[string]string{}
	var all []string
	for name, r := range rows {
		s := newSubject()
		subjects[name], all = s, append(all, s)
		maxAttempts := 1000
		if r.lockout > 0 {
			maxAttempts = 1
		}
		if _, err := repo.RecordFailure(ctx, s, now.Add(r.lastFailed), maxAttempts, r.lockout); err != nil {
			t.Fatal(err)
		}
	}
	cleanupSubjects(t, pool, all...)

	if n, err := repo.PruneForgotten(ctx, now, 1); err != nil || n != 1 {
		t.Fatalf("un lote de uno borro %d (%v)", n, err)
	}
	for {
		n, err := repo.PruneForgotten(ctx, now, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if n < 1000 {
			break
		}
	}
	for name, r := range rows {
		if _, ok := readUnknown(ctx, t, pool, subjects[name]); ok == r.forgotten {
			t.Errorf("%s: sigue=%v, olvidado=%v", name, ok, r.forgotten)
		}
	}
}

// La sentencia de la cuenta cuenta con la misma regla que el dominio: conserva los fallos sin
// fecha (anteriores a la 028), los olvida fuera de la ventana, cuenta la ventana desde el final
// del bloqueo y el reinicio borra tambien la fecha del ultimo fallo.
func TestContadorDeUnaCuentaIgualQueElDominio(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	tenant := uuid.New()
	cleanupTenant(t, pool, tenant)
	repo := NewUserRepo(pool)
	id := insertAccount(ctx, t, pool, tenant, "active", nil)
	if _, err := pool.Exec(ctx, `UPDATE identity.users SET failed_login_attempts = 4 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	w := domain.FailedLoginWindow
	lockUntil := failBase.Add(100*w + 2*time.Hour)
	steps := []struct {
		at   time.Duration
		lock bool
	}{
		{100 * w, false},
		{101*w + time.Second, false},
		{101*w + time.Hour, true},
		{102*w + time.Second, false},
		{103*w + time.Second, false},
	}
	for i, s := range steps {
		now := failBase.Add(s.at)
		before, err := repo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := before.Failures().AttemptsAfterFailure(now)
		got, err := repo.IncrementFailedAttempts(ctx, id, now)
		if err != nil || got != want {
			t.Fatalf("paso %d: la base cuenta %d (%v), el dominio %d", i, got, err, want)
		}
		after, err := repo.GetByID(ctx, id)
		if err != nil || after.FailedLoginAttempts != want || !sameTime(after.LastFailedLoginAt, &now) {
			t.Fatalf("paso %d: fila %+v (%v)", i, after, err)
		}
		if s.lock {
			if err := repo.LockUser(ctx, id, &lockUntil); err != nil {
				t.Fatal(err)
			}
			lockUntil = lockUntil.Add(w)
		}
	}
	if _, err := repo.IncrementFailedAttempts(ctx, uuid.New(), failBase); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("cuenta inexistente: %v", err)
	}
	if err := repo.ResetFailedAttempts(ctx, id); err != nil {
		t.Fatal(err)
	}
	if u, _ := repo.GetByID(ctx, id); u.FailedLoginAttempts != 0 || u.LastFailedLoginAt != nil || u.LockedUntil != nil {
		t.Fatalf("tras reiniciar: %d intentos, ultimo fallo %v, bloqueo %v", u.FailedLoginAttempts, u.LastFailedLoginAt, u.LockedUntil)
	}
}

// El apunte de un inicio correcto retira el bloqueo y los intentos, fija la hora del inicio y
// cambia el hash solo si la cuenta conserva el que se comparo, sin tocar password_changed_at.
func TestElInicioCorrectoApuntaYRehaceElHash(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	tenant := uuid.New()
	cleanupTenant(t, pool, tenant)
	repo := NewUserRepo(pool)
	day := 24 * time.Hour
	id := insertAccount(ctx, t, pool, tenant, "locked", &day)
	changed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx,
		`UPDATE identity.users SET failed_login_attempts = 5, last_failed_login_at = $2, password_hash = 'viejo', password_changed_at = $3 WHERE id = $1`,
		id, failBase, changed); err != nil {
		t.Fatal(err)
	}
	read := func() *domain.User {
		t.Helper()
		u, err := repo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	if err := repo.RecordLogin(ctx, id, &ports.PasswordRehash{Current: "otro", Replacement: "nuevo"}); err != nil {
		t.Fatal(err)
	}
	u := read()
	if u.PasswordHash != "viejo" {
		t.Fatalf("se piso un hash que ya no era el comparado: %q", u.PasswordHash)
	}
	if u.Status != domain.UserStatusActive || u.FailedLoginAttempts != 0 || u.LastFailedLoginAt != nil || u.LockedUntil != nil || u.LastLoginAt == nil {
		t.Fatalf("apunte del inicio: %+v", u)
	}
	if err := repo.RecordLogin(ctx, id, &ports.PasswordRehash{Current: "viejo", Replacement: "nuevo"}); err != nil {
		t.Fatal(err)
	}
	if u := read(); u.PasswordHash != "nuevo" || u.PasswordChangedAt == nil || !u.PasswordChangedAt.Equal(changed) {
		t.Fatalf("rehash: hash %q, cambiada %v", u.PasswordHash, u.PasswordChangedAt)
	}
	if err := repo.RecordLogin(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if u := read(); u.PasswordHash != "nuevo" {
		t.Fatalf("sin rehash el hash cambio: %q", u.PasswordHash)
	}
}

type itTenants struct {
	*TenantRepo
	slug string
	id   uuid.UUID
}

func (t itTenants) GetIDBySlug(_ context.Context, slug string) (uuid.UUID, error) {
	if slug == t.slug {
		return t.id, nil
	}
	return uuid.Nil, domain.ErrTenantNotFound
}

type itAudit struct{}

func (itAudit) Log(context.Context, *domain.AuditEntry) error { return nil }

type itEvents struct{}

func (itEvents) PublishUserCreated(string, string, string) error                    { return nil }
func (itEvents) PublishUserLoggedIn(string, string, string, string) error           { return nil }
func (itEvents) PublishUserLoggedOut(string, string) error                          { return nil }
func (itEvents) PublishUserLocked(string, string) error                             { return nil }
func (itEvents) PublishLoginFailed(string, string, string, string, string) error    { return nil }
func (itEvents) PublishSessionRevoked(string, string, string, string, string) error { return nil }
func (itEvents) PublishPasswordChanged(string, string) error                        { return nil }

type itLogin struct {
	uc     *app.AuthUseCase
	clock  *time.Time
	tenant uuid.UUID
	slug   string
}

// newItLogin monta el caso de uso sobre los repositorios reales, con el hasher de identity al
// coste minimo y una politica propia de la empresa (3 intentos, 10 minutos).
func newItLogin(t *testing.T, pool *pgxpool.Pool) *itLogin {
	t.Helper()
	ctx := context.Background()
	l := &itLogin{tenant: uuid.New(), slug: "it-" + uuid.NewString()[:8]}
	cleanupTenant(t, pool, l.tenant)
	policies := NewPasswordPolicyRepo(pool)
	p := domain.DefaultPasswordPolicy(l.tenant)
	p.MaxFailedAttempts, p.LockoutDurationMinutes = 3, 10
	if err := policies.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.password_policies WHERE tenant_id = $1`, l.tenant)
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.unknown_login_failures WHERE last_failed_at BETWEEN $1 AND $2`,
			failBase.Add(-time.Hour), failBase.Add(10*domain.FailedLoginWindow))
	})
	hasher, err := passwordhash.NewBcrypt(bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
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
	clock := failBase
	l.clock = &clock
	l.uc, err = app.NewAuthUseCase(app.AuthDeps{
		Users: NewUserRepo(pool), Sessions: NewSessionRepo(pool), Policies: policies,
		Tenants: itTenants{TenantRepo: NewTenantRepo(pool), slug: l.slug, id: l.tenant},
		Roles:   NewRoleRepo(pool), Audit: itAudit{}, Events: itEvents{},
		Tokens: auth.NewTokenService(signer, verifier, 5*time.Minute, time.Hour),
		Hasher: hasher, UnknownLogins: NewUnknownLoginRepo(pool), Logger: zap.NewNop(),
		Now: func() time.Time { return *l.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *itLogin) account(ctx context.Context, t *testing.T, pool *pgxpool.Pool, hash string) (uuid.UUID, string) {
	t.Helper()
	email := uuid.NewString() + "@example.test"
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO identity.users (tenant_id, email, password_hash, first_name, last_name, status)
		 VALUES ($1, $2, $3, 'Prueba', 'Bloqueo', 'active') RETURNING id`, l.tenant, email, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id, email
}

func loginClass(err error) string {
	switch {
	case err == nil:
		return "200"
	case errors.Is(err, domain.ErrInvalidCredentials), errors.Is(err, domain.ErrTenantNotFound):
		return "401"
	case errors.Is(err, domain.ErrAccountLocked):
		return "403"
	}
	return err.Error()
}

// Con los repositorios reales, una cuenta con la contrasena mala y un correo sin cuenta en la
// misma empresa responden la misma secuencia con la politica de la empresa: bloqueo al tercer
// fallo, vuelta a bloquear al vencer y olvido pasada la ventana.
func TestCuentaYCorreoSinCuentaSeBloqueanIgualEnLaBase(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	l := newItLogin(t, pool)
	hash, err := bcrypt.GenerateFromPassword([]byte("Correcta-2026!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_, email := l.account(ctx, t, pool, string(hash))

	script := []struct {
		advance  time.Duration
		attempts int
	}{
		{0, 5},
		{11 * time.Minute, 2},
		{10*time.Minute + domain.FailedLoginWindow + time.Second, 4},
	}
	want := strings.Fields("401 401 401 403 403  401 403  401 401 401 403")
	run := func(req ports.LoginRequest) []string {
		*l.clock = failBase
		var out []string
		for _, s := range script {
			*l.clock = l.clock.Add(s.advance)
			for range s.attempts {
				_, err := l.uc.Login(ctx, req)
				out = append(out, loginClass(err))
			}
		}
		return out
	}
	for name, req := range map[string]ports.LoginRequest{
		"cuenta con la empresa":           {TenantSlug: l.slug, Email: email, Password: "mala"},
		"correo sin cuenta en la empresa": {TenantSlug: l.slug, Email: uuid.NewString() + "@example.test", Password: "mala"},
	} {
		if got := run(req); !slices.Equal(got, want) {
			t.Errorf("%s: %v, se esperaba %v", name, got, want)
		}
	}
	var accounts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE tenant_id = $1`, l.tenant).Scan(&accounts); err != nil || accounts != 1 {
		t.Fatalf("el correo sin cuenta dejo filas en identity.users: %d (%v)", accounts, err)
	}
}

// Una cuenta con un hash de otro coste queda, tras su primer inicio correcto, con el coste de
// identity y la misma contrasena; el siguiente inicio ya no lo toca.
func TestElPrimerInicioCorrectoDejaElCosteDeIdentity(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	l := newItLogin(t, pool)
	old, err := bcrypt.GenerateFromPassword([]byte("Correcta-2026!"), bcrypt.MinCost+1)
	if err != nil {
		t.Fatal(err)
	}
	id, email := l.account(ctx, t, pool, string(old))
	users := NewUserRepo(pool)

	login := ports.LoginRequest{TenantSlug: l.slug, Email: email, Password: "Correcta-2026!", IPAddress: "203.0.113.7"}
	if res, err := l.uc.Login(ctx, login); err != nil || res.AccessToken == "" {
		t.Fatalf("inicio: %v", err)
	}
	u, err := users.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if cost, err := bcrypt.Cost([]byte(u.PasswordHash)); err != nil || cost != bcrypt.MinCost {
		t.Fatalf("coste tras el inicio: %d (%v)", cost, err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("Correcta-2026!")) != nil || u.PasswordChangedAt != nil {
		t.Fatalf("el hash nuevo no es de la misma contrasena o se marco como cambiada")
	}
	if _, err := l.uc.Login(ctx, login); err != nil {
		t.Fatal(err)
	}
	if again, _ := users.GetByID(ctx, id); again.PasswordHash != u.PasswordHash {
		t.Fatal("con el coste vigente el hash volvio a cambiar")
	}
}
