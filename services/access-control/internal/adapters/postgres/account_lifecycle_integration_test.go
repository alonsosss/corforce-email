//go:build integration

package postgres

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Estado efectivo que publica identity y retirada de los roles de una cuenta borrada, sobre el
// registro real (ACCESS_CONTROL_TEST_DSN). Los bloqueos se fijan a un dia del now() de la base:
// nada depende de la fecha real.

func insertRole(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID, name string, perm *uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO access_control.roles (tenant_id, name, status) VALUES ($1, $2, 'active') RETURNING id`,
		tenant, name).Scan(&id); err != nil {
		t.Fatalf("rol %s: %v", name, err)
	}
	if perm != nil {
		if _, err := pool.Exec(ctx,
			`INSERT INTO access_control.role_permissions (role_id, permission_id) VALUES ($1, $2)`, id, *perm); err != nil {
			t.Fatalf("permiso de %s: %v", name, err)
		}
	}
	return id
}

func assignRoles(ctx context.Context, t *testing.T, pool *pgxpool.Pool, user uuid.UUID, roles ...uuid.UUID) {
	t.Helper()
	for _, r := range roles {
		if _, err := pool.Exec(ctx, `INSERT INTO access_control.user_roles (user_id, role_id) VALUES ($1, $2)`, user, r); err != nil {
			t.Fatalf("asignar rol: %v", err)
		}
	}
}

func assignedRoles(ctx context.Context, t *testing.T, pool *pgxpool.Pool, user uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT role_id FROM access_control.user_roles WHERE user_id = $1 ORDER BY role_id`, user)
	if err != nil {
		t.Fatal(err)
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

// Un bloqueo por intentos que ya vencio no cierra la sesion ni saca a la cuenta de los avisos;
// uno vigente, si.
func TestUnBloqueoVencidoCuentaComoActivo(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	tenant := uuid.New()
	module := "it_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM access_control.roles WHERE tenant_id = $1`, tenant)
		_, _ = pool.Exec(cctx, `DELETE FROM access_control.permissions WHERE module = $1`, module)
		_, _ = pool.Exec(cctx, `DELETE FROM identity.users WHERE tenant_id = $1`, tenant)
	})
	var perm uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO access_control.permissions (module, resource, action) VALUES ($1, 'items', 'update') RETURNING id`,
		module).Scan(&perm); err != nil {
		t.Fatal(err)
	}
	manager := insertRole(ctx, t, pool, tenant, "gestor", &perm)

	account := func(status, lockedUntil string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO identity.users (tenant_id, email, password_hash, first_name, last_name, status, locked_until)
			 VALUES ($1, $2, 'x', 'Prueba', 'Bloqueo', $3, `+lockedUntil+`) RETURNING id`,
			tenant, uuid.NewString()+"@example.test", status).Scan(&id); err != nil {
			t.Fatalf("cuenta %s: %v", status, err)
		}
		assignRoles(ctx, t, pool, id, manager)
		return id
	}
	cases := []struct {
		name   string
		id     uuid.UUID
		status string
		active bool
	}{
		{"bloqueo vigente", account("locked", "now() + interval '1 day'"), "locked", false},
		{"bloqueo vencido", account("locked", "now() - interval '1 day'"), "active", true},
		{"bloqueada sin fecha", account("locked", "NULL"), "active", true},
		{"activa", account("active", "NULL"), "active", true},
		{"pendiente", account("pending", "NULL"), "pending", false},
	}

	repo := NewUserRoleRepo(pool)
	var notified []uuid.UUID
	for _, c := range cases {
		got, err := repo.UserAccount(ctx, c.id, tenant)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Status != c.status || got.Active() != c.active {
			t.Errorf("%s: estado %q activo=%v, se esperaba %q activo=%v", c.name, got.Status, got.Active(), c.status, c.active)
		}
		if c.active {
			notified = append(notified, c.id)
		}
	}
	got, err := repo.ListUsersWithPermission(ctx, tenant, module, "update")
	if err != nil {
		t.Fatal(err)
	}
	sortUUIDs(got)
	sortUUIDs(notified)
	if !slices.Equal(got, notified) {
		t.Fatalf("usuarios con el permiso %v, se esperaba %v", got, notified)
	}
}

// Las asignaciones de una cuenta borrada se retiran solo en su empresa y solo si la cuenta ya
// no existe en ella; repetirlo, o hacerlo despues de la baja de la empresa, no hace nada.
func TestRetirarLosRolesDeUnaCuentaBorrada(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	tenantA, tenantB := uuid.New(), uuid.New()
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM access_control.roles WHERE tenant_id = ANY($1)`, []uuid.UUID{tenantA, tenantB})
		_, _ = pool.Exec(cctx, `DELETE FROM identity.users WHERE tenant_id = ANY($1)`, []uuid.UUID{tenantA, tenantB})
	})
	adminA, readerA := insertRole(ctx, t, pool, tenantA, "admin", nil), insertRole(ctx, t, pool, tenantA, "lector", nil)
	adminB := insertRole(ctx, t, pool, tenantB, "admin", nil)

	deleted := uuid.New()
	assignRoles(ctx, t, pool, deleted, adminA, readerA, adminB)
	var alive uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO identity.users (tenant_id, email, password_hash, first_name, last_name)
		 VALUES ($1, $2, 'x', 'Prueba', 'Viva') RETURNING id`, tenantA, uuid.NewString()+"@example.test").Scan(&alive); err != nil {
		t.Fatal(err)
	}
	assignRoles(ctx, t, pool, alive, readerA)

	repo := NewDeletedAccountRepo(pool)
	remove := func(tenant, user uuid.UUID) int64 {
		t.Helper()
		n, err := repo.RemoveRolesOfDeletedUser(ctx, tenant, user)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := remove(tenantA, deleted); n != 2 {
		t.Fatalf("asignaciones retiradas en la empresa A: %d, se esperaban 2", n)
	}
	if got := assignedRoles(ctx, t, pool, deleted); !slices.Equal(got, []uuid.UUID{adminB}) {
		t.Fatalf("quedan %v, se esperaba solo el rol de la empresa B", got)
	}
	if n := remove(tenantA, deleted); n != 0 {
		t.Fatalf("reentrega: %d, se esperaba 0", n)
	}
	if n := remove(tenantA, alive); n != 0 || !slices.Equal(assignedRoles(ctx, t, pool, alive), []uuid.UUID{readerA}) {
		t.Fatalf("una cuenta que existe conserva sus roles: retiradas %d", n)
	}
	if n := remove(tenantA, uuid.New()); n != 0 {
		t.Fatalf("cuenta desconocida: %d", n)
	}

	if _, err := NewTenantRoleLifecycleRepo(pool).DeleteTenantRoles(ctx, tenantB); err != nil {
		t.Fatal(err)
	}
	if n := remove(tenantB, deleted); n != 0 {
		t.Fatalf("tras la baja de la empresa: %d, se esperaba 0", n)
	}
	if got := assignedRoles(ctx, t, pool, deleted); len(got) != 0 {
		t.Fatalf("quedan asignaciones %v", got)
	}
}
