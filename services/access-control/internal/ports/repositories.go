package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/access-control/internal/domain"
	"github.com/google/uuid"
)

type RoleRepository interface {
	Create(ctx context.Context, role *domain.Role) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Role, error)
	GetByName(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Role, error)
	List(ctx context.Context, tenantID uuid.UUID) ([]*domain.Role, error)
	Update(ctx context.Context, role *domain.Role) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type PermissionRepository interface {
	List(ctx context.Context) ([]*domain.Permission, error)
	ListByModule(ctx context.Context, module string) ([]*domain.Permission, error)
	// GetByIDs devuelve los permisos existentes entre los pedidos; los que no existen no
	// aparecen.
	GetByIDs(ctx context.Context, ids []uuid.UUID) ([]*domain.Permission, error)
}

type RolePermissionRepository interface {
	ListPermissions(ctx context.Context, roleID uuid.UUID) ([]*domain.Permission, error)
	ReplaceAll(ctx context.Context, roleID uuid.UUID, permissionIDs []uuid.UUID) error
}

// UserRoleRepository resuelve el acceso de un usuario a partir de sus roles. Todas las
// consultas reciben el tenant ademas del usuario: un rol siempre pertenece al tenant del
// usuario, pero exigirlo en la consulta impide sondear a usuarios de otra empresa desde
// una sesion cualquiera.
type UserRoleRepository interface {
	Assign(ctx context.Context, userID, roleID, assignedBy uuid.UUID) error
	Revoke(ctx context.Context, userID, roleID uuid.UUID) error
	ListRoles(ctx context.Context, userID, tenantID uuid.UUID) ([]*domain.Role, error)
	ListPermissions(ctx context.Context, userID, tenantID uuid.UUID) ([]*domain.Permission, error)
	GetAccessPolicy(ctx context.Context, userID, tenantID uuid.UUID) (*domain.AccessPolicy, error)
	// ListAccessibleModules devuelve los modulos en los que el usuario tiene algun
	// permiso: es la base para decidir que ve en el menu.
	ListAccessibleModules(ctx context.Context, userID, tenantID uuid.UUID) ([]string, error)
	// ListWriteActionsByModule devuelve, por modulo, las acciones de ESCRITURA (distintas
	// de read/export) que tiene el usuario. Permite al gateway gatear por accion (DELETE
	// exige delete), no solo por modulo, y que un rol de solo lectura no pueda modificar
	// aunque vea el modulo.
	ListWriteActionsByModule(ctx context.Context, userID, tenantID uuid.UUID) (map[string][]string, error)
	// TokensValidFrom devuelve el instante desde el cual son validos los tokens del
	// usuario: el gateway rechaza cualquier access token emitido antes (revocacion
	// instantanea de sesiones).
	TokensValidFrom(ctx context.Context, userID uuid.UUID) (time.Time, error)
	// ListUsersWithPermission devuelve los usuarios activos de la empresa que tienen un
	// permiso concreto. Permite avisar a quien corresponde sin que el servicio que avisa
	// tenga que conocer los nombres de los roles (que cada empresa puede renombrar).
	ListUsersWithPermission(ctx context.Context, tenantID uuid.UUID, module, action string) ([]uuid.UUID, error)
}

// DenialRepository persiste y consulta los accesos denegados por RBAC (metricas).
type DenialRepository interface {
	Record(ctx context.Context, d *domain.AccessDenial) error
	List(ctx context.Context, tenantID uuid.UUID, limit int) ([]*domain.AccessDenial, error)
	SummaryByModule(ctx context.Context, tenantID uuid.UUID, since time.Time) ([]domain.DenialSummaryRow, error)
	SummaryByUser(ctx context.Context, tenantID uuid.UUID, since time.Time) ([]domain.DenialSummaryRow, error)
	Total(ctx context.Context, tenantID uuid.UUID, since time.Time) (int64, error)
}
