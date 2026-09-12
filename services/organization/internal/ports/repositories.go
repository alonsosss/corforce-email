package ports

import (
	"context"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

type TenantRepository interface {
	Create(ctx context.Context, tenant *domain.Tenant) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)
	GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
	List(ctx context.Context, offset, limit int) ([]*domain.Tenant, int64, error)
	Update(ctx context.Context, tenant *domain.Tenant) error
	// Delete retira el tenant del registro junto con sus usuarios, roles y modulos.
	// No toca la base fisica: esa decision se toma aparte y con respaldo.
	Delete(ctx context.Context, id uuid.UUID) error
}

// CellRepository es el directorio de celdas: donde esta cada Postgres regional y si
// admite tenants nuevos.
type CellRepository interface {
	Create(ctx context.Context, cell *domain.Cell) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Cell, error)
	GetByCode(ctx context.Context, code string) (*domain.Cell, error)
	List(ctx context.Context) ([]*domain.Cell, error)
	Update(ctx context.Context, cell *domain.Cell) error
}

type TenantDBProvisioner interface {
	CreateDatabase(ctx context.Context, target domain.DBTarget) error
	RunMigrations(ctx context.Context, target domain.DBTarget) error
	DropDatabase(ctx context.Context, target domain.DBTarget) error
	// MigrationStatus reporta migraciones aplicadas y pendientes sin aplicar nada.
	MigrationStatus(ctx context.Context, target domain.DBTarget) (domain.TenantMigrationStatus, error)
}

// RoleSeeder siembra los roles de sistema de un tenant. Es idempotente: se aplica en
// el alta y se reaplica en cada arranque para que un permiso nuevo del catalogo llegue
// a los tenants que ya existian.
type RoleSeeder interface {
	SeedDefaultRoles(ctx context.Context, tenantID uuid.UUID) error
}

// AdminUserSeeder crea el primer usuario administrador del tenant con la contrasena
// recibida en el alta. La contrasena nunca se registra ni se devuelve.
type AdminUserSeeder interface {
	CreateAdminUser(ctx context.Context, tenantID uuid.UUID, email, password, firstName, lastName string) error
}

// ModulesRepository accede al catalogo de modulos y al estado de habilitacion por
// tenant. La ausencia de filas para un tenant significa "todo habilitado".
type ModulesRepository interface {
	ListCatalog(ctx context.Context) ([]domain.ModuleCatalogEntry, error)
	HasAny(ctx context.Context, tenantID uuid.UUID) (bool, error)
	ListForTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.TenantModuleState, error)
	Upsert(ctx context.Context, tenantID uuid.UUID, module string, enabled bool) error
	ReplaceForTenant(ctx context.Context, tenantID uuid.UUID, states map[string]bool) error
	DeleteForTenant(ctx context.Context, tenantID uuid.UUID) error
}

// TenantEventPublisher anuncia los hechos del plano de control que interesan al resto
// de servicios. Los eventos son persistentes: un consumidor caido los recibe al volver.
type TenantEventPublisher interface {
	TenantCreated(ctx context.Context, tenant *domain.Tenant) error
	TenantStatusChanged(ctx context.Context, tenant *domain.Tenant, previous string) error
	// TenantModulesChanged lleva el estado efectivo completo tras el cambio, para que
	// el consumidor no tenga que reconstruirlo a partir de una secuencia de toggles.
	TenantModulesChanged(ctx context.Context, tenantID uuid.UUID, enabled, disabled []string) error
}
