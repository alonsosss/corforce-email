package postgres

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const roleColumns = `id, tenant_id, name, description, is_system, status, created_at, updated_at`

func scanRole(row pgx.Row) (*domain.Role, error) {
	role := &domain.Role{}
	if err := row.Scan(&role.ID, &role.TenantID, &role.Name, &role.Description, &role.IsSystem, &role.Status, &role.CreatedAt, &role.UpdatedAt); err != nil {
		return nil, err
	}
	return role, nil
}

func collectRoles(rows pgx.Rows) ([]*domain.Role, error) {
	defer rows.Close()
	roles := make([]*domain.Role, 0)
	for rows.Next() {
		role, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

const permissionColumns = `id, module, resource, action, description, scope`

func collectPermissions(rows pgx.Rows) ([]*domain.Permission, error) {
	defer rows.Close()
	perms := make([]*domain.Permission, 0)
	for rows.Next() {
		p := &domain.Permission{}
		if err := rows.Scan(&p.ID, &p.Module, &p.Resource, &p.Action, &p.Description, &p.Scope); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, rows.Err()
}

type RoleRepo struct {
	pool *pgxpool.Pool
}

func NewRoleRepo(pool *pgxpool.Pool) *RoleRepo {
	return &RoleRepo{pool: pool}
}

func (r *RoleRepo) Create(ctx context.Context, role *domain.Role) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO access_control.roles (id, tenant_id, name, description, is_system, status)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		role.ID, role.TenantID, role.Name, role.Description, role.IsSystem, role.Status,
	)
	return err
}

func (r *RoleRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Role, error) {
	role, err := scanRole(r.pool.QueryRow(ctx,
		`SELECT `+roleColumns+` FROM access_control.roles WHERE id = $1`, id))
	if err != nil {
		return nil, domain.ErrRoleNotFound
	}
	return role, nil
}

func (r *RoleRepo) GetByName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Role, error) {
	role, err := scanRole(r.pool.QueryRow(ctx,
		`SELECT `+roleColumns+` FROM access_control.roles WHERE tenant_id = $1 AND name = $2`, tenantID, name))
	if err != nil {
		return nil, domain.ErrRoleNotFound
	}
	return role, nil
}

func (r *RoleRepo) List(ctx context.Context, tenantID uuid.UUID) ([]*domain.Role, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+roleColumns+` FROM access_control.roles WHERE tenant_id = $1 ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	return collectRoles(rows)
}

func (r *RoleRepo) Update(ctx context.Context, role *domain.Role) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE access_control.roles SET name = $1, description = $2, status = $3 WHERE id = $4`,
		role.Name, role.Description, role.Status, role.ID,
	)
	return err
}

func (r *RoleRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM access_control.roles WHERE id = $1 AND is_system = FALSE`, id)
	return err
}

type PermissionRepo struct {
	pool *pgxpool.Pool
}

func NewPermissionRepo(pool *pgxpool.Pool) *PermissionRepo {
	return &PermissionRepo{pool: pool}
}

func (r *PermissionRepo) List(ctx context.Context) ([]*domain.Permission, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+permissionColumns+` FROM access_control.permissions ORDER BY module, resource, action`)
	if err != nil {
		return nil, err
	}
	return collectPermissions(rows)
}

func (r *PermissionRepo) ListByModule(ctx context.Context, module string) ([]*domain.Permission, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+permissionColumns+` FROM access_control.permissions WHERE module = $1 ORDER BY resource, action`, module)
	if err != nil {
		return nil, err
	}
	return collectPermissions(rows)
}

func (r *PermissionRepo) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]*domain.Permission, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+permissionColumns+` FROM access_control.permissions WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	return collectPermissions(rows)
}

type RolePermissionRepo struct {
	pool *pgxpool.Pool
}

func NewRolePermissionRepo(pool *pgxpool.Pool) *RolePermissionRepo {
	return &RolePermissionRepo{pool: pool}
}

func (r *RolePermissionRepo) ListPermissions(ctx context.Context, roleID uuid.UUID) ([]*domain.Permission, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT p.id, p.module, p.resource, p.action, p.description, p.scope
		   FROM access_control.permissions p
		   JOIN access_control.role_permissions rp ON rp.permission_id = p.id
		  WHERE rp.role_id = $1
		  ORDER BY p.module, p.resource, p.action`, roleID)
	if err != nil {
		return nil, err
	}
	return collectPermissions(rows)
}

func (r *RolePermissionRepo) ReplaceAll(ctx context.Context, roleID uuid.UUID, permIDs []uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, `DELETE FROM access_control.role_permissions WHERE role_id = $1`, roleID); err != nil {
		return err
	}

	for _, pid := range permIDs {
		if _, err = tx.Exec(ctx,
			`INSERT INTO access_control.role_permissions (role_id, permission_id) VALUES ($1, $2)`,
			roleID, pid,
		); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

type UserRoleRepo struct {
	pool *pgxpool.Pool
}

func NewUserRoleRepo(pool *pgxpool.Pool) *UserRoleRepo {
	return &UserRoleRepo{pool: pool}
}

// TokensValidFrom lee la columna que identity adelanta al revocar todas las sesiones de
// un usuario. Se lee de identity.users, en la misma base de registro, sin clave foranea.
func (r *UserRoleRepo) TokensValidFrom(ctx context.Context, userID uuid.UUID) (time.Time, error) {
	var validFrom time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT tokens_valid_from FROM identity.users WHERE id = $1`, userID).Scan(&validFrom)
	return validFrom, err
}

