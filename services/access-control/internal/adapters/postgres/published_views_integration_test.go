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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ACCESS_CONTROL_TEST_DSN apunta a un Postgres limpio (pgvector/pgvector:pg16). La prueba
// aplica dos veces todas las migraciones del registro y comprueba que TokensValidFrom y
// ListUsersWithPermission, que leen la vista identity.v_user_status, devuelven lo mismo
// que las consultas sobre identity.users a las que sustituyen, con datos de dos empresas.

const (
	legacyTokensValidFromSQL = `SELECT tokens_valid_from FROM identity.users WHERE id = $1`

	legacyUsersWithPermissionSQL = `SELECT DISTINCT ur.user_id
		   FROM access_control.permissions p
		   JOIN access_control.role_permissions rp ON rp.permission_id = p.id
		   JOIN access_control.user_roles ur ON ur.role_id = rp.role_id
		   JOIN access_control.roles r ON r.id = ur.role_id
		   JOIN identity.users u ON u.id = ur.user_id
		  WHERE p.module = $1 AND p.action = $2
		    AND r.tenant_id = $3 AND r.status = 'active' AND u.tenant_id = $3 AND u.status = 'active'`
)

func registryDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := integrationEnv(t, "ACCESS_CONTROL_TEST_DSN")
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

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

type registryFixture struct {
	pool             *pgxpool.Pool
	tenantA, tenantB uuid.UUID
	module           string
	users            map[string]uuid.UUID
}

func seedRegistry(ctx context.Context, t *testing.T, pool *pgxpool.Pool) *registryFixture {
	t.Helper()
	f := &registryFixture{
		pool: pool, tenantA: uuid.New(), tenantB: uuid.New(),
		module: "it_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
		users:  map[string]uuid.UUID{},
	}
	tenants := []uuid.UUID{f.tenantA, f.tenantB}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM access_control.roles WHERE tenant_id = ANY($1)`, tenants)
		_, _ = pool.Exec(cctx, `DELETE FROM access_control.permissions WHERE module = $1`, f.module)
		_, _ = pool.Exec(cctx, `DELETE FROM identity.users WHERE tenant_id = ANY($1)`, tenants)
	})

	var perm uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO access_control.permissions (module, resource, action) VALUES ($1, 'items', 'update') RETURNING id`,
		f.module).Scan(&perm); err != nil {
		t.Fatalf("permiso: %v", err)
	}

	// Cada usuario con su empresa, estado y epoch de revocacion propios, para que una
	// consulta que confunda filas se note en el resultado.
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	userSpecs := []struct {
		key    string
		tenant uuid.UUID
		status string
	}{
		{"a_activo", f.tenantA, "active"},
		{"a_inactivo", f.tenantA, "inactive"},
		{"a_sin_permiso", f.tenantA, "active"},
		{"b_activo", f.tenantB, "active"},
		{"b_pendiente", f.tenantB, "pending"},
		{"b_rol_inactivo", f.tenantB, "active"},
		{"b_con_rol_de_a", f.tenantB, "active"},
	}
	for i, u := range userSpecs {
		id := uuid.New()
		if _, err := pool.Exec(ctx,
			`INSERT INTO identity.users (id, tenant_id, email, password_hash, first_name, last_name, status, tokens_valid_from)
			 VALUES ($1, $2, $3, 'x', 'Prueba', 'Registro', $4, $5)`,
			id, u.tenant, u.key+"@pruebas.local", u.status, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("usuario %s: %v", u.key, err)
		}
		f.users[u.key] = id
	}

	role := func(tenant uuid.UUID, name, status string, withPermission bool) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO access_control.roles (tenant_id, name, status) VALUES ($1, $2, $3) RETURNING id`,
			tenant, name, status).Scan(&id); err != nil {
			t.Fatalf("rol %s: %v", name, err)
		}
		if withPermission {
			if _, err := pool.Exec(ctx,
				`INSERT INTO access_control.role_permissions (role_id, permission_id) VALUES ($1, $2)`, id, perm); err != nil {
				t.Fatalf("permiso de %s: %v", name, err)
			}
		}
		return id
	}
	managerA := role(f.tenantA, "gestor", "active", true)
	otherA := role(f.tenantA, "otro", "active", false)
	managerB := role(f.tenantB, "gestor", "active", true)
	retiredB := role(f.tenantB, "baja", "inactive", true)

	for key, roleID := range map[string]uuid.UUID{
		"a_activo": managerA, "a_inactivo": managerA, "a_sin_permiso": otherA,
		"b_activo": managerB, "b_pendiente": managerB, "b_rol_inactivo": retiredB,
		"b_con_rol_de_a": managerA,
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO access_control.user_roles (user_id, role_id) VALUES ($1, $2)`, f.users[key], roleID); err != nil {
			t.Fatalf("asignar rol a %s: %v", key, err)
		}
	}
	return f
}

