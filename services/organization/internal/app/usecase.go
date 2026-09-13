package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/alonsosss/corforce-email/services/organization/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var slugRegex = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// cellCodeRegex acepta codigos cortos en minusculas, como "pe-01" o "eu-west-1".
var cellCodeRegex = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// minAdminPasswordLength es el minimo que se exige a la contrasena del primer
// administrador antes de empezar el alta. La politica de contrasenas la aplica identity al
// crear la cuenta; este suelo evita crear y deshacer una base por una contrasena trivial.
const minAdminPasswordLength = 12

// defaultSagaLease es el arriendo de una ejecucion de la saga cuando el despliegue no fija
// otro: holgado para crear y migrar una base nueva.
const defaultSagaLease = 5 * time.Minute

// Dependencies agrupa los puertos del caso de uso. Un constructor con nombres evita
// el error clasico de cruzar dos argumentos posicionales del mismo tipo.
type Dependencies struct {
	Tenants     ports.TenantRepository
	Cells       ports.CellRepository
	Sagas       ports.TenantSagaRepository
	Provisioner ports.TenantDBProvisioner
	// Access e Identity son los duenos del rol del sistema, las asignaciones y las cuentas
	// de cada empresa: la saga se los pide por su API interna.
	Access    ports.AccessControl
	Identity  ports.Identity
	Modules   ports.ModulesRepository
	Publisher ports.TenantEventPublisher
	// DefaultCellCode es la celda donde nacen los tenants cuya alta no indica una.
	// Vacio significa que no hay celda por defecto y toda alta debe indicarla.
	DefaultCellCode string
	// SagaLease es cuanto puede tardar una ejecucion de la saga antes de que otra instancia
	// la de por abandonada. Cero toma defaultSagaLease.
	SagaLease time.Duration
	Logger    *zap.Logger
}

type OrganizationUseCase struct {
	tenants         ports.TenantRepository
	cells           ports.CellRepository
	sagas           ports.TenantSagaRepository
	provisioner     ports.TenantDBProvisioner
	access          ports.AccessControl
	identity        ports.Identity
	modules         ports.ModulesRepository
	publisher       ports.TenantEventPublisher
	defaultCellCode string
	sagaLease       time.Duration
	logger          *zap.Logger
}

func NewOrganizationUseCase(deps Dependencies) *OrganizationUseCase {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	lease := deps.SagaLease
	if lease <= 0 {
		lease = defaultSagaLease
	}
	return &OrganizationUseCase{
		tenants:         deps.Tenants,
		cells:           deps.Cells,
		sagas:           deps.Sagas,
		provisioner:     deps.Provisioner,
		access:          deps.Access,
		identity:        deps.Identity,
		modules:         deps.Modules,
		publisher:       deps.Publisher,
		defaultCellCode: strings.TrimSpace(deps.DefaultCellCode),
		sagaLease:       lease,
		logger:          logger,
	}
}

// tenantSettingKeys son los ajustes descriptivos que viven en tenants.settings. Se
// enumeran para que el alta, la edicion y la respuesta HTTP expongan el mismo conjunto.
// "tz" es el identificador IANA de la franja horaria del tenant (America/Lima).
var tenantSettingKeys = []string{"legal_name", "tz", "phone", "address", "website", "logo_url"}

// TenantSettingKeys devuelve los ajustes descriptivos admitidos, en orden estable.
func TenantSettingKeys() []string { return append([]string(nil), tenantSettingKeys...) }

type CreateTenantRequest struct {
	Slug string
	Name string
	// CellCode es la celda donde crear la base. Vacio = celda por defecto.
	CellCode       string
	Settings       map[string]string
	AdminEmail     string
	AdminPassword  string
	AdminFirstName string
	AdminLastName  string
}