func (r *UserRoleRepo) Assign(ctx context.Context, userID, roleID, assignedBy uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO access_control.user_roles (user_id, role_id, assigned_by)
		 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		userID, roleID, assignedBy,
	)
	return err
}

func (r *UserRoleRepo) Revoke(ctx context.Context, userID, roleID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM access_control.user_roles WHERE user_id = $1 AND role_id = $2`,
		userID, roleID,
	)
	return err
}

func (r *UserRoleRepo) ListRoles(ctx context.Context, userID, tenantID uuid.UUID) ([]*domain.Role, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT r.id, r.tenant_id, r.name, r.description, r.is_system, r.status, r.created_at, r.updated_at
		   FROM access_control.roles r
		   JOIN access_control.user_roles ur ON ur.role_id = r.id
		  WHERE ur.user_id = $1 AND r.tenant_id = $2
		  ORDER BY r.name`, userID, tenantID)
	if err != nil {
		return nil, err
	}
	return collectRoles(rows)
}

func (r *UserRoleRepo) ListPermissions(ctx context.Context, userID, tenantID uuid.UUID) ([]*domain.Permission, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT p.id, p.module, p.resource, p.action, p.description, p.scope
		   FROM access_control.permissions p
		   JOIN access_control.role_permissions rp ON rp.permission_id = p.id
		   JOIN access_control.user_roles ur ON ur.role_id = rp.role_id
		   JOIN access_control.roles r ON r.id = ur.role_id
		  WHERE ur.user_id = $1 AND r.tenant_id = $2
		  ORDER BY p.module, p.resource, p.action`, userID, tenantID)
	if err != nil {
		return nil, err
	}
	return collectPermissions(rows)
}

func (r *UserRoleRepo) ListAccessibleModules(ctx context.Context, userID, tenantID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT p.module
		   FROM access_control.permissions p
		   JOIN access_control.role_permissions rp ON rp.permission_id = p.id
		   JOIN access_control.user_roles ur ON ur.role_id = rp.role_id
		   JOIN access_control.roles r ON r.id = ur.role_id
		  WHERE ur.user_id = $1 AND r.tenant_id = $2
		  ORDER BY p.module`, userID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	modules := make([]string, 0)
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		modules = append(modules, m)
	}
	return modules, rows.Err()
}

// ListWriteActionsByModule excluye read y export: son las dos acciones que nunca
// modifican estado y las unicas que el gateway deja pasar sin gatear por accion.
func (r *UserRoleRepo) ListWriteActionsByModule(ctx context.Context, userID, tenantID uuid.UUID) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT p.module, p.action
		   FROM access_control.permissions p
		   JOIN access_control.role_permissions rp ON rp.permission_id = p.id
		   JOIN access_control.user_roles ur ON ur.role_id = rp.role_id
		   JOIN access_control.roles r ON r.id = ur.role_id
		  WHERE ur.user_id = $1 AND r.tenant_id = $2 AND p.action NOT IN ('read', 'export')
		  ORDER BY p.module, p.action`, userID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var module, action string
		if err := rows.Scan(&module, &action); err != nil {
			return nil, err
		}
		result[module] = append(result[module], action)
	}
	return result, rows.Err()
}

func (r *UserRoleRepo) GetAccessPolicy(ctx context.Context, userID, tenantID uuid.UUID) (*domain.AccessPolicy, error) {
	roles, err := r.ListRoles(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}

	perms, err := r.ListPermissions(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}

	policy := &domain.AccessPolicy{
		UserID:      userID,
		TenantID:    tenantID,
		Roles:       make([]domain.Role, len(roles)),
		Permissions: make([]domain.Permission, len(perms)),
	}
	for i, role := range roles {
		policy.Roles[i] = *role
	}
	for i, p := range perms {
		policy.Permissions[i] = *p
	}
	return policy, nil
}

// ListUsersWithPermission devuelve los usuarios activos de una empresa que tienen un
// permiso concreto, resuelto por sus roles. Existe para poder avisar a quien corresponde
// sin que el servicio que avisa tenga que conocer el modelo de roles: notificar "a los
// que gestionan dominios" es preguntar por quien tiene domains/update, no por un rol con
// un nombre concreto (que cada empresa puede haber renombrado).
func (r *UserRoleRepo) ListUsersWithPermission(ctx context.Context, tenantID uuid.UUID, module, action string) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT ur.user_id
		   FROM access_control.permissions p
		   JOIN access_control.role_permissions rp ON rp.permission_id = p.id
		   JOIN access_control.user_roles ur ON ur.role_id = rp.role_id
		   JOIN access_control.roles r ON r.id = ur.role_id
		   JOIN identity.users u ON u.id = ur.user_id
		  WHERE p.module = $1 AND p.action = $2
		    AND r.tenant_id = $3 AND u.tenant_id = $3 AND u.status = 'active'`,
		module, action, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]uuid.UUID, 0, 8)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