func TestVistaUserStatusPublicaSoloLoNecesario(t *testing.T) {
	pool := registryDB(t)
	rows, err := pool.Query(context.Background(),
		`SELECT column_name FROM information_schema.columns
		  WHERE table_schema = 'identity' AND table_name = 'v_user_status' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
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
	if want := []string{"user_id", "tenant_id", "status", "tokens_valid_from"}; !slices.Equal(cols, want) {
		t.Fatalf("columnas de v_user_status = %v, se esperaba %v", cols, want)
	}
}

func TestTokensValidFromPorVistaIgualQueTabla(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	f := seedRegistry(ctx, t, pool)
	repo := NewUserRoleRepo(pool)

	seen := map[time.Time]bool{}
	for key, id := range f.users {
		var legacy time.Time
		if err := pool.QueryRow(ctx, legacyTokensValidFromSQL, id).Scan(&legacy); err != nil {
			t.Fatalf("%s por la tabla: %v", key, err)
		}
		got, err := repo.TokensValidFrom(ctx, id)
		if err != nil {
			t.Fatalf("%s por la vista: %v", key, err)
		}
		if !got.Equal(legacy) {
			t.Fatalf("%s: vista %s, tabla %s", key, got, legacy)
		}
		seen[got.UTC()] = true
	}
	if len(seen) != len(f.users) {
		t.Fatalf("se esperaban %d epochs distintos, hubo %d", len(f.users), len(seen))
	}

	missing := uuid.New()
	legacyErr := pool.QueryRow(ctx, legacyTokensValidFromSQL, missing).Scan(new(time.Time))
	_, err := repo.TokensValidFrom(ctx, missing)
	if !errors.Is(legacyErr, pgx.ErrNoRows) || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("usuario inexistente: vista err=%v, tabla err=%v; ambas debian ser pgx.ErrNoRows", err, legacyErr)
	}
}

func TestListUsersWithPermissionPorVistaIgualQueTabla(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	f := seedRegistry(ctx, t, pool)
	repo := NewUserRoleRepo(pool)

	// Un rol inactivo no concede nada: la referencia de tabla lleva el mismo filtro de
	// estado, asi que la comparacion solo prueba el cambio a la vista.
	cases := map[string]struct {
		tenant uuid.UUID
		want   []uuid.UUID
	}{
		"empresa A":     {f.tenantA, []uuid.UUID{f.users["a_activo"]}},
		"empresa B":     {f.tenantB, []uuid.UUID{f.users["b_activo"]}},
		"empresa vacia": {uuid.New(), []uuid.UUID{}},
	}
	for name, tc := range cases {
		legacy := collectUserIDs(t, pool, legacyUsersWithPermissionSQL, f.module, "update", tc.tenant)
		got, err := repo.ListUsersWithPermission(ctx, tc.tenant, f.module, "update")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		sortUUIDs(got)
		sortUUIDs(tc.want)
		if !slices.Equal(got, legacy) {
			t.Fatalf("%s: vista %v, tabla %v", name, got, legacy)
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%s: usuarios %v, se esperaba %v", name, got, tc.want)
		}
	}
}

func collectUserIDs(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("consulta anterior: %v", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	sortUUIDs(out)
	return out
}

func sortUUIDs(ids []uuid.UUID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
}

// Desactivar un rol retira sus permisos: la politica efectiva (roles, permisos, modulos y
// acciones de escritura) no cuenta un rol inactivo.
func TestUnRolInactivoNoConcedeNada(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	f := seedRegistry(ctx, t, pool)
	repo := NewUserRoleRepo(pool)

	inactivo, activo := f.users["b_rol_inactivo"], f.users["b_activo"]
	roles, err := repo.ListRoles(ctx, inactivo, f.tenantB)
	if err != nil || len(roles) != 0 {
		t.Fatalf("roles del usuario con rol inactivo = %v, err %v; se esperaba ninguno", roles, err)
	}
	perms, err := repo.ListPermissions(ctx, inactivo, f.tenantB)
	if err != nil || len(perms) != 0 {
		t.Fatalf("permisos = %v, err %v; se esperaba ninguno", perms, err)
	}
	mods, err := repo.ListAccessibleModules(ctx, inactivo, f.tenantB)
	if err != nil || len(mods) != 0 {
		t.Fatalf("modulos = %v, err %v; se esperaba ninguno", mods, err)
	}
	writes, err := repo.ListWriteActionsByModule(ctx, inactivo, f.tenantB)
	if err != nil || len(writes) != 0 {
		t.Fatalf("acciones de escritura = %v, err %v; se esperaba ninguna", writes, err)
	}
	mods, err = repo.ListAccessibleModules(ctx, activo, f.tenantB)
	if err != nil || !slices.Contains(mods, f.module) {
		t.Fatalf("el usuario con rol activo debe conservar el modulo %s: %v, err %v", f.module, mods, err)
	}
}
