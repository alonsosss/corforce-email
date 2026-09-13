//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	outboxadapter "github.com/alonsosss/corforce-email/services/identity/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/identity/internal/app"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Estado efectivo de la vista, baja con su evento y bloqueo sobre el registro real
// (IDENTITY_TEST_DSN). Las fechas de bloqueo se fijan relativas al now() de la base, a un dia
// de distancia: ninguna comprobacion depende de la fecha real.

// insertAccount da de alta una cuenta con su estado y, si lockOffset no es nil, un
// locked_until a esa distancia del now() de la base.
func insertAccount(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, status string, lockOffset *time.Duration) uuid.UUID {
	t.Helper()
	var offset any
	if lockOffset != nil {
		offset = fmt.Sprintf("%d seconds", int64(lockOffset.Seconds()))
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO identity.users (tenant_id, email, password_hash, first_name, last_name, status, locked_until)
		 VALUES ($1, $2, 'x', 'Prueba', 'Ciclo', $3, now() + $4::interval) RETURNING id`,
		tenant, uuid.NewString()+"@example.test", status, offset).Scan(&id); err != nil {
		t.Fatalf("cuenta %s: %v", status, err)
	}
	return id
}

func cleanupTenant(t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID) {
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM identity.users WHERE tenant_id = $1`, tenant)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.event_outbox WHERE tenant_id = $1`, tenant)
	})
}

// La columna que lee access-control da, para cada cuenta, lo mismo que la regla de identity
// con el mismo reloj (el now() de la transaccion que la lee). Las migraciones se aplicaron
// dos veces: la 023 no le quita la columna que anade la 027.
func TestEstadoEfectivoDeLaVistaIgualQueElDominio(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()

	if got, want := viewColumns(ctx, t, pool, "identity", "v_user_status"),
		[]string{"user_id", "tenant_id", "status", "tokens_valid_from", "effective_status"}; !slices.Equal(got, want) {
		t.Fatalf("columnas de v_user_status = %v, se esperaba %v", got, want)
	}

	tenant := uuid.New()
	cleanupTenant(t, pool, tenant)
	day, pastDay := 24*time.Hour, -24*time.Hour
	cases := map[string]struct {
		status string
		offset *time.Duration
		want   string
	}{
		"activa":                        {"active", nil, "active"},
		"inactiva":                      {"inactive", nil, "inactive"},
		"pendiente":                     {"pending", nil, "pending"},
		"bloqueo vigente":               {"locked", &day, "locked"},
		"bloqueo vencido":               {"locked", &pastDay, "active"},
		"bloqueada sin fecha":           {"locked", nil, "active"},
		"inactiva con fecha de bloqueo": {"inactive", &day, "inactive"},
	}
	ids := map[string]uuid.UUID{}
	for name, c := range cases {
		ids[name] = insertAccount(ctx, t, pool, tenant, c.status, c.offset)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var dbNow time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	repo := NewUserRepo(pool)
	for name, c := range cases {
		var effective string
		if err := tx.QueryRow(ctx,
			`SELECT effective_status FROM identity.v_user_status WHERE user_id = $1 AND tenant_id = $2`,
			ids[name], tenant).Scan(&effective); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if effective != c.want {
			t.Errorf("%s: effective_status = %q, se esperaba %q", name, effective, c.want)
		}
		u, err := repo.GetByID(ctx, ids[name])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := string(u.EffectiveStatus(dbNow)); got != effective {
			t.Errorf("%s: la vista dice %q y el dominio %q con el mismo reloj", name, effective, got)
		}
	}
}

type failingAccountEvents struct{}

func (failingAccountEvents) UserDeleted(context.Context, domain.UserDeletion) error {
	return errors.New("outbox no disponible")
}

func outboxRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID) (subjects []string, payloads [][]byte) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT subject, payload FROM platform.event_outbox WHERE tenant_id = $1 ORDER BY created_at`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var p []byte
		if err := rows.Scan(&s, &p); err != nil {
			t.Fatal(err)
		}
		subjects, payloads = append(subjects, s), append(payloads, p)
	}
	return subjects, payloads
}

