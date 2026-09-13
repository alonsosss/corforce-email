package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/alonsosss/corforce-email/services/access-control/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SystemRoles son los nombres de los dos roles estructurales de la plataforma. Llegan
// desde el cableado (pkg/middleware) para que el caso de uso no dependa del paquete
// HTTP; que permiso tiene cada uno sigue siendo un dato de la base.
type SystemRoles struct {
	// Superadmin opera la plataforma entera: transciende el catalogo de modulos.
	Superadmin string
	// TenantAdmin administra su propia empresa: es_admin, pero queda sujeto a los
	// modulos que la empresa tenga contratados.
	TenantAdmin string
}

// Actor es quien pide un cambio de roles o permisos. Privileged es un rol del sistema
// (superadmin o tenant_admin): los demas solo conceden lo que ellos mismos tienen, para
// que un permiso de gestion de roles no se convierta en una escalada a administrador.
type Actor struct {
	UserID     uuid.UUID
	Privileged bool
}

type RBACUseCase struct {
	roles       ports.RoleRepository
	perms       ports.PermissionRepository
	rolePerms   ports.RolePermissionRepository
	userRoles   ports.UserRoleRepository
	denials     ports.DenialRepository
	moduleGate  ports.TenantModuleGate
	systemRoles SystemRoles
	logger      *zap.Logger
}

func NewRBACUseCase(
	roles ports.RoleRepository,
	perms ports.PermissionRepository,
	rolePerms ports.RolePermissionRepository,
	userRoles ports.UserRoleRepository,
	denials ports.DenialRepository,
	moduleGate ports.TenantModuleGate,
	systemRoles SystemRoles,
	logger *zap.Logger,
) *RBACUseCase {
	return &RBACUseCase{
		roles:       roles,
		perms:       perms,
		rolePerms:   rolePerms,
		userRoles:   userRoles,
		denials:     denials,
		moduleGate:  moduleGate,
		systemRoles: systemRoles,
		logger:      logger,
	}
}

func (uc *RBACUseCase) RecordDenial(ctx context.Context, d *domain.AccessDenial) error {
	return uc.denials.Record(ctx, d)
}

// DenialMetrics resume los accesos denegados de un tenant en una ventana de tiempo.
type DenialMetrics struct {
	Total    int64
	ByModule []domain.DenialSummaryRow
	ByUser   []domain.DenialSummaryRow
	Recent   []*domain.AccessDenial
}

func (uc *RBACUseCase) DenialMetrics(ctx context.Context, tenantID uuid.UUID, since time.Time, recentLimit int) (*DenialMetrics, error) {
	total, err := uc.denials.Total(ctx, tenantID, since)
	if err != nil {
		return nil, err
	}
	byModule, err := uc.denials.SummaryByModule(ctx, tenantID, since)
	if err != nil {
		return nil, err
	}
	byUser, err := uc.denials.SummaryByUser(ctx, tenantID, since)
	if err != nil {
		return nil, err
	}
	recent, err := uc.denials.List(ctx, tenantID, recentLimit)
	if err != nil {
		return nil, err
	}
	return &DenialMetrics{Total: total, ByModule: byModule, ByUser: byUser, Recent: recent}, nil
}

func (uc *RBACUseCase) CreateRole(ctx context.Context, tenantID uuid.UUID, name, description string) (*domain.Role, error) {
	existing, _ := uc.roles.GetByName(ctx, tenantID, name)
	if existing != nil {
		return nil, domain.ErrRoleAlreadyExists
	}

	role := &domain.Role{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Name:        name,
		Description: description,
		Status:      "active",
	}

	if err := uc.roles.Create(ctx, role); err != nil {
		return nil, fmt.Errorf("create role: %w", err)
	}
	return role, nil
}

