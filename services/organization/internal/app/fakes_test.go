package app

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/alonsosss/corforce-email/services/organization/internal/ports"
	"github.com/google/uuid"
)

// fakeTenantRepo guarda tenants en memoria; solo se ejercitan los metodos que usan los
// casos de uso probados.
type fakeTenantRepo struct {
	tenants []*domain.Tenant
	updated []*domain.Tenant
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

func (f *fakeTenantRepo) remove(id uuid.UUID) {
	for i, t := range f.tenants {
		if t.ID == id {
			f.tenants = append(f.tenants[:i], f.tenants[i+1:]...)
			return
		}
	}
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

// callLog registra, en orden, lo que la saga pide a cada dueno de datos (la base de la
// empresa, access-control e identity), y hace fallar la llamada que se le indique.
type callLog struct {
	calls []string
	fail  map[string]int
}

// fakeError es el fallo simulado de una llamada: su nombre queda en el error.
type fakeError string

func (e fakeError) Error() string { return "fallo simulado en " + string(e) }

// failOn hace fallar las proximas times llamadas con ese nombre.
func (l *callLog) failOn(name string, times int) {
	if l.fail == nil {
		l.fail = map[string]int{}
	}
	l.fail[name] = times
}

func (l *callLog) record(name string) error {
	l.calls = append(l.calls, name)
	if n := l.fail[name]; n > 0 {
		l.fail[name] = n - 1
		return fakeError(name)
	}
	return nil
}

// fakeSagaRepo guarda las sagas en memoria con la semantica de arriendo del repositorio
// real y con su propio reloj: las pruebas no dependen de la hora del sistema. Guarda
// copias, como la base: la saga del caso de uso y la persistida son objetos distintos.
type fakeSagaRepo struct {
	tenants *fakeTenantRepo
	sagas   map[uuid.UUID]*domain.TenantSaga
	now     time.Time
	// saveFailAt hace fallar el Save numero saveFailAt (1 = el primero): el registro cae
	// justo despues de un paso, que es lo que ve la saga cuando su instancia muere ahi.
	saves       int
	saveFailAt  int
	completeErr error
}

func newFakeSagaRepo(tenants *fakeTenantRepo) *fakeSagaRepo {
	return &fakeSagaRepo{
		tenants: tenants,
		sagas:   map[uuid.UUID]*domain.TenantSaga{},
		now:     time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

func (f *fakeSagaRepo) leased(s *domain.TenantSaga) bool {
	return s.LeaseUntil != nil && s.LeaseUntil.After(f.now)
}

func (f *fakeSagaRepo) setLease(s *domain.TenantSaga, d time.Duration) {
	if d <= 0 {
		s.LeaseUntil = nil
		return
	}
	until := f.now.Add(d)
	s.LeaseUntil = &until
}

func (f *fakeSagaRepo) store(s *domain.TenantSaga) {
	c := *s
	f.sagas[s.TenantID] = &c
}

func (f *fakeSagaRepo) BeginCreate(_ context.Context, t *domain.Tenant, s *domain.TenantSaga, lease time.Duration) error {
	for _, existing := range f.tenants.tenants {
		if existing.Slug == t.Slug || existing.DBName == t.DBName {
			return domain.ErrTenantAlreadyExists
		}
	}
	f.tenants.tenants = append(f.tenants.tenants, t)
	s.LeaseToken, s.Attempts = uuid.New(), 1
	f.setLease(s, lease)
	f.store(s)
	return nil
}

func (f *fakeSagaRepo) Insert(_ context.Context, s *domain.TenantSaga, lease time.Duration) error {
	if _, ok := f.sagas[s.TenantID]; ok {
		return domain.ErrTenantBusy
	}
	s.LeaseToken, s.Attempts = uuid.New(), 1
	f.setLease(s, lease)
	f.store(s)
	return nil
}

func (f *fakeSagaRepo) Get(_ context.Context, id uuid.UUID) (*domain.TenantSaga, error) {
	s, ok := f.sagas[id]
	if !ok {
		return nil, domain.ErrSagaNotFound
	}
	c := *s
	return &c, nil
}

func (f *fakeSagaRepo) Claim(_ context.Context, id uuid.UUID, lease time.Duration) (*domain.TenantSaga, error) {
	s, ok := f.sagas[id]
	if !ok {
		return nil, domain.ErrSagaNotFound
	}
	if f.leased(s) {
		return nil, domain.ErrTenantBusy
	}
	s.LeaseToken = uuid.New()
	s.Attempts++
	f.setLease(s, lease)
	c := *s
	return &c, nil
}

func (f *fakeSagaRepo) Save(_ context.Context, s *domain.TenantSaga, lease time.Duration) error {
	f.saves++
	if f.saveFailAt > 0 && f.saves == f.saveFailAt {
		return errors.New("registro caido")
	}
	stored, ok := f.sagas[s.TenantID]
	if !ok || stored.LeaseToken != s.LeaseToken {
		return domain.ErrLeaseLost
	}
	f.setLease(s, lease)
	f.store(s)
	return nil
}

func (f *fakeSagaRepo) CompleteCreate(_ context.Context, s *domain.TenantSaga) error {
	if f.completeErr != nil {
		return f.completeErr
	}
	stored, ok := f.sagas[s.TenantID]
	if !ok || stored.LeaseToken != s.LeaseToken {
		return domain.ErrLeaseLost
	}
	s.State, s.Step, s.LastError, s.LeaseUntil = domain.SagaCompleted, domain.StepActivated, "", nil
	f.store(s)
	for _, t := range f.tenants.tenants {
		if t.ID == s.TenantID {
			t.Status = domain.TenantStatusActive
		}
	}
	return nil
}

func (f *fakeSagaRepo) DeleteTenant(_ context.Context, s *domain.TenantSaga) error {
	stored, ok := f.sagas[s.TenantID]
	if !ok || stored.LeaseToken != s.LeaseToken {
		return domain.ErrLeaseLost
	}
	delete(f.sagas, s.TenantID)
	f.tenants.remove(s.TenantID)
	return nil
}

func (f *fakeSagaRepo) ListStale(_ context.Context, limit int) ([]*domain.TenantSaga, error) {
	var out []*domain.TenantSaga
	for _, s := range f.sagas {
		if (s.State == domain.SagaRunning || s.State == domain.SagaCompensating) && !f.leased(s) {
			c := *s
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID.String() < out[j].TenantID.String() })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// fakeProvisioner simula las bases de la celda con su marca: owner guarda, por base, la
// empresa que la creo (uuid.Nil para una base ajena, sin marca).
type fakeProvisioner struct {
	log     *callLog
	runErr  map[string]error
	status  map[string]domain.TenantMigrationStatus
	owner   map[string]uuid.UUID
	ran     []string
	created []string
	dropped []string
}

func (f *fakeProvisioner) note(name string) error {
	if f.log == nil {
		return nil
	}
	return f.log.record(name)
}

func (f *fakeProvisioner) CreateDatabase(_ context.Context, target domain.DBTarget, tenantID uuid.UUID) error {
	if err := f.note("db.create"); err != nil {
		return err
	}
	if f.owner == nil {
		f.owner = map[string]uuid.UUID{}
	}
	if owner, ok := f.owner[target.DBName]; ok && owner != tenantID {
		return domain.ErrDatabaseOccupied
	}
	f.owner[target.DBName] = tenantID
	f.created = append(f.created, target.DBName)
	return nil
}

func (f *fakeProvisioner) DropOwnedDatabase(_ context.Context, target domain.DBTarget, tenantID uuid.UUID) error {
	if err := f.note("db.drop"); err != nil {
		return err
	}
	if owner, ok := f.owner[target.DBName]; ok && owner == tenantID {
		delete(f.owner, target.DBName)
		f.dropped = append(f.dropped, target.DBName)
	}
	return nil
}

func (f *fakeProvisioner) RunMigrations(_ context.Context, target domain.DBTarget) error {
	if err := f.note("db.migrate"); err != nil {
		return err
	}
	f.ran = append(f.ran, target.DBName)
	return f.runErr[target.DBName]
}

func (f *fakeProvisioner) MigrationStatus(_ context.Context, target domain.DBTarget) (domain.TenantMigrationStatus, error) {
	return f.status[target.DBName], nil
}

// fakeAccess hace de access-control: el rol del sistema de cada empresa y las asignaciones.
type fakeAccess struct {
	log      *callLog
	roles    map[uuid.UUID]uuid.UUID
	assigned map[uuid.UUID]uuid.UUID
	reseeds  int
}

func newFakeAccess(log *callLog) *fakeAccess {
	return &fakeAccess{log: log, roles: map[uuid.UUID]uuid.UUID{}, assigned: map[uuid.UUID]uuid.UUID{}}
}

func (f *fakeAccess) SeedTenantAdminRole(_ context.Context, tenantID uuid.UUID) (uuid.UUID, error) {
	if err := f.log.record("access.seed"); err != nil {
		return uuid.Nil, err
	}
	if id, ok := f.roles[tenantID]; ok {
		return id, nil
	}
	id := uuid.New()
	f.roles[tenantID] = id
	return id, nil
}

func (f *fakeAccess) ReseedSystemRoles(context.Context) (int, error) {
	if err := f.log.record("access.reseed"); err != nil {
		return 0, err
	}
	f.reseeds++
	return len(f.roles), nil
}

func (f *fakeAccess) AssignRole(_ context.Context, tenantID, userID, roleID uuid.UUID) error {
	if err := f.log.record("access.assign"); err != nil {
		return err
	}
	if f.roles[tenantID] != roleID {
		return errors.New("rol de otra empresa")
	}
	f.assigned[userID] = roleID
	return nil
}

func (f *fakeAccess) RevokeRole(_ context.Context, _, userID, _ uuid.UUID) error {
	if err := f.log.record("access.revoke"); err != nil {
		return err
	}
	delete(f.assigned, userID)
	return nil
}

func (f *fakeAccess) RemoveTenantRoles(_ context.Context, tenantID uuid.UUID) error {
	if err := f.log.record("access.remove"); err != nil {
		return err
	}
	roleID := f.roles[tenantID]
	delete(f.roles, tenantID)
	for user, role := range f.assigned {
		if role == roleID {
			delete(f.assigned, user)
		}
	}
	return nil
}

// fakeIdentity hace de identity: el primer usuario de cada empresa, con la misma regla de
// repeticion (mismo id, correo y contrasena) que el servicio real.
type fakeIdentity struct {
	log    *callLog
	users  map[uuid.UUID]ports.FirstAdmin
	reject *domain.AdminRejectedError
}

func newFakeIdentity(log *callLog) *fakeIdentity {
	return &fakeIdentity{log: log, users: map[uuid.UUID]ports.FirstAdmin{}}
}

func (f *fakeIdentity) CreateFirstUser(_ context.Context, tenantID uuid.UUID, admin ports.FirstAdmin) error {
	if err := f.log.record("identity.create"); err != nil {
		return err
	}
	if f.reject != nil {
		return f.reject
	}
	if u, ok := f.users[tenantID]; ok {
		if u.UserID != admin.UserID || u.Email != admin.Email || u.Password != admin.Password {
			return domain.ErrAdminUserConflict
		}
		return nil
	}
	f.users[tenantID] = admin
	return nil
}

func (f *fakeIdentity) RemoveTenantUsers(_ context.Context, tenantID uuid.UUID) error {
	if err := f.log.record("identity.remove"); err != nil {
		return err
	}
	delete(f.users, tenantID)
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
