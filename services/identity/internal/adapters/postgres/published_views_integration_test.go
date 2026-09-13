//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IDENTITY_TEST_DSN apunta a un Postgres limpio (pgvector/pgvector:pg16). La prueba aplica
// dos veces todas las migraciones del registro y comprueba que RoleNames, que lee la vista
// access_control.v_user_roles, devuelve lo mismo que el join sobre las tablas de
// access-control al que sustituye, con datos de dos empresas.

const legacyRoleNamesSQL = `SELECT DISTINCT r.name FROM access_control.roles r
	JOIN access_control.user_roles ur ON ur.role_id = r.id
	WHERE ur.user_id = $1 AND r.status = 'active'`

func registryDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_TEST_DSN no definido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyRegistryMigrationsTwice(ctx, t, pool)
	return pool
}

// applyRegistryMigrationsTwice recorre dos veces el directorio completo: la segunda pasada
// es la que demuestra que cada migracion tolera re-ejecutarse. El candado asesor se
// mantiene hasta que acaba la prueba: otro paquete que migre la misma base mientras esta
// siembra provoca bloqueos mutuos entre el DDL y los INSERT.
func applyRegistryMigrationsTwice(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "..", "..", "migrations", "registry")
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migraciones del registro en %s: %v", dir, err)
	}
	order := func(path string) int {
		n, convErr := strconv.Atoi(strings.SplitN(filepath.Base(path), "_", 2)[0])
		if convErr != nil {
			t.Fatalf("migracion sin numero: %s", path)
		}
		return n
	}
	sort.Slice(files, func(i, j int) bool { return order(files[i]) < order(files[j]) })

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("conexion: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('registry-migrations-it'))`); err != nil {
		conn.Release()
		t.Fatalf("candado: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('registry-migrations-it'))`)
		conn.Release()
	})
	for pass := 1; pass <= 2; pass++ {
		for _, f := range files {
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("leer %s: %v", f, err)
			}
			if _, err := conn.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("pasada %d, %s: %v", pass, filepath.Base(f), err)
			}
		}
	}
}

