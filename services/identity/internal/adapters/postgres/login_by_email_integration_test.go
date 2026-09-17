//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Inicio de sesion sin indicar la empresa sobre el registro real (IDENTITY_TEST_DSN): la empresa
// la resuelve la credencial, no el correo, cuando la misma direccion esta dada de alta en varias.
// Las cuentas se insertan de la mas nueva a la mas antigua a proposito: sin el ORDER BY, la
// lectura las devuelve en el orden de la tabla y las pruebas lo notan.

func insertLoginAccount(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, email, password, status string, created time.Time) uuid.UUID {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO identity.users (tenant_id, email, password_hash, first_name, last_name, status, created_at)
		 VALUES ($1, $2, $3, 'Prueba', 'Correo', $4, $5) RETURNING id`,
		tenant, email, string(hash), status, created).Scan(&id); err != nil {
		t.Fatalf("alta de %s en %s: %v", email, tenant, err)
	}
	return id
}

func failedAttempts(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id uuid.UUID) int {
	t.Helper()
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT failed_login_attempts FROM identity.users WHERE id = $1`, id).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	return attempts
}

// Con dos empresas que tienen el mismo correo y contrasenas distintas, cada persona entra en la
// suya sin indicar la empresa; una contrasena que no es de ninguna responde como cualquier otra
// mala y cuenta el intento en las dos cuentas.
func TestSinEmpresaCadaCuentaEntraConSuContrasenaEnLaBase(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	l := newItLogin(t, pool)
	otra := uuid.New()
	cleanupTenant(t, pool, otra)

	email := uuid.NewString() + "@example.test"
	nueva := insertLoginAccount(ctx, t, pool, otra, email, "Segunda-2026!", "active", failBase)
	vieja := insertLoginAccount(ctx, t, pool, l.tenant, email, "Primera-2026!", "active", failBase.Add(-48*time.Hour))

	for _, c := range []struct {
		name     string
		password string
		user     uuid.UUID
		tenant   uuid.UUID
	}{
		{"la cuenta mas antigua", "Primera-2026!", vieja, l.tenant},
		{"la cuenta mas nueva", "Segunda-2026!", nueva, otra},
	} {
		res, err := l.uc.Login(ctx, ports.LoginRequest{Email: email, Password: c.password, IPAddress: "203.0.113.7"})
		if err != nil || res == nil || res.AccessToken == "" {
			t.Fatalf("%s: res=%v err=%v, se esperaba sesion", c.name, res, err)
		}
		if res.UserID != c.user.String() || res.TenantID != c.tenant.String() {
			t.Errorf("%s: entro %s de la empresa %s, se esperaba %s de %s", c.name, res.UserID, res.TenantID, c.user, c.tenant)
		}
	}

	if _, err := l.uc.Login(ctx, ports.LoginRequest{Email: email, Password: "no-es-la-contrasena", IPAddress: "203.0.113.7"}); loginClass(err) != "401" {
		t.Fatalf("contrasena que no es de ninguna cuenta: %v, se esperaba 401", err)
	}
	for name, id := range map[string]uuid.UUID{"la mas antigua": vieja, "la mas nueva": nueva} {
		if got := failedAttempts(ctx, t, pool, id); got != 1 {
			t.Errorf("%s: %d intentos fallidos, se esperaba 1", name, got)
		}
	}
}

// Las cuentas de un correo se leen en orden de alta, solo las que pueden tener sesion y hasta el
// tope que se pide; la empresa que resuelve un correo para la recuperacion de contrasena es la de
// la cuenta mas antigua, siempre la misma.
func TestLasCuentasDeUnCorreoSeLeenPorAlta(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()
	users := NewUserRepo(pool)
	tenants := NewTenantRepo(pool)

	email := uuid.NewString() + "@example.test"
	type alta struct {
		tenant  uuid.UUID
		status  string
		created time.Time
	}
	altas := []alta{
		{uuid.New(), "inactive", failBase.Add(-72 * time.Hour)},
		{uuid.New(), "active", failBase},
		{uuid.New(), "locked", failBase.Add(-24 * time.Hour)},
		{uuid.New(), "active", failBase.Add(-48 * time.Hour)},
	}
	ids := make([]uuid.UUID, len(altas))
	for i, a := range altas {
		cleanupTenant(t, pool, a.tenant)
		ids[i] = insertLoginAccount(ctx, t, pool, a.tenant, email, "Correcta-2026!", a.status, a.created)
	}

	candidates, err := users.ListLoginCandidates(ctx, email, 4)
	if err != nil {
		t.Fatal(err)
	}
	// La inactive no sale; el resto, de la mas antigua a la mas nueva.
	orden := []int{3, 2, 1}
	if len(candidates) != len(orden) {
		t.Fatalf("%d candidatas, se esperaban %d", len(candidates), len(orden))
	}
	want := make([]uuid.UUID, len(orden))
	for i, pos := range orden {
		want[i] = ids[pos]
		u := candidates[i]
		if u.ID != ids[pos] || u.TenantID != altas[pos].tenant {
			t.Fatalf("candidata %d: %s de %s, se esperaba %s de %s", i, u.ID, u.TenantID, ids[pos], altas[pos].tenant)
		}
		if u.PasswordHash == "" {
			t.Fatalf("candidata %d sin hash: la comparacion de la contrasena lo necesita", i)
		}
	}

	if acotadas, err := users.ListLoginCandidates(ctx, email, 1); err != nil || len(acotadas) != 1 || acotadas[0].ID != want[0] {
		t.Fatalf("con tope 1: %v (%v), se esperaba solo %s", acotadas, err, want[0])
	}
	if vacias, err := users.ListLoginCandidates(ctx, email, 0); err != nil || len(vacias) != 0 {
		t.Fatalf("con tope 0: %v (%v), se esperaba ninguna", vacias, err)
	}
	if got, err := tenants.GetIDByEmail(ctx, email); err != nil || got != altas[3].tenant {
		t.Fatalf("GetIDByEmail = %v (%v), se esperaba la empresa de la cuenta mas antigua %v", got, err, altas[3].tenant)
	}
	if _, err := users.ListLoginCandidates(ctx, uuid.NewString()+"@example.test", 4); err != nil {
		t.Fatalf("un correo sin cuenta no es un error: %v", err)
	}
	// La cuenta inactive no es candidata, asi que su correo se comporta como uno sin cuenta.
	soloInactiva := uuid.NewString() + "@example.test"
	tenant := uuid.New()
	cleanupTenant(t, pool, tenant)
	insertLoginAccount(ctx, t, pool, tenant, soloInactiva, "Correcta-2026!", "inactive", failBase)
	if got, err := users.ListLoginCandidates(ctx, soloInactiva, 4); err != nil || len(got) != 0 {
		t.Fatalf("una cuenta inactive es candidata: %v (%v)", got, err)
	}
	if _, err := tenants.GetIDByEmail(ctx, soloInactiva); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("GetIDByEmail de una cuenta inactive: %v, se esperaba ErrTenantNotFound", err)
	}
}