// tenantRole carga un rol y comprueba que pertenece al tenant: un rol ajeno se reporta
// como inexistente para no revelar que existe.
func (uc *RBACUseCase) tenantRole(ctx context.Context, tenantID, id uuid.UUID) (*domain.Role, error) {
	role, err := uc.roles.GetByID(ctx, id)
	if err != nil || role.TenantID != tenantID {
		return nil, domain.ErrRoleNotFound
	}
	return role, nil
}

func (uc *RBACUseCase) GetRole(ctx context.Context, tenantID, id uuid.UUID) (*domain.Role, error) {
	return uc.tenantRole(ctx, tenantID, id)
}

func (uc *RBACUseCase) ListRoles(ctx context.Context, tenantID uuid.UUID) ([]*domain.Role, error) {
	return uc.roles.List(ctx, tenantID)
}

func (uc *RBACUseCase) UpdateRole(ctx context.Context, tenantID, id uuid.UUID, name, description string) (*domain.Role, error) {
	role, err := uc.tenantRole(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if role.IsSystem {
		return nil, domain.ErrSystemRole
	}

	role.Name = name
	role.Description = description

	if err := uc.roles.Update(ctx, role); err != nil {
		return nil, fmt.Errorf("update role: %w", err)
	}
	return role, nil
}

func (uc *RBACUseCase) DeleteRole(ctx context.Context, tenantID, id uuid.UUID) error {
	role, err := uc.tenantRole(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if role.IsSystem {
		return domain.ErrSystemRole
	}
	return uc.roles.Delete(ctx, id)
}

func (uc *RBACUseCase) SetRolePermissions(ctx context.Context, actor Actor, tenantID, roleID uuid.UUID, permissionIDs []uuid.UUID) error {
	role, err := uc.tenantRole(ctx, tenantID, roleID)
	if err != nil {
		return err
	}
	if role.IsSystem {
		return domain.ErrSystemRole
	}
	unique := make([]uuid.UUID, 0, len(permissionIDs))
	seen := make(map[uuid.UUID]struct{}, len(permissionIDs))
	for _, id := range permissionIDs {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	if len(unique) > 0 {
		found, err := uc.perms.GetByIDs(ctx, unique)
		if err != nil {
			return fmt.Errorf("leer permisos: %w", err)
		}
		if len(found) != len(unique) {
			return domain.ErrPermissionNotFound
		}
		for _, p := range found {
			if !p.AssignableToTenantRole() {
				return domain.ErrPlatformPermission
			}
		}
		if err := uc.requireHeld(ctx, actor, tenantID, found); err != nil {
			return err
		}
	}
	return uc.rolePerms.ReplaceAll(ctx, roleID, unique)
}

// requireHeld exige que un actor no privilegiado tenga cada uno de los permisos que
// concede. Se evalua con la misma regla de comodines que la politica efectiva.
func (uc *RBACUseCase) requireHeld(ctx context.Context, actor Actor, tenantID uuid.UUID, perms []*domain.Permission) error {
	if actor.Privileged || len(perms) == 0 {
		return nil
	}
	policy, err := uc.userRoles.GetAccessPolicy(ctx, actor.UserID, tenantID)
	if err != nil {
		return fmt.Errorf("leer la politica de quien concede: %w", err)
	}
	for _, p := range perms {
		if !policy.HasPermission(p.Module, p.Resource, p.Action) {
			return domain.ErrPermissionNotHeld
		}
	}
	return nil
}

// checkRoleGrant aplica a asignar y retirar un rol las mismas reglas que a editarlo: un
// rol del sistema solo lo mueve un rol del sistema, y un rol propio solo quien tiene todo
// lo que el rol concede.
func (uc *RBACUseCase) checkRoleGrant(ctx context.Context, actor Actor, tenantID uuid.UUID, role *domain.Role) error {
	if actor.Privileged {
		return nil
	}
	if role.IsSystem {
		return domain.ErrSystemRoleAssignment
	}
	perms, err := uc.rolePerms.ListPermissions(ctx, role.ID)
	if err != nil {
		return fmt.Errorf("leer permisos del rol: %w", err)
	}
	return uc.requireHeld(ctx, actor, tenantID, perms)
}

func (uc *RBACUseCase) GetRolePermissions(ctx context.Context, tenantID, roleID uuid.UUID) ([]*domain.Permission, error) {
	if _, err := uc.tenantRole(ctx, tenantID, roleID); err != nil {
		return nil, err
	}
	return uc.rolePerms.ListPermissions(ctx, roleID)
}

func (uc *RBACUseCase) AssignRoleToUser(ctx context.Context, actor Actor, tenantID, userID, roleID uuid.UUID) error {
	role, err := uc.tenantRole(ctx, tenantID, roleID)
	if err != nil {
		return err
	}
	if err := uc.checkRoleGrant(ctx, actor, tenantID, role); err != nil {
		return err
	}
	return uc.userRoles.Assign(ctx, userID, roleID, actor.UserID)
}

func (uc *RBACUseCase) RevokeRoleFromUser(ctx context.Context, actor Actor, tenantID, userID, roleID uuid.UUID) error {
	role, err := uc.tenantRole(ctx, tenantID, roleID)
	if err != nil {
		return err
	}
	if err := uc.checkRoleGrant(ctx, actor, tenantID, role); err != nil {
		return err
	}
	return uc.userRoles.Revoke(ctx, userID, roleID)
}

func (uc *RBACUseCase) GetUserRoles(ctx context.Context, userID, tenantID uuid.UUID) ([]*domain.Role, error) {
	return uc.userRoles.ListRoles(ctx, userID, tenantID)
}

// UserAccess resume el acceso operativo de un usuario para filtrar el menu y la API.
// Modules: modulos visibles (cualquier permiso). WriteModules: modulos donde puede
// modificar. La distincion evita que un rol de solo lectura pueda escribir aunque vea
// el modulo.
type UserAccess struct {
	IsAdmin      bool
	Roles        []string
	Modules      []string
	WriteModules []string
	// WriteActions: modulo -> acciones de escritura que el usuario tiene (create,
	// update, delete, ...). Permite gateo por accion en el gateway.
	WriteActions map[string][]string
	// DisabledModules: modulos que la empresa no tiene contratados. El gateway los usa
	// para bloquear escrituras incluso del administrador de la empresa (que si no
	// tendria acceso total). Vacio si el tenant no tiene estado explicito o el usuario
	// es superadmin.
	DisabledModules []string
	// TokensValidFrom: instante de revocacion del usuario. El gateway rechaza los access
	// token emitidos antes.
	TokensValidFrom time.Time
}

// GetUserAccess devuelve domain.ErrUserNotFound o domain.ErrUserNotActive cuando la cuenta
// no existe en la empresa o no esta activa: el gateway los toma como definitivos y rechaza
// el token. Un fallo leyendo la cuenta no lo es: se sigue sin instante de revocacion, porque
// un fallo transitorio de la base no debe expulsar a toda la empresa, y la sesion queda
// acotada por la vida corta del access token.
func (uc *RBACUseCase) GetUserAccess(ctx context.Context, userID, tenantID uuid.UUID) (*UserAccess, error) {
	account, err := uc.userRoles.UserAccount(ctx, userID, tenantID)
	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		return nil, domain.ErrUserNotFound
	case err != nil:
		uc.logger.Warn("no se pudo leer el estado de la cuenta; sin instante de revocacion",
			zap.String("user_id", userID.String()), zap.Error(err))
	case !account.Active():
		return nil, domain.ErrUserNotActive
	}

	roles, err := uc.userRoles.ListRoles(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}
	access := &UserAccess{
		Roles:           make([]string, 0, len(roles)),
		DisabledModules: []string{},
		TokensValidFrom: account.TokensValidFrom,
	}
	isSuperadmin := false
	for _, r := range roles {
		access.Roles = append(access.Roles, r.Name)
		switch r.Name {
		case uc.systemRoles.Superadmin:
			isSuperadmin = true
			access.IsAdmin = true
		case uc.systemRoles.TenantAdmin:
			access.IsAdmin = true
		}
	}

	modules, err := uc.userRoles.ListAccessibleModules(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}
	access.Modules = modules

	writeActions, err := uc.userRoles.ListWriteActionsByModule(ctx, userID, tenantID)
	if err != nil {
		return nil, err
	}
	access.WriteActions = writeActions

	// Acotar por contratacion: interseccion de los modulos del rol con los que la
	// empresa tiene habilitados. El superadmin transciende el catalogo. Fail-open: el
	// catalogo es comercial, no una frontera de seguridad, y si su lectura falla es
	// preferible no filtrar a dejar a la empresa sin pantallas.
	if uc.moduleGate != nil && tenantID != uuid.Nil && !isSuperadmin {
		availability, gateErr := uc.moduleGate.EffectiveModules(ctx, tenantID)
		if gateErr != nil {
			uc.logger.Warn("no se pudo leer los modulos contratados del tenant; sin filtro",
				zap.String("tenant_id", tenantID.String()), zap.Error(gateErr))
		} else if availability.Restricted {
			access.DisabledModules = availability.Disabled()
			access.Modules = filterAllowed(access.Modules, availability)
			filtered := make(map[string][]string, len(access.WriteActions))
			for m, acts := range access.WriteActions {
				if availability.Allows(m) {
					filtered[m] = acts
				}
			}
			access.WriteActions = filtered
		}
	}

	access.WriteModules = make([]string, 0, len(access.WriteActions))
	for module := range access.WriteActions {
		access.WriteModules = append(access.WriteModules, module)
	}
	sort.Strings(access.WriteModules)

	return access, nil
}

// UsersWithPermission resuelve a quien hay que avisar de algo. Es preferible a preguntar
// por un rol: los nombres de rol los puede cambiar cada empresa, el permiso no.
func (uc *RBACUseCase) UsersWithPermission(ctx context.Context, tenantID uuid.UUID, module, action string) ([]uuid.UUID, error) {
	return uc.userRoles.ListUsersWithPermission(ctx, tenantID, module, action)
}

func filterAllowed(modules []string, availability domain.ModuleAvailability) []string {
	out := make([]string, 0, len(modules))
	for _, m := range modules {
		if availability.Allows(m) {
			out = append(out, m)
		}
	}
	return out
}

func (uc *RBACUseCase) CheckAccess(ctx context.Context, userID, tenantID uuid.UUID, module, resource, action string) (bool, error) {
	policy, err := uc.userRoles.GetAccessPolicy(ctx, userID, tenantID)
	if err != nil {
		return false, err
	}
	return policy.HasPermission(module, resource, action), nil
}

func (uc *RBACUseCase) GetAccessPolicy(ctx context.Context, userID, tenantID uuid.UUID) (*domain.AccessPolicy, error) {
	return uc.userRoles.GetAccessPolicy(ctx, userID, tenantID)
}

// ListPermissions devuelve el catalogo con el que se editan roles. Quien no opera la
// plataforma solo ve los permisos que puede asignar a un rol de su empresa.
func (uc *RBACUseCase) ListPermissions(ctx context.Context, includePlatform bool) ([]*domain.Permission, error) {
	perms, err := uc.perms.List(ctx)
	if err != nil {
		return nil, err
	}
	return scopeFilter(perms, includePlatform), nil
}

func (uc *RBACUseCase) ListPermissionsByModule(ctx context.Context, module string, includePlatform bool) ([]*domain.Permission, error) {
	perms, err := uc.perms.ListByModule(ctx, module)
	if err != nil {
		return nil, err
	}
	return scopeFilter(perms, includePlatform), nil
}

func scopeFilter(perms []*domain.Permission, includePlatform bool) []*domain.Permission {
	if includePlatform {
		return perms
	}
	out := make([]*domain.Permission, 0, len(perms))
	for _, p := range perms {
		if p.AssignableToTenantRole() {
			out = append(out, p)
		}
	}
	return out
}
