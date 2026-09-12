package app

import (
	"context"
	"sort"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

// fakeTenantRepo guarda tenants en memoria; solo se ejercitan los metodos que usan los
// casos de uso probados.
type fakeTenantRepo struct {
	tenants []*domain.Tenant
	updated []*domain.Tenant
}

func (f *fakeTenantRepo) Create(_ context.Context, t *domain.Tenant) error {
	f.tenants = append(f.tenants, t)
	return nil
}

func (f *fakeTenantRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Tenant, error) {
	for _, t := range f.tenants {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, domain.ErrTenantNotFound
}

func (f *fakeTenantRepo) GetBySlug(_ context.Context, slug string) (*domain.Tenant, error) {
	for _, t := range f.tenants {
		if t.Slug == slug {
			return t, nil
		}
	}
	return nil, domain.ErrTenantNotFound
}

func (f *fakeTenantRepo) List(_ context.Context, offset, limit int) ([]*domain.Tenant, int64, error) {
	if offset >= len(f.tenants) {
		return nil, int64(len(f.tenants)), nil
	}
	end := offset + limit
	if end > len(f.tenants) {
		end = len(f.tenants)
	}
	return f.tenants[offset:end], int64(len(f.tenants)), nil
}

func (f *fakeTenantRepo) Update(_ context.Context, t *domain.Tenant) error {
	f.updated = append(f.updated, t)
	return nil
}

func (f *fakeTenantRepo) Delete(_ context.Context, id uuid.UUID) error {
	for i, t := range f.tenants {
		if t.ID == id {
			f.tenants = append(f.tenants[:i], f.tenants[i+1:]...)
			return nil
		}
	}
	return domain.ErrTenantNotFound
}

// fakeCellRepo es el directorio de celdas en memoria.
type fakeCellRepo struct {
	cells []*domain.Cell
}

func (f *fakeCellRepo) Create(_ context.Context, c *domain.Cell) error {
	f.cells = append(f.cells, c)
	return nil
}

func (f *fakeCellRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Cell, error) {
	for _, c := range f.cells {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, domain.ErrCellNotFound
}

func (f *fakeCellRepo) GetByCode(_ context.Context, code string) (*domain.Cell, error) {
	for _, c := range f.cells {
		if c.Code == code {
			return c, nil
		}
	}
	return nil, domain.ErrCellNotFound
}

func (f *fakeCellRepo) List(context.Context) ([]*domain.Cell, error) { return f.cells, nil }
func (f *fakeCellRepo) Update(context.Context, *domain.Cell) error   { return nil }

// fakeProvisioner devuelve por nombre de base el resultado que se quiere simular y
// registra que bases se crearon, migraron o borraron.
type fakeProvisioner struct {
	runErr  map[string]error
	status  map[string]domain.TenantMigrationStatus
	ran     []string
	created []string
	dropped []string
}

func (f *fakeProvisioner) CreateDatabase(_ context.Context, target domain.DBTarget) error {
	f.created = append(f.created, target.DBName)
	return nil
}

func (f *fakeProvisioner) DropDatabase(_ context.Context, target domain.DBTarget) error {
	f.dropped = append(f.dropped, target.DBName)
	return nil
}

func (f *fakeProvisioner) RunMigrations(_ context.Context, target domain.DBTarget) error {
	f.ran = append(f.ran, target.DBName)
	return f.runErr[target.DBName]
}

func (f *fakeProvisioner) MigrationStatus(_ context.Context, target domain.DBTarget) (domain.TenantMigrationStatus, error) {
	return f.status[target.DBName], nil
}

type fakeRoleSeeder struct{ seeded []uuid.UUID }

func (f *fakeRoleSeeder) SeedDefaultRoles(_ context.Context, tenantID uuid.UUID) error {
	f.seeded = append(f.seeded, tenantID)
	return nil
}

type fakeAdminSeeder struct {
	tenantID uuid.UUID
	email    string
	password string
}

func (f *fakeAdminSeeder) CreateAdminUser(_ context.Context, tenantID uuid.UUID, email, password, _, _ string) error {
	f.tenantID, f.email, f.password = tenantID, email, password
	return nil
}

// fakeModulesRepo mantiene catalogo y estado por tenant en memoria.
type fakeModulesRepo struct {
	catalog []domain.ModuleCatalogEntry
	states  map[uuid.UUID]map[string]bool
}

func (f *fakeModulesRepo) ListCatalog(context.Context) ([]domain.ModuleCatalogEntry, error) {
	return f.catalog, nil
}

func (f *fakeModulesRepo) HasAny(_ context.Context, tenantID uuid.UUID) (bool, error) {
	return len(f.states[tenantID]) > 0, nil
}

func (f *fakeModulesRepo) ListForTenant(_ context.Context, tenantID uuid.UUID) ([]domain.TenantModuleState, error) {
	var out []domain.TenantModuleState
	for m, en := range f.states[tenantID] {
		out = append(out, domain.TenantModuleState{Module: m, Enabled: en})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out, nil
}

func (f *fakeModulesRepo) Upsert(_ context.Context, tenantID uuid.UUID, module string, enabled bool) error {
	if f.states == nil {
		f.states = map[uuid.UUID]map[string]bool{}
	}
	if f.states[tenantID] == nil {
		f.states[tenantID] = map[string]bool{}
	}
	f.states[tenantID][module] = enabled
	return nil
}

func (f *fakeModulesRepo) ReplaceForTenant(_ context.Context, tenantID uuid.UUID, states map[string]bool) error {
	if f.states == nil {
		f.states = map[uuid.UUID]map[string]bool{}
	}
	copied := make(map[string]bool, len(states))
	for m, en := range states {
		copied[m] = en
	}
	f.states[tenantID] = copied
	return nil
}

func (f *fakeModulesRepo) DeleteForTenant(_ context.Context, tenantID uuid.UUID) error {
	delete(f.states, tenantID)
	return nil
}

// fakePublisher registra los eventos publicados para comprobarlos.
type fakePublisher struct {
	created  []uuid.UUID
	statuses []string
	modules  [][]string
}

func (f *fakePublisher) TenantCreated(_ context.Context, t *domain.Tenant) error {
	f.created = append(f.created, t.ID)
	return nil
}

func (f *fakePublisher) TenantStatusChanged(_ context.Context, t *domain.Tenant, previous string) error {
	f.statuses = append(f.statuses, previous+"->"+t.Status)
	return nil
}

func (f *fakePublisher) TenantModulesChanged(_ context.Context, _ uuid.UUID, enabled, _ []string) error {
	f.modules = append(f.modules, enabled)
	return nil
}

// catalogFixture refleja el catalogo que siembra migrations/registry/001_organization.sql.
// Mantener sincronizado si cambia el catalogo.
func catalogFixture() []domain.ModuleCatalogEntry {
	return []domain.ModuleCatalogEntry{
		{Module: "corporate_mail", Tier: domain.ModuleTierOptional, Label: "Correo corporativo",
			PermissionModules: []string{"domains", "mailboxes", "mail_routing", "mail_security", "mail_storage"}},
		{Module: "transactional", Tier: domain.ModuleTierOptional, Label: "Correo transaccional",
			PermissionModules: []string{"transactional", "templates", "suppression", "reputation"}},
		{Module: "marketing", Tier: domain.ModuleTierOptional, Label: "Marketing", Requires: []string{"transactional"},
			PermissionModules: []string{"contacts", "segments", "campaigns", "automations", "analytics"}},
	}
}

func reqMapFixture() map[string][]string {
	out := map[string][]string{}
	for _, e := range catalogFixture() {
		out[e.Module] = e.Requires
	}
	return out
}
