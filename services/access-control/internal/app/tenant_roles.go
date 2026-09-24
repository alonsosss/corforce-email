package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/google/uuid"
)

// tenantAdminDescription es la descripcion con la que nace el rol del sistema de cada empresa.
const tenantAdminDescription = "Administrador del tenant: usuarios, dominios y políticas de su propia organización"

// TenantRolesDeps agrupa los puertos de TenantRolesUseCase.
type TenantRolesDeps struct {
	Lifecycle ports.TenantRoleLifecycle
	Roles     ports.RoleRepository
	// UserRoles es el mismo repositorio que usa RBACUseCase (con cache si hay Redis):
	// asignar y retirar invalidan la politica del usuario.
	UserRoles ports.UserRoleRepository
	// Cache es nil sin Redis.
	Cache       ports.PolicyCache
	Tenants     ports.TenantDirectory
	SystemRoles SystemRoles
}

// TenantRolesUseCase son las operaciones de roles sobre una empresa entera que organization
// orquesta al darla de alta y de baja. Solo existen en la API interna: no las pide ninguna
// persona, y por eso no pasan por la regla de "solo concedes lo que tienes", que protege a
// una empresa de sus propios usuarios.
type TenantRolesUseCase struct {
	lifecycle   ports.TenantRoleLifecycle
	roles       ports.RoleRepository
	userRoles   ports.UserRoleRepository
	cache       ports.PolicyCache
	tenants     ports.TenantDirectory
	systemRoles SystemRoles
}

func NewTenantRolesUseCase(d TenantRolesDeps) *TenantRolesUseCase {
	return &TenantRolesUseCase{
		lifecycle:   d.Lifecycle,
		roles:       d.Roles,
		userRoles:   d.UserRoles,
		cache:       d.Cache,
		tenants:     d.Tenants,
		systemRoles: d.SystemRoles,
	}
}

// SeedSystemRole siembra el rol tenant_admin de la empresa con todos los permisos de alcance
// tenant del catalogo.
func (uc *TenantRolesUseCase) SeedSystemRole(ctx context.Context, tenantID uuid.UUID) (*domain.SystemRoleSeed, error) {
	return uc.lifecycle.SeedSystemRole(ctx, tenantID, uc.systemRoles.TenantAdmin, tenantAdminDescription)
}

// ReseedSystemRoles concede a los roles del sistema de todas las empresas los permisos de
// alcance tenant que les falten.
func (uc *TenantRolesUseCase) ReseedSystemRoles(ctx context.Context) (domain.SystemRoleReseed, error) {
	return uc.lifecycle.ReseedSystemRoles(ctx, uc.systemRoles.TenantAdmin)
}

// AssignRole asigna un rol de la empresa a un usuario en nombre de la plataforma (sin actor).
// Un rol de otra empresa se trata como inexistente.
func (uc *TenantRolesUseCase) AssignRole(ctx context.Context, tenantID, userID, roleID uuid.UUID) error {
	role, err := uc.roles.GetByID(ctx, roleID)
	if err != nil {
		return err
	}
	if role.TenantID != tenantID {
		return domain.ErrRoleNotFound
	}
	return uc.userRoles.Assign(ctx, userID, roleID, uuid.Nil)
}

// RevokeRole retira la asignacion. Un rol que ya no existe no deja nada que retirar: la
// compensacion que la pide puede repetirse despues de que los roles de la empresa se borraran.
func (uc *TenantRolesUseCase) RevokeRole(ctx context.Context, tenantID, userID, roleID uuid.UUID) error {
	role, err := uc.roles.GetByID(ctx, roleID)
	if errors.Is(err, domain.ErrRoleNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if role.TenantID != tenantID {
		return nil
	}
	return uc.userRoles.Revoke(ctx, userID, roleID)
}

// RemoveTenantRoles borra todos los roles de una empresa que no esta activa, con sus
// permisos y asignaciones, y descarta la politica cacheada de quienes los tenian. Una
// empresa activa se rechaza: borrar sus roles la dejaria sin administracion.
func (uc *TenantRolesUseCase) RemoveTenantRoles(ctx context.Context, tenantID uuid.UUID) (*domain.TenantRolesRemoval, error) {
	active, err := uc.tenants.IsActive(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("estado de la empresa: %w", err)
	}
	if active {
		return nil, domain.ErrTenantActive
	}
	removal, err := uc.lifecycle.DeleteTenantRoles(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if uc.cache != nil && len(removal.Users) > 0 {
		uc.cache.InvalidateUsers(ctx, removal.Users)
	}
	return removal, nil
}
