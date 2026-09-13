package postgres

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// RoleSeeder siembra el rol de sistema de cada tenant.
//
// Solo se siembra tenant_admin: es el unico rol que existe antes de que el tenant
// tenga ningun permiso propio, y de el nace el resto de la administracion. Recibe
// TODOS los permisos del catalogo de alcance 'tenant'; los de alcance 'platform' son del
// superadmin, que es una cuenta de plataforma y no se siembra por tenant. Que permiso
// tiene el rol y cual es de plataforma son datos del catalogo access_control.permissions,
// nunca una lista en el codigo.
type RoleSeeder struct {
	pool *pgxpool.Pool
}

func NewRoleSeeder(pool *pgxpool.Pool) *RoleSeeder {
	return &RoleSeeder{pool: pool}
}

const tenantAdminDescription = "Administrador del tenant: usuarios, dominios y politicas de su propia organizacion"

// SeedDefaultRoles es idempotente: el rol se inserta con ON CONFLICT DO NOTHING y los
// permisos se vuelven a conceder en una sola sentencia, de modo que un permiso nuevo del
// catalogo llega al rol con solo volver a llamar.
func (s *RoleSeeder) SeedDefaultRoles(ctx context.Context, tenantID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`INSERT INTO access_control.roles (id, tenant_id, name, description, is_system, status)
 VALUES ($1, $2, $3, $4, true, 'active')
 ON CONFLICT (tenant_id, name) DO NOTHING`,
		uuid.New(), tenantID, middleware.RoleTenantAdmin, tenantAdminDescription,
	); err != nil {
		return fmt.Errorf("insertar rol %s: %w", middleware.RoleTenantAdmin, err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO access_control.role_permissions (role_id, permission_id)
 SELECT r.id, p.id
   FROM access_control.roles r
   CROSS JOIN access_control.permissions p
  WHERE r.tenant_id = $1 AND r.name = $2
    AND p.scope = 'tenant'
 ON CONFLICT DO NOTHING`,
		tenantID, middleware.RoleTenantAdmin,
	); err != nil {
		return fmt.Errorf("conceder permisos a %s: %w", middleware.RoleTenantAdmin, err)
	}
	return tx.Commit(ctx)
}

// AdminUserSeeder crea el primer administrador del tenant. La contrasena llega en claro
// desde la peticion de alta, se guarda solo como hash bcrypt y no se registra nunca.
type AdminUserSeeder struct {
	pool *pgxpool.Pool
}

func NewAdminUserSeeder(pool *pgxpool.Pool) *AdminUserSeeder {
	return &AdminUserSeeder{pool: pool}
}

func (s *AdminUserSeeder) CreateAdminUser(ctx context.Context, tenantID uuid.UUID, email, password, firstName, lastName string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash de contrasena: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Sin ON CONFLICT: el tenant acaba de nacer, asi que un correo repetido es un
	// fallo real y no algo que silenciar dejando la cuenta sin rol.
	userID := uuid.New()
	if _, err := tx.Exec(ctx,
		`INSERT INTO identity.users (id, tenant_id, email, password_hash, first_name, last_name, status, mfa_enabled)
 VALUES ($1, $2, $3, $4, $5, $6, 'active', false)`,
		userID, tenantID, email, string(hash), firstName, lastName,
	); err != nil {
		return fmt.Errorf("insertar administrador: %w", err)
	}

	tag, err := tx.Exec(ctx,
		`INSERT INTO access_control.user_roles (user_id, role_id)
 SELECT $1, r.id FROM access_control.roles r
 WHERE r.tenant_id = $2 AND r.name = $3
 ON CONFLICT DO NOTHING`,
		userID, tenantID, middleware.RoleTenantAdmin,
	)
	if err != nil {
		return fmt.Errorf("asignar rol de administrador: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("el rol %s no existe en el tenant: siembra los roles antes del administrador", middleware.RoleTenantAdmin)
	}
	return tx.Commit(ctx)
}