// El borrado y su evento se confirman juntos: si el evento no se encola la cuenta sigue ahi,
// y una baja repetida no deja un segundo evento.
func TestBajaDeUnaCuentaYSuEventoSeConfirmanJuntos(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	tenant, actor := uuid.New(), uuid.New()
	cleanupTenant(t, pool, tenant)
	users := NewUserRepo(pool)
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	deps := app.UserDeps{Users: users, Tx: NewTransactor(pool), Now: func() time.Time { return now }}

	target := insertAccount(ctx, t, pool, tenant, "active", nil)
	withoutOutbox := deps
	withoutOutbox.AccountEvents = failingAccountEvents{}
	if err := app.NewUserUseCase(withoutOutbox).Delete(ctx, target, actor); err == nil {
		t.Fatal("una baja cuyo evento no se encola debe fallar")
	}
	if _, err := users.GetByID(ctx, target); err != nil {
		t.Fatalf("sin evento la cuenta debe seguir: %v", err)
	}

	deps.AccountEvents = outboxadapter.NewPublisher(&db.ContextPool{})
	uc := app.NewUserUseCase(deps)
	if err := uc.Delete(ctx, target, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := users.GetByID(ctx, target); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("la cuenta sigue tras la baja: %v", err)
	}
	subjects, payloads := outboxRows(ctx, t, pool, tenant)
	if !slices.Equal(subjects, []string{outboxadapter.SubjectUserDeleted}) {
		t.Fatalf("outbox de la empresa: %v", subjects)
	}
	var evt struct {
		Type     string            `json:"type"`
		Source   string            `json:"source"`
		TenantID string            `json:"tenant_id"`
		UserID   string            `json:"user_id"`
		Data     map[string]string `json:"data"`
	}
	if err := json.Unmarshal(payloads[0], &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Type != "user.deleted" || evt.Source != "identity-service" || evt.TenantID != tenant.String() || evt.UserID != actor.String() {
		t.Fatalf("sobre: %+v", evt)
	}
	want := map[string]string{"tenant_id": tenant.String(), "user_id": target.String(), "deleted_at": "2026-03-01T10:00:00Z"}
	if !mapsEqual(evt.Data, want) {
		t.Fatalf("payload %v, se esperaba %v", evt.Data, want)
	}

	if err := uc.Delete(ctx, target, actor); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("segunda baja: %v, se esperaba ErrUserNotFound", err)
	}
	if subjects, _ := outboxRows(ctx, t, pool, tenant); len(subjects) != 1 {
		t.Fatalf("una baja repetida dejo %d eventos", len(subjects))
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// El bloqueo solo cae sobre una cuenta que podia entrar, y retirarlo la devuelve a active sin
// tocar inactive ni pending. Tampoco se resuelve la empresa por el correo de una cuenta que no
// puede entrar.
func TestElBloqueoSoloCaeSobreCuentasQuePodianEntrar(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	tenant := uuid.New()
	cleanupTenant(t, pool, tenant)
	repo := NewUserRepo(pool)

	pending := insertAccount(ctx, t, pool, tenant, "pending", nil)
	inactive := insertAccount(ctx, t, pool, tenant, "inactive", nil)
	active := insertAccount(ctx, t, pool, tenant, "active", nil)
	until := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)
	for _, id := range []uuid.UUID{pending, inactive, active} {
		if err := repo.LockUser(ctx, id, &until); err != nil {
			t.Fatal(err)
		}
	}
	read := func(id uuid.UUID) *domain.User {
		t.Helper()
		u, err := repo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	if s := read(pending).Status; s != domain.UserStatusPending {
		t.Errorf("pendiente tras bloquear: %s", s)
	}
	if s := read(inactive).Status; s != domain.UserStatusInactive {
		t.Errorf("inactiva tras bloquear: %s", s)
	}
	if u := read(active); u.Status != domain.UserStatusLocked || u.LockedUntil == nil || !u.LockedUntil.Equal(until) {
		t.Errorf("activa tras bloquear: %s hasta %v", u.Status, u.LockedUntil)
	}

	tenants := NewTenantRepo(pool)
	for name, id := range map[string]uuid.UUID{"pendiente": pending, "inactiva": inactive} {
		if _, err := tenants.GetIDByEmail(ctx, read(id).Email); !errors.Is(err, domain.ErrTenantNotFound) {
			t.Errorf("empresa por el correo de una cuenta %s: %v, se esperaba ErrTenantNotFound", name, err)
		}
	}
	if got, err := tenants.GetIDByEmail(ctx, read(active).Email); err != nil || got != tenant {
		t.Errorf("empresa por el correo de una cuenta bloqueada: %v, %v", got, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE identity.users SET failed_login_attempts = 5 WHERE id = ANY($1)`,
		[]uuid.UUID{active, inactive}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{active, inactive} {
		if err := repo.ResetFailedAttempts(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if u := read(active); u.Status != domain.UserStatusActive || u.LockedUntil != nil || u.FailedLoginAttempts != 0 {
		t.Errorf("desbloqueada: %s hasta %v con %d intentos", u.Status, u.LockedUntil, u.FailedLoginAttempts)
	}
	if u := read(inactive); u.Status != domain.UserStatusInactive || u.FailedLoginAttempts != 0 {
		t.Errorf("inactiva tras reiniciar intentos: %s con %d", u.Status, u.FailedLoginAttempts)
	}
}