// CreateTenant da de alta una empresa como saga (tenant_saga.go): la empresa se registra
// inactiva, se crea y migra su base, access-control siembra su rol del sistema, identity
// crea su primer usuario, access-control se lo asigna y solo entonces la empresa pasa a
// activa. Pedir de nuevo el alta de un slug con un alta pendiente la retoma.
func (uc *OrganizationUseCase) CreateTenant(ctx context.Context, req CreateTenantRequest) (*domain.Tenant, error) {
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	if !slugRegex.MatchString(slug) {
		return nil, fmt.Errorf("formato de slug no valido")
	}
	if req.AdminEmail == "" || req.AdminPassword == "" {
		return nil, domain.ErrAdminUserRequired
	}
	if len(req.AdminPassword) < minAdminPasswordLength {
		return nil, domain.ErrAdminPasswordShort
	}

	existing, err := uc.tenants.GetBySlug(ctx, slug)
	switch {
	case err == nil:
		return uc.retryCreate(ctx, existing, req)
	case !errors.Is(err, domain.ErrTenantNotFound):
		return nil, fmt.Errorf("buscar la empresa: %w", err)
	}

	// La celda se resuelve ANTES de registrar nada: sin celda no hay donde aprovisionar.
	cell, err := uc.resolveCell(ctx, req.CellCode)
	if err != nil {
		return nil, err
	}

	settings := make(map[string]interface{}, len(tenantSettingKeys))
	for _, key := range tenantSettingKeys {
		if v, ok := req.Settings[key]; ok && v != "" {
			settings[key] = v
		}
	}

	tenant := &domain.Tenant{
		ID:     uuid.New(),
		Slug:   slug,
		Name:   req.Name,
		DBName: domain.TenantDBName(slug),
		// Inactiva hasta que la saga termine: sin login, sin enrutado y fuera de los
		// barridos de migracion.
		Status:   domain.TenantStatusInactive,
		CellID:   cell.ID,
		Settings: settings,
	}
	saga := &domain.TenantSaga{
		TenantID:    tenant.ID,
		Operation:   domain.SagaCreate,
		State:       domain.SagaRunning,
		Step:        domain.StepRegistered,
		AdminUserID: uuid.New(),
	}
	if err := uc.sagas.BeginCreate(ctx, tenant, saga, uc.sagaLease); err != nil {
		return nil, err
	}
	return uc.runCreate(ctx, &createRun{
		tenant: tenant, target: domain.DBTargetFor(tenant, cell), saga: saga, admin: firstAdmin(req, tenant, saga),
	})
}

// targetFor localiza la base de un tenant existente en su celda.
func (uc *OrganizationUseCase) targetFor(ctx context.Context, t *domain.Tenant) (domain.DBTarget, error) {
	cell, err := uc.cells.GetByID(ctx, t.CellID)
	if err != nil {
		return domain.DBTarget{}, fmt.Errorf("celda del tenant %s: %w", t.Slug, err)
	}
	return domain.DBTargetFor(t, cell), nil
}

// resolveCell devuelve la celda de la peticion o, si no trae, la celda por defecto
// de la plataforma. Solo una celda activa recibe tenants nuevos.
func (uc *OrganizationUseCase) resolveCell(ctx context.Context, code string) (*domain.Cell, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		code = uc.defaultCellCode
	}
	if code == "" {
		return nil, domain.ErrCellRequired
	}
	cell, err := uc.cells.GetByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if !cell.AcceptsTenants() {
		return nil, domain.ErrCellNotAssignable
	}
	return cell, nil
}

// publish envia un evento de plano de control en el hilo de la peticion, sin que un
// fallo de publicacion deshaga lo que ya quedo escrito: el hecho existe aunque el aviso
// no salga, y se deja constancia para reintentarlo.
func (uc *OrganizationUseCase) publish(event string, tenantID uuid.UUID, fn func() error) {
	if uc.publisher == nil {
		return
	}
	if err := fn(); err != nil {
		uc.logger.Warn("no se pudo publicar el evento del tenant",
			zap.String("event", event), zap.String("tenant_id", tenantID.String()), zap.Error(err))
	}
}

func (uc *OrganizationUseCase) GetTenant(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	return uc.tenants.GetByID(ctx, id)
}

// ReseedAllRoles pide a access-control que conceda a los roles del sistema de todas las
// empresas los permisos del catalogo que les falten. Corre al arrancar, despues de migrar el
// registro: es lo que hace que un permiso nuevo llegue a las empresas que ya existian sin
// que nadie tenga que pulsar nada. Devuelve cuantos roles recorrio.
func (uc *OrganizationUseCase) ReseedAllRoles(ctx context.Context) (int, error) {
	return uc.access.ReseedSystemRoles(ctx)
}

