package ports

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

// TenantRepository lee y edita el registro de empresas. El alta y la baja de una empresa
// son de la saga (TenantSagaRepository): la empresa nace y desaparece junto a su saga.
type TenantRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)
	GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
	List(ctx context.Context, offset, limit int) ([]*domain.Tenant, int64, error)
	Update(ctx context.Context, tenant *domain.Tenant) error
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

// MailDomainIndex es el indice global de los dominios de correo activos: dominio -> empresa. La
// celda del dominio es la de su empresa. Lo escribe domain-service por la API interna al activar
// y retirar dominios; lo lee el gateway para llevar el webmail de cada buzon a su celda.
type MailDomainIndex interface {
	// Claim registra el dominio para la empresa; si ya es suyo solo lo confirma.
	// ErrMailDomainClaimed si es de otra empresa; ErrTenantBeingRemoved si la empresa tiene la
	// baja en curso (tampoco confirma uno suyo); ErrTenantNotFound si la empresa no existe.
	Claim(ctx context.Context, name string, tenantID uuid.UUID) error
	// Release retira el dominio si es de la empresa y dice si lo retiro. Si no esta, o es de
	// otra empresa, no hace nada.
	Release(ctx context.Context, name string, tenantID uuid.UUID) (bool, error)
	// ReleaseTenant retira todos los dominios de la empresa y dice cuantos retiro.
	ReleaseTenant(ctx context.Context, tenantID uuid.UUID) (int64, error)
	// TenantOf devuelve la empresa del dominio o ErrMailDomainNotFound.
	TenantOf(ctx context.Context, name string) (uuid.UUID, error)
}

type TenantDBProvisioner interface {
	// CreateDatabase crea la base de la empresa, la marca como suya y la cierra a PUBLIC. Si
	// ya existe con la marca de esa empresa (un intento anterior del alta) la adopta; si
	// existe sin ella responde ErrDatabaseOccupied y no la toca.
	CreateDatabase(ctx context.Context, target domain.DBTarget, tenantID uuid.UUID) error
	RunMigrations(ctx context.Context, target domain.DBTarget) error
	// DropOwnedDatabase borra la base solo si lleva la marca de la empresa. Sin base, o con
	// una ajena, no hace nada.
	DropOwnedDatabase(ctx context.Context, target domain.DBTarget, tenantID uuid.UUID) error
	// MigrationStatus reporta migraciones aplicadas y pendientes sin aplicar nada.
	MigrationStatus(ctx context.Context, target domain.DBTarget) (domain.TenantMigrationStatus, error)
}

// TenantSagaRepository persiste la saga de alta y baja de cada empresa. Cada escritura de
// una saga en curso lleva el token de su arriendo: si otra instancia la tomo, ErrLeaseLost.
// Un lease mayor que cero alarga el arriendo hasta ahora + lease; cero lo suelta.
type TenantSagaRepository interface {
	// BeginCreate registra la empresa y su saga de alta, con el arriendo tomado, en una
	// transaccion. ErrTenantAlreadyExists si el slug o el nombre de base estan ocupados.
	BeginCreate(ctx context.Context, tenant *domain.Tenant, saga *domain.TenantSaga, lease time.Duration) error
	// Insert crea, con el arriendo tomado, la saga de una empresa que no la tenia (dada de
	// alta antes de que existieran). ErrTenantBusy si ya existe.
	Insert(ctx context.Context, saga *domain.TenantSaga, lease time.Duration) error
	// Get devuelve la saga de la empresa o ErrSagaNotFound.
	Get(ctx context.Context, tenantID uuid.UUID) (*domain.TenantSaga, error)
	// Claim toma el arriendo si nadie lo tiene vigente. ErrSagaNotFound o ErrTenantBusy.
	Claim(ctx context.Context, tenantID uuid.UUID, lease time.Duration) (*domain.TenantSaga, error)
	// Save guarda operacion, estado, paso, rol, borrado de base y error de la saga.
	Save(ctx context.Context, saga *domain.TenantSaga, lease time.Duration) error
	// CompleteCreate activa la empresa y cierra su saga de alta en una transaccion.
	CompleteCreate(ctx context.Context, saga *domain.TenantSaga) error
	// DeleteTenant retira del registro la empresa, sus modulos y su saga en una transaccion.
	DeleteTenant(ctx context.Context, saga *domain.TenantSaga) error
	// ListStale devuelve las sagas en curso o compensando sin arriendo vigente.
	ListStale(ctx context.Context, limit int) ([]*domain.TenantSaga, error)
}

// AccessControl son las operaciones de roles de una empresa entera que access-control sirve
// por su API interna. Todas son idempotentes.
type AccessControl interface {
	// SeedTenantAdminRole siembra el rol del sistema con los permisos de alcance tenant del
	// catalogo y devuelve su id.
	SeedTenantAdminRole(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, error)
	// ReseedSystemRoles concede a los roles del sistema de todas las empresas los permisos
	// que les falten y devuelve cuantos roles recorrio.
	ReseedSystemRoles(ctx context.Context) (int, error)
	AssignRole(ctx context.Context, tenantID, userID, roleID uuid.UUID) error
	RevokeRole(ctx context.Context, tenantID, userID, roleID uuid.UUID) error
	// RemoveTenantRoles borra los roles de una empresa que no esta activa, con sus
	// asignaciones.
	RemoveTenantRoles(ctx context.Context, tenantID uuid.UUID) error
}

// MailDirectory es lo que la baja de una empresa pide al directorio de correo de su celda
// (mail-directory de esa celda) por su API interna.
type MailDirectory interface {
	// RetireTenant da de baja a la empresa en el directorio de la celda cellCode: nada suyo vuelve
	// a recibir, reenviar ni autenticar ahi, y su directorio deja de admitir cambios. Solo vuelve
	// sin error cuando la instancia de esa celda lo confirma. Idempotente.
	RetireTenant(ctx context.Context, cellCode string, tenantID uuid.UUID) error
}

// FirstAdmin es el primer administrador de una empresa. La contrasena solo viaja a
// identity: organization no la guarda ni la registra nunca, y por eso no se serializa ni se
// imprime.
type FirstAdmin struct {
	UserID    uuid.UUID
	Email     string
	Password  string `json:"-"`
	FirstName string
	LastName  string
}

func (a FirstAdmin) String() string {
	return "FirstAdmin{" + a.UserID.String() + " " + a.Email + "}"
}

func (a FirstAdmin) GoString() string { return a.String() }

// Identity son las operaciones sobre las cuentas de una empresa entera que identity sirve
// por su API interna. Las dos son idempotentes.
type Identity interface {
	// CreateFirstUser da de alta la primera cuenta con la politica de contrasenas de
	// identity. AdminRejectedError si identity rechaza la contrasena; ErrAdminUserConflict
	// si la empresa ya tiene otra cuenta.
	CreateFirstUser(ctx context.Context, tenantID uuid.UUID, admin FirstAdmin) error
	// RemoveTenantUsers borra las cuentas de una empresa que no esta activa, con sus sesiones.
	RemoveTenantUsers(ctx context.Context, tenantID uuid.UUID) error
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
