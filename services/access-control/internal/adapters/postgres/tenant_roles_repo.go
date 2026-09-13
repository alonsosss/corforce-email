package postgres

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantRoleLifecycleRepo siembra, resiembra y retira los roles de una empresa entera. Que
// permisos recibe el rol del sistema lo decide el catalogo (alcance tenant), no el codigo.
type TenantRoleLifecycleRepo struct {
	pool *pgxpool.Pool
}

func NewTenantRoleLifecycleRepo(pool *pgxpool.Pool) *TenantRoleLifecycleRepo {
	return &TenantRoleLifecycleRepo{pool: pool}
}

// SeedSystemRole es idempotente: el rol nace con ON CONFLICT DO NOTHING y los permisos se
// conceden en una sola sentencia, asi que repetirla no cambia nada y un permiso nuevo del
// catalogo llega al rol con solo volver a llamar.
func (r *TenantRoleLifecycleRepo) SeedSystemRole(ctx context.Context, tenantID uuid.UUID, name, description string) (*domain.SystemRoleSeed, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	inserted, err := tx.Exec(ctx,
		`INSERT INTO access_control.roles (id, tenant_id, name, description, is_system, status)
		 VALUES ($1, $2, $3, $4, true, 'active')
		 ON CONFLICT (tenant_id, name) DO NOTHING`,
		uuid.New(), tenantID, name, description)
	if err != nil {
		return nil, fmt.Errorf("insertar el rol %s: %w", name, err)
	}
	role, err := scanRole(tx.QueryRow(ctx,
		`SELECT `+roleColumns+` FROM access_control.roles WHERE tenant_id = $1 AND name = $2 FOR UPDATE`,
		tenantID, name))
	if err != nil {
		return nil, fmt.Errorf("leer el rol %s: %w", name, err)
	}
	if !role.IsSystem {
		return nil, domain.ErrSystemRoleNameTaken
	}
	granted, err := tx.Exec(ctx,
		`INSERT INTO access_control.role_permissions (role_id, permission_id)
		 SELECT $1, p.id FROM access_control.permissions p WHERE p.scope = $2
		 ON CONFLICT DO NOTHING`,
		role.ID, domain.PermissionScopeTenant)
	if err != nil {
		return nil, fmt.Errorf("conceder permisos a %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &domain.SystemRoleSeed{Role: role, Created: inserted.RowsAffected() == 1, Granted: granted.RowsAffected()}, nil
}

// ReseedSystemRoles concede en una sola sentencia, a los roles del sistema de todas las
// empresas, los permisos de alcance tenant que les falten.
func (r *TenantRoleLifecycleRepo) ReseedSystemRoles(ctx context.Context, name string) (domain.SystemRoleReseed, error) {
	var out domain.SystemRoleReseed
	err := r.pool.QueryRow(ctx,
		`WITH system_roles AS (
		     SELECT id FROM access_control.roles WHERE is_system AND name = $1
		 ), granted AS (
		     INSERT INTO access_control.role_permissions (role_id, permission_id)
		     SELECT s.id, p.id FROM system_roles s CROSS JOIN access_control.permissions p
		      WHERE p.scope = $2
		     ON CONFLICT DO NOTHING
		     RETURNING 1
		 )
		 SELECT (SELECT count(*) FROM system_roles), (SELECT count(*) FROM granted)`,
		name, domain.PermissionScopeTenant).Scan(&out.Roles, &out.Granted)
	return out, err
}

// DeleteTenantRoles borra los roles de la empresa; sus permisos y asignaciones caen con
// ellos (ON DELETE CASCADE). Devuelve a quien se le retiro alguna asignacion.
func (r *TenantRoleLifecycleRepo) DeleteTenantRoles(ctx context.Context, tenantID uuid.UUID) (*domain.TenantRolesRemoval, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT DISTINCT ur.user_id
		   FROM access_control.user_roles ur
		   JOIN access_control.roles r ON r.id = ur.role_id
		  WHERE r.tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("leer las asignaciones de la empresa: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM access_control.roles WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("borrar los roles de la empresa: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &domain.TenantRolesRemoval{Roles: tag.RowsAffected(), Users: users}, nil
}
