//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
)

// El ciclo de vida de los roles de una empresa entera contra Postgres real, con las
// migraciones del registro aplicadas dos veces (registryDB): la siembra da al rol del
// sistema exactamente los permisos de alcance tenant del catalogo y ninguno de plataforma y
// es idempotente, la resiembra lleva a los roles existentes un permiso nuevo, un rol propio
// con el nombre del sistema no se siembra, la asignacion de la plataforma queda sin actor, y
// retirar los roles de una empresa se lleva sus asignaciones sin tocar las de otra.
func TestCicloDeVidaDeLosRolesDeUnaEmpresa(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	repo := NewTenantRoleLifecycleRepo(pool)
	tenantA, tenantB := uuid.New(), uuid.New()
	module := "it_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM access_control.roles WHERE tenant_id = ANY($1)`, []uuid.UUID{tenantA, tenantB})
		_, _ = pool.Exec(cctx, `DELETE FROM access_control.permissions WHERE module = $1`, module)
	})

	var tenantScope int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM access_control.permissions WHERE scope = 'tenant'`).Scan(&tenantScope); err != nil {
		t.Fatal(err)
	}
	if tenantScope == 0 {
		t.Fatal("el catalogo sembrado no tiene permisos de alcance tenant")
	}
	byScope := func(roleID uuid.UUID) (tenant, platform int64) {
		t.Helper()
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE p.scope = 'tenant'), count(*) FILTER (WHERE p.scope = 'platform')
			   FROM access_control.role_permissions rp
			   JOIN access_control.permissions p ON p.id = rp.permission_id
			  WHERE rp.role_id = $1`, roleID).Scan(&tenant, &platform); err != nil {
			t.Fatal(err)
		}
		return tenant, platform
	}

	seed, err := repo.SeedSystemRole(ctx, tenantA, "tenant_admin", "Administrador")
	if err != nil {
		t.Fatalf("siembra: %v", err)
	}
	if !seed.Created || seed.Granted != tenantScope || !seed.Role.IsSystem || seed.Role.Status != "active" {
		t.Fatalf("siembra = creado %v, %d permisos, sistema %v, estado %s; want creado con los %d de alcance tenant",
			seed.Created, seed.Granted, seed.Role.IsSystem, seed.Role.Status, tenantScope)
	}
	if tn, pl := byScope(seed.Role.ID); tn != tenantScope || pl != 0 {
		t.Fatalf("permisos del rol = %d tenant, %d plataforma; want %d y 0", tn, pl, tenantScope)
	}
	again, err := repo.SeedSystemRole(ctx, tenantA, "tenant_admin", "Administrador")
	if err != nil || again.Created || again.Granted != 0 || again.Role.ID != seed.Role.ID {
		t.Fatalf("repetir la siembra = %+v, %v; want el mismo rol sin cambios", again, err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO access_control.permissions (module, resource, action, scope)
		 VALUES ($1, 'items', 'read', 'tenant'), ($1, 'items', 'purge', 'platform')`, module); err != nil {
		t.Fatal(err)
	}
	reseed, err := repo.ReseedSystemRoles(ctx, "tenant_admin")
	if err != nil || reseed.Roles < 1 || reseed.Granted < 1 {
		t.Fatalf("resiembra = %+v, %v; want al menos un rol con el permiso nuevo", reseed, err)
	}
	if tn, pl := byScope(seed.Role.ID); tn != tenantScope+1 || pl != 0 {
		t.Fatalf("tras la resiembra = %d tenant, %d plataforma; want %d y 0", tn, pl, tenantScope+1)
	}

	var squatter uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO access_control.roles (tenant_id, name, is_system) VALUES ($1, 'tenant_admin', false) RETURNING id`,
		tenantB).Scan(&squatter); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SeedSystemRole(ctx, tenantB, "tenant_admin", "x"); !errors.Is(err, domain.ErrSystemRoleNameTaken) {
		t.Fatalf("sembrar sobre un rol propio = %v; want ErrSystemRoleNameTaken", err)
	}
	if tn, _ := byScope(squatter); tn != 0 {
		t.Fatal("el rol propio con el nombre del sistema no recibe permisos")
	}

	users := NewUserRoleRepo(pool)
	admin, member := uuid.New(), uuid.New()
	if err := users.Assign(ctx, admin, seed.Role.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if err := users.Assign(ctx, member, squatter, uuid.New()); err != nil {
		t.Fatal(err)
	}
	var assignedBy uuid.NullUUID
	if err := pool.QueryRow(ctx,
		`SELECT assigned_by FROM access_control.user_roles WHERE user_id = $1`, admin).Scan(&assignedBy); err != nil {
		t.Fatal(err)
	}
	if assignedBy.Valid {
		t.Fatalf("assigned_by = %s; una asignacion de la plataforma queda sin actor", assignedBy.UUID)
	}

	removal, err := repo.DeleteTenantRoles(ctx, tenantA)
	if err != nil || removal.Roles != 1 || len(removal.Users) != 1 || removal.Users[0] != admin {
		t.Fatalf("retirar = %+v, %v; want 1 rol y el administrador afectado", removal, err)
	}
	var left struct{ roles, grants, adminRoles, memberRoles int }
	if err := pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM access_control.roles WHERE tenant_id = $1),
		        (SELECT count(*) FROM access_control.role_permissions WHERE role_id = $2),
		        (SELECT count(*) FROM access_control.user_roles WHERE user_id = $3),
		        (SELECT count(*) FROM access_control.user_roles WHERE user_id = $4)`,
		tenantA, seed.Role.ID, admin, member).Scan(&left.roles, &left.grants, &left.adminRoles, &left.memberRoles); err != nil {
		t.Fatal(err)
	}
	if left.roles != 0 || left.grants != 0 || left.adminRoles != 0 || left.memberRoles != 1 {
		t.Fatalf("tras retirar = %+v; want nada de la empresa y la asignacion de la otra intacta", left)
	}
	if again, err := repo.DeleteTenantRoles(ctx, tenantA); err != nil || again.Roles != 0 || len(again.Users) != 0 {
		t.Fatalf("repetir = %+v, %v; want nada que retirar", again, err)
	}
	if _, err := NewRoleRepo(pool).GetByID(ctx, seed.Role.ID); !errors.Is(err, domain.ErrRoleNotFound) {
		t.Fatalf("rol retirado = %v; want ErrRoleNotFound", err)
	}
}

// access-control decide si puede retirar los roles de una empresa por la vista publicada de
// organization, nunca por su tabla.
func TestEstadoDeLaEmpresaPorLaVistaDeOrganization(t *testing.T) {
	ctx := context.Background()
	pool := registryDB(t)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	active, inactive := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organization.tenants WHERE id = ANY($1)`, []uuid.UUID{active, inactive})
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO organization.tenants (id, slug, name, db_name, status, cell_id)
		 VALUES ($1, $3, 'Activa', $4, 'active', $7), ($2, $5, 'Inactiva', $6, 'inactive', $7)`,
		active, inactive, "it-a-"+suffix, "mail_tenant_it_a_"+suffix, "it-b-"+suffix, "mail_tenant_it_b_"+suffix, uuid.New()); err != nil {
		t.Fatal(err)
	}
	dir := NewTenantDirectory(pool)
	for id, want := range map[uuid.UUID]bool{active: true, inactive: false, uuid.New(): false} {
		if got, err := dir.IsActive(ctx, id); err != nil || got != want {
			t.Errorf("IsActive(%s) = %v, %v; want %v", id, got, err, want)
		}
	}
}