// ReseedRoles repara el rol del sistema de un tenant: access-control lo vuelve a sembrar,
// de forma idempotente, con los permisos que le falten. Sustituye a cualquier arreglo SQL
// a mano: el mapeo rol-permiso vive una sola vez, en el catalogo de access-control.
func (uc *OrganizationUseCase) ReseedRoles(ctx context.Context, tenantID uuid.UUID) error {
	if _, err := uc.tenants.GetByID(ctx, tenantID); err != nil {
		return domain.ErrTenantNotFound
	}
	_, err := uc.access.SeedTenantAdminRole(ctx, tenantID)
	return err
}

type UpdateTenantRequest struct {
	Name     *string
	Settings map[string]*string
}

// UpdateTenant cambia el nombre y los ajustes descriptivos. El estado tiene su propia
// operacion porque cambia el enrutado y publica un evento.
func (uc *OrganizationUseCase) UpdateTenant(ctx context.Context, id uuid.UUID, req UpdateTenantRequest) (*domain.Tenant, error) {
	tenant, err := uc.tenants.GetByID(ctx, id)
	if err != nil {
		return nil, domain.ErrTenantNotFound
	}
	if req.Name != nil {
		tenant.Name = strings.TrimSpace(*req.Name)
	}
	if tenant.Settings == nil {
		tenant.Settings = make(map[string]interface{})
	}
	for _, key := range tenantSettingKeys {
		if val, ok := req.Settings[key]; ok && val != nil {
			tenant.Settings[key] = *val
		}
	}
	if err := uc.tenants.Update(ctx, tenant); err != nil {
		return nil, fmt.Errorf("actualizar tenant: %w", err)
	}
	return tenant, nil
}

// SetTenantStatus cambia el estado del tenant y lo anuncia. Un estado repetido no
// escribe ni publica nada: el evento significa "cambio", no "confirmacion".
func (uc *OrganizationUseCase) SetTenantStatus(ctx context.Context, id uuid.UUID, status string) (*domain.Tenant, error) {
	if !contains(domain.TenantStatuses(), status) {
		return nil, domain.ErrInvalidTenantStatus
	}
	tenant, err := uc.tenants.GetByID(ctx, id)
	if err != nil {
		return nil, domain.ErrTenantNotFound
	}
	if tenant.Status == status {
		return tenant, nil
	}
	if err := uc.requireNoPendingSaga(ctx, id); err != nil {
		return nil, err
	}
	previous := tenant.Status
	tenant.Status = status
	if err := uc.tenants.Update(ctx, tenant); err != nil {
		return nil, fmt.Errorf("cambiar estado del tenant: %w", err)
	}
	uc.publish("tenant.status_changed", tenant.ID, func() error {
		return uc.publisher.TenantStatusChanged(ctx, tenant, previous)
	})
	return tenant, nil
}