func viewColumns(ctx context.Context, t *testing.T, pool *pgxpool.Pool, schema, view string) []string {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		  WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`, schema, view)
	if err != nil {
		t.Fatalf("columnas de %s.%s: %v", schema, view, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

func TestRoleNamesPorVistaPublicadaIgualQueJoinDirecto(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()

	if got, want := viewColumns(ctx, t, pool, "access_control", "v_user_roles"),
		[]string{"user_id", "tenant_id", "role_name"}; !slices.Equal(got, want) {
		t.Fatalf("columnas de v_user_roles = %v, se esperaba %v", got, want)
	}

	tenantA, tenantB := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM access_control.roles WHERE tenant_id = ANY($1)`, []uuid.UUID{tenantA, tenantB})
	})

	role := func(tenant uuid.UUID, name, status string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO access_control.roles (tenant_id, name, status) VALUES ($1, $2, $3) RETURNING id`,
			tenant, name, status).Scan(&id); err != nil {
			t.Fatalf("rol %s: %v", name, err)
		}
		return id
	}
	adminA, readerA, retiredA := role(tenantA, "admin", "active"), role(tenantA, "lector", "active"), role(tenantA, "baja", "inactive")
	adminB, retiredB := role(tenantB, "admin", "active"), role(tenantB, "baja", "inactive")

	userA1, userA2, userB1, userB2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	// crossTenant tiene el admin de las dos empresas: el nombre repetido sale una vez.
	crossTenant := uuid.New()
	assignments := map[uuid.UUID][]uuid.UUID{
		userA1:      {adminA, readerA, retiredA},
		userA2:      {retiredA},
		userB1:      {adminB, retiredB},
		crossTenant: {adminA, adminB},
	}
	for user, roles := range assignments {
		for _, r := range roles {
			if _, err := pool.Exec(ctx,
				`INSERT INTO access_control.user_roles (user_id, role_id) VALUES ($1, $2)`, user, r); err != nil {
				t.Fatalf("asignar rol: %v", err)
			}
		}
	}

	want := map[uuid.UUID][]string{
		userA1:      {"admin", "lector"},
		userA2:      {},
		userB1:      {"admin"},
		userB2:      {},
		crossTenant: {"admin"},
	}
	repo := NewRoleRepo(pool)
	for user, expected := range want {
		legacy := legacyRoleNames(ctx, t, pool, user)
		got, err := repo.RoleNames(ctx, user)
		if err != nil {
			t.Fatalf("RoleNames: %v", err)
		}
		sort.Strings(got)
		if !slices.Equal(got, legacy) {
			t.Fatalf("usuario %s: vista %v, join directo %v", user, got, legacy)
		}
		if !slices.Equal(got, expected) {
			t.Fatalf("usuario %s: roles %v, se esperaba %v", user, got, expected)
		}
	}
}

// TestEmpresaPorVistaPublicada comprueba que el login y el listado de sesiones leen la
// empresa por organization.v_tenants con el mismo resultado que la tabla: solo una empresa
// activa resuelve el login, y el nombre sale aunque la empresa este suspendida o vacio si
// el usuario no tiene empresa en el registro (el LEFT JOIN de antes).
func TestEmpresaPorVistaPublicada(t *testing.T) {
	pool := registryDB(t)
	ctx := context.Background()

	if got, want := viewColumns(ctx, t, pool, "organization", "v_tenants"),
		[]string{"tenant_id", "slug", "name", "status"}; !slices.Equal(got, want) {
		t.Fatalf("columnas de v_tenants = %v, se esperaba %v", got, want)
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	tenant := func(slug, name, status string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO organization.tenants (slug, name, db_name, status, cell_id)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			slug, name, "mail_tenant_"+strings.ReplaceAll(slug, "-", "_"), status, uuid.New()).Scan(&id); err != nil {
			t.Fatalf("empresa %s: %v", slug, err)
		}
		return id
	}
	activeSlug, suspendedSlug := "it-activa-"+suffix, "it-suspendida-"+suffix
	active := tenant(activeSlug, "Activa "+suffix, "active")
	suspended := tenant(suspendedSlug, "Suspendida "+suffix, "suspended")
	orphan := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM identity.users WHERE tenant_id = ANY($1)`, []uuid.UUID{active, suspended, orphan})
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM organization.tenants WHERE id = ANY($1)`, []uuid.UUID{active, suspended})
	})

	tenants := NewTenantRepo(pool)
	if got, err := tenants.GetIDBySlug(ctx, activeSlug); err != nil || got != active {
		t.Fatalf("GetIDBySlug activa = %v, %v; se esperaba %v", got, err, active)
	}
	for _, slug := range []string{suspendedSlug, "it-inexistente-" + suffix} {
		if _, err := tenants.GetIDBySlug(ctx, slug); !errors.Is(err, domain.ErrTenantNotFound) {
			t.Fatalf("GetIDBySlug %s: %v, se esperaba ErrTenantNotFound", slug, err)
		}
	}

	session := func(tenant uuid.UUID) uuid.UUID {
		t.Helper()
		var user, id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO identity.users (tenant_id, email, password_hash, first_name, last_name)
			 VALUES ($1, $2, 'x', 'Prueba', 'Vista') RETURNING id`,
			tenant, uuid.NewString()+"@example.test").Scan(&user); err != nil {
			t.Fatalf("usuario: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO identity.sessions (user_id, refresh_token_hash, expires_at)
			 VALUES ($1, $2, now() + interval '1 hour') RETURNING id`,
			user, uuid.NewString()).Scan(&id); err != nil {
			t.Fatalf("sesion: %v", err)
		}
		return id
	}
	sessions := NewSessionRepo(pool)
	want := map[uuid.UUID]string{
		session(active):    "Activa " + suffix,
		session(suspended): "Suspendida " + suffix,
		session(orphan):    "",
	}
	for id, name := range want {
		info, err := sessions.GetInfo(ctx, id)
		if err != nil {
			t.Fatalf("GetInfo %s: %v", id, err)
		}
		if info.TenantName != name {
			t.Fatalf("sesion %s: empresa %q, se esperaba %q", id, info.TenantName, name)
		}
	}

	list, total, err := sessions.ListInfo(ctx, domain.SessionFilter{TenantID: &active, Limit: 10})
	if err != nil {
		t.Fatalf("ListInfo: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].TenantName != "Activa "+suffix {
		t.Fatalf("ListInfo de la empresa activa: total %d, %d filas", total, len(list))
	}
}

func legacyRoleNames(ctx context.Context, t *testing.T, pool *pgxpool.Pool, user uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(ctx, legacyRoleNamesSQL, user)
	if err != nil {
		t.Fatalf("join directo: %v", err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