// requireNoPendingSaga rechaza cambiar a mano el estado de una empresa con un alta sin
// completar o una baja en curso: solo la saga la activa, y activarla a medias la pondria en
// servicio sin administrador.
func (uc *OrganizationUseCase) requireNoPendingSaga(ctx context.Context, id uuid.UUID) error {
	saga, err := uc.sagas.Get(ctx, id)
	switch {
	case errors.Is(err, domain.ErrSagaNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("saga de la empresa: %w", err)
	case saga.Pending():
		return domain.ErrTenantBusy
	}
	return nil
}

func (uc *OrganizationUseCase) ListTenants(ctx context.Context, page, pageSize int) ([]*domain.Tenant, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	return uc.tenants.List(ctx, (page-1)*pageSize, pageSize)
}

// DeleteTenant retira del registro un tenant que ya no esta activa, como saga que se retoma
// hasta terminar: access-control retira sus roles, identity sus cuentas y organization su
// registro. La base fisica se conserva (borrarla es una decision operativa aparte, con
// respaldo previo), salvo la de un alta que nunca llego a completarse.
func (uc *OrganizationUseCase) DeleteTenant(ctx context.Context, id uuid.UUID) error {
	tenant, err := uc.tenants.GetByID(ctx, id)
	if err != nil {
		return domain.ErrTenantNotFound
	}
	if tenant.IsActive() {
		return domain.ErrTenantStillActive
	}
	saga, err := uc.claimDeletion(ctx, id)
	if err != nil {
		return err
	}
	runCtx, cancel := uc.sagaContext(ctx)
	defer cancel()
	return uc.runDelete(runCtx, tenant, saga)
}

// eachTenant recorre todos los tenants pagina a pagina, esten en el estado que esten.
func (uc *OrganizationUseCase) eachTenant(ctx context.Context, fn func(*domain.Tenant)) error {
	const batch = 100
	offset := 0
	for {
		tenants, _, err := uc.tenants.List(ctx, offset, batch)
		if err != nil {
			return err
		}
		for _, t := range tenants {
			fn(t)
		}
		if len(tenants) < batch {
			return nil
		}
		offset += batch
	}
}

// activeTenants recorre solo los tenants activos: los unicos que reciben trafico y, por
// tanto, los unicos que deben migrarse en un barrido.
func (uc *OrganizationUseCase) activeTenants(ctx context.Context, fn func(*domain.Tenant)) error {
	return uc.eachTenant(ctx, func(t *domain.Tenant) {
		if t.IsActive() {
			fn(t)
		}
	})
}

// ── Celdas ───────────────────────────────────────────────────────────────────

type CreateCellRequest struct {
	Code   string
	Region string
	DBHost string
	DBPort int
}

func (uc *OrganizationUseCase) CreateCell(ctx context.Context, req CreateCellRequest) (*domain.Cell, error) {
	code := strings.ToLower(strings.TrimSpace(req.Code))
	if !cellCodeRegex.MatchString(code) {
		return nil, domain.ErrInvalidCellCode
	}
	if existing, err := uc.cells.GetByCode(ctx, code); err == nil && existing != nil {
		return nil, domain.ErrCellCodeExists
	} else if err != nil && !errors.Is(err, domain.ErrCellNotFound) {
		return nil, err
	}
	cell := &domain.Cell{
		ID:     uuid.New(),
		Code:   code,
		Region: strings.TrimSpace(req.Region),
		Status: domain.CellStatusActive,
		DBHost: strings.TrimSpace(req.DBHost),
		DBPort: req.DBPort,
	}
	if err := uc.cells.Create(ctx, cell); err != nil {
		return nil, fmt.Errorf("crear celda: %w", err)
	}
	return cell, nil
}

func (uc *OrganizationUseCase) ListCells(ctx context.Context) ([]*domain.Cell, error) {
	return uc.cells.List(ctx)
}

type UpdateCellRequest struct {
	Status *string
	Region *string
}

// UpdateCell cambia el estado o la region de una celda. El host y el puerto no se
// editan por API: mover una celda de maquina es una operacion de datos, no un PATCH.
func (uc *OrganizationUseCase) UpdateCell(ctx context.Context, id uuid.UUID, req UpdateCellRequest) (*domain.Cell, error) {
	if req.Status == nil && req.Region == nil {
		return nil, domain.ErrNothingToUpdate
	}
	cell, err := uc.cells.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Status != nil {
		if !contains(domain.CellStatuses(), *req.Status) {
			return nil, domain.ErrInvalidCellStatus
		}
		cell.Status = *req.Status
	}
	if req.Region != nil {
		cell.Region = strings.TrimSpace(*req.Region)
	}
	if err := uc.cells.Update(ctx, cell); err != nil {
		return nil, fmt.Errorf("actualizar celda: %w", err)
	}
	return cell, nil
}

// ── Utilidades ───────────────────────────────────────────────────────────────

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// normalizePage deja pagina y tamano dentro de lo admitido. Pedir mas de lo permitido se
// RECORTA al maximo, en vez de caer al valor por defecto y devolver menos de lo pedido
// sin avisar.
func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// NormalizePage expone la normalizacion de paginado para que la capa HTTP construya la
// meta con los mismos valores que uso la consulta.
func NormalizePage(page, pageSize int) (int, int) { return normalizePage(page, pageSize) }

func strOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
