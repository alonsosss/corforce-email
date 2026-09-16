package app

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// memStore es una base en memoria con transacciones: lo escrito dentro de Transact se
// deshace si fn falla, y cada escritura y cada evento anotan en que transaccion ocurrieron.
type memStore struct {
	mu        sync.Mutex
	jobs      map[uuid.UUID]domain.JobDefinition
	execs     map[uuid.UUID]domain.JobExecution
	schedules map[uuid.UUID]domain.JobSchedule
	tasks     map[uuid.UUID]domain.ScheduledTask
	// taskWrites cuenta las escrituras de tareas (cancelar, marcar ejecutada).
	taskWrites int
	events     []recorded
	writes     []recorded
	txSeq      int
	// failPublish hace fallar la publicacion para comprobar que arrastra al dato.
	failPublish error
	// jobUpdates cuenta las escrituras de un trabajo que no lo desactivan (Update, Activate).
	jobUpdates  int
	deactivated int
	// planned cuenta las planificaciones sin ejecucion (SetNextRun).
	planned int
	// locks anota, en orden, las filas que se bloquean con GetForUpdate ("schedule", "job").
	locks []string
	// beforeJobUpdate, si esta, corre una vez al empezar la siguiente escritura de un trabajo.
	beforeJobUpdate func()
	// beforeTaskMark, si esta, corre una vez antes de marcar ejecutada una tarea: simula la
	// cancelacion que llega entre la lectura del barrido y su escritura.
	beforeTaskMark func()
}

type recorded struct {
	subject string
	execID  uuid.UUID
	retry   *domain.JobExecution
	timeout int
	tx      int
}

type txKey struct{}

func txOf(ctx context.Context) int {
	id, _ := ctx.Value(txKey{}).(int)
	return id
}

func newMemStore() *memStore {
	return &memStore{
		jobs:      map[uuid.UUID]domain.JobDefinition{},
		execs:     map[uuid.UUID]domain.JobExecution{},
		schedules: map[uuid.UUID]domain.JobSchedule{},
		tasks:     map[uuid.UUID]domain.ScheduledTask{},
	}
}

type memSnapshot struct {
	jobs          map[uuid.UUID]domain.JobDefinition
	execs         map[uuid.UUID]domain.JobExecution
	schedules     map[uuid.UUID]domain.JobSchedule
	tasks         map[uuid.UUID]domain.ScheduledTask
	events, write int
}

func (s *memStore) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	if txOf(ctx) != 0 {
		return fn(ctx)
	}
	s.mu.Lock()
	s.txSeq++
	id := s.txSeq
	snap := memSnapshot{jobs: map[uuid.UUID]domain.JobDefinition{}, execs: map[uuid.UUID]domain.JobExecution{},
		schedules: map[uuid.UUID]domain.JobSchedule{}, tasks: map[uuid.UUID]domain.ScheduledTask{},
		events: len(s.events), write: len(s.writes)}
	for k, v := range s.jobs {
		snap.jobs[k] = v
	}
	for k, v := range s.execs {
		snap.execs[k] = v
	}
	for k, v := range s.schedules {
		snap.schedules[k] = v
	}
	for k, v := range s.tasks {
		snap.tasks[k] = v
	}
	s.mu.Unlock()
	if err := fn(context.WithValue(ctx, txKey{}, id)); err != nil {
		s.mu.Lock()
		s.jobs, s.execs, s.schedules, s.tasks = snap.jobs, snap.execs, snap.schedules, snap.tasks
		s.events, s.writes = s.events[:snap.events], s.writes[:snap.write]
		s.mu.Unlock()
		return err
	}
	return nil
}

func visible(owner *uuid.UUID, tenantID uuid.UUID) bool { return owner == nil || *owner == tenantID }

type memJobs struct{ *memStore }

func (r memJobs) Create(_ context.Context, j *domain.JobDefinition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[j.ID] = *j
	return nil
}

func (r memJobs) GetByID(_ context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || !visible(j.TenantID, tenantID) {
		return nil, domain.ErrJobNotFound
	}
	return &j, nil
}

func (r memJobs) GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	r.mu.Lock()
	r.locks = append(r.locks, "job")
	r.mu.Unlock()
	return r.GetByID(ctx, id, tenantID)
}

func (r memJobs) GetByCode(_ context.Context, code string) (*domain.JobDefinition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.Code == code {
			return &j, nil
		}
	}
	return nil, domain.ErrJobNotFound
}

func (r memJobs) List(context.Context, domain.JobFilter) ([]*domain.JobOverview, int64, error) {
	return nil, 0, nil
}

// GetOverview lee como el repositorio: el calendario y la ejecucion creada mas reciente.
func (r memJobs) GetOverview(_ context.Context, id, tenantID uuid.UUID) (*domain.JobOverview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || !visible(j.TenantID, tenantID) {
		return nil, domain.ErrJobNotFound
	}
	var next, last *time.Time
	if s, ok := r.schedules[id]; ok {
		n := s.NextRunAt
		next, last = &n, s.LastRunAt
	}
	var latest *domain.JobExecution
	for _, e := range r.execs {
		if e.JobID == id && (latest == nil || e.CreatedAt.After(latest.CreatedAt)) {
			latest = &e
		}
	}
	var summary *domain.ExecutionSummary
	if latest != nil {
		summary = &domain.ExecutionSummary{ID: latest.ID, Status: latest.Status, CompletedAt: latest.CompletedAt, FailureReason: latest.FailureReason}
	}
	return domain.NewJobOverview(j, next, last, summary), nil
}

// sameOwner es la condicion de las escrituras por id del repositorio: la fila sigue siendo
// de esa empresa (nil, de plataforma).
func sameOwner(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Update escribe como el SQL: la definicion, sin is_active, solo sobre la version leida, que
// sube en uno.
func (r memJobs) Update(_ context.Context, j *domain.JobDefinition) error {
	r.mu.Lock()
	hook := r.beforeJobUpdate
	r.beforeJobUpdate = nil
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.jobs[j.ID]
	if !ok || !sameOwner(stored.TenantID, j.TenantID) {
		return domain.ErrJobNotFound
	}
	if stored.Version != j.Version {
		return domain.ErrJobVersionConflict
	}
	updated := *j
	updated.IsActive = stored.IsActive
	updated.Version = stored.Version + 1
	r.jobs[j.ID] = updated
	r.jobUpdates++
	return nil
}

func (r memJobs) Activate(_ context.Context, id uuid.UUID, owner *uuid.UUID, updatedAt time.Time) error {
	if err := r.setActive(id, owner, true, updatedAt); err != nil {
		return err
	}
	r.mu.Lock()
	r.jobUpdates++
	r.mu.Unlock()
	return nil
}

func (r memJobs) Deactivate(_ context.Context, id uuid.UUID, owner *uuid.UUID, updatedAt time.Time) error {
	if err := r.setActive(id, owner, false, updatedAt); err != nil {
		return err
	}
	r.mu.Lock()
	r.deactivated++
	r.mu.Unlock()
	return nil
}

func (r memJobs) setActive(id uuid.UUID, owner *uuid.UUID, active bool, updatedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || !sameOwner(j.TenantID, owner) {
		return domain.ErrJobNotFound
	}
	j.IsActive = active
	j.UpdatedAt = updatedAt
	r.jobs[id] = j
	return nil
}

type memExecs struct{ *memStore }

func (r memExecs) Create(ctx context.Context, e *domain.JobExecution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.RetryOf != nil {
		for _, other := range r.execs {
			if other.RetryOf != nil && *other.RetryOf == *e.RetryOf {
				return domain.ErrAlreadyRetried
			}
		}
	}
	r.execs[e.ID] = *e
	r.writes = append(r.writes, recorded{subject: "create", execID: e.ID, tx: txOf(ctx)})
	return nil
}

func (r memExecs) get(id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.execs[id]
	if !ok || !visible(e.TenantID, tenantID) {
		return nil, domain.ErrExecutionNotFound
	}
	return &e, nil
}

func (r memExecs) GetByID(_ context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	return r.get(id, tenantID)
}

func (r memExecs) GetForUpdate(_ context.Context, id, tenantID uuid.UUID) (*domain.JobExecution, error) {
	return r.get(id, tenantID)
}

// GetByJob pagina como el repositorio: las visibles para la empresa, las mas recientes antes.
func (r memExecs) GetByJob(_ context.Context, jobID, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []*domain.JobExecution
	for _, e := range r.execs {
		if e.JobID == jobID && visible(e.TenantID, tenantID) {
			c := e
			all = append(all, &c)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	return pageOf(all, page, perPage), int64(len(all)), nil
}

// pageOf corta la pagina con el desplazamiento del dominio, que satura en vez de desbordar.
func pageOf[T any](all []T, page, perPage int) []T {
	offset := domain.PageOffset(page, perPage)
	if offset >= int64(len(all)) {
		return nil
	}
	end := min(int(offset)+perPage, len(all))
	return all[offset:end]
}

func (r memExecs) Update(ctx context.Context, e *domain.JobExecution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stored, ok := r.execs[e.ID]; !ok || !sameOwner(stored.TenantID, e.TenantID) {
		return domain.ErrExecutionNotFound
	}
	r.execs[e.ID] = *e
	r.writes = append(r.writes, recorded{subject: "update", execID: e.ID, tx: txOf(ctx)})
	return nil
}

// ListRunning pagina como el repositorio: las activas visibles para la empresa, las mas
// recientes antes y el id como desempate.
func (r memExecs) ListRunning(_ context.Context, tenantID uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []*domain.JobExecution
	for _, e := range r.execs {
		if e.IsActive() && visible(e.TenantID, tenantID) {
			c := e
			all = append(all, &c)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID.String() > all[j].ID.String()
	})
	return pageOf(all, page, perPage), int64(len(all)), nil
}

// first devuelve la ejecucion que cumple ok con la menor clave de orden.
func (r memExecs) first(ok func(e domain.JobExecution) bool, key func(e domain.JobExecution) time.Time) *domain.JobExecution {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []domain.JobExecution
	for _, e := range r.execs {
		if ok(e) {
			found = append(found, e)
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Slice(found, func(i, j int) bool { return key(found[i]).Before(key(found[j])) })
	return &found[0]
}

func (r memExecs) ClaimOverdue(_ context.Context, now time.Time) (*domain.JobExecution, error) {
	return r.first(
		func(e domain.JobExecution) bool {
			return e.IsActive() && e.DeadlineAt != nil && !e.DeadlineAt.After(now)
		},
		func(e domain.JobExecution) time.Time { return *e.DeadlineAt }), nil
}

func (r memExecs) ClaimDispatchable(_ context.Context, now time.Time) (*domain.JobExecution, error) {
	return r.first(
		func(e domain.JobExecution) bool {
			return e.Status == domain.StatusPending && e.DeadlineAt == nil && e.NextAttemptAt != nil && !e.NextAttemptAt.After(now)
		},
		func(e domain.JobExecution) time.Time { return *e.NextAttemptAt }), nil
}

type memSchedules struct{ *memStore }

// GetForUpdate lee como el SQL: el calendario de un trabajo que ve la empresa, o nil.
func (r memSchedules) GetForUpdate(_ context.Context, jobID, tenantID uuid.UUID) (*domain.JobSchedule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.locks = append(r.locks, "schedule")
	s, ok := r.schedules[jobID]
	j, known := r.jobs[jobID]
	if !ok || !known || !visible(j.TenantID, tenantID) {
		return nil, nil
	}
	s.TenantID = j.TenantID
	return &s, nil
}

func (r memSchedules) UpdateNextRun(_ context.Context, jobID uuid.UUID, next, ranAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.schedules[jobID]
	s.JobID, s.NextRunAt, s.LastRunAt = jobID, next, &ranAt
	r.schedules[jobID] = s
	return nil
}

func (r memSchedules) GetDue(_ context.Context, now time.Time) ([]*domain.JobSchedule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.JobSchedule
	for id, s := range r.schedules {
		j := r.jobs[id]
		if j.IsActive && !s.NextRunAt.After(now) {
			s.TenantID = j.TenantID
			out = append(out, &s)
		}
	}
	return out, nil
}

// SetNextRun planifica sin marcar ejecucion; UpdateNextRun, en cambio, anota last_run_at.
func (r memSchedules) SetNextRun(_ context.Context, jobID uuid.UUID, next time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.schedules[jobID]
	s.JobID, s.NextRunAt = jobID, next
	r.schedules[jobID] = s
	r.planned++
	return nil
}

// Restart planifica como un alta: tampoco marca ejecucion y olvida last_run_at.
func (r memSchedules) Restart(_ context.Context, jobID uuid.UUID, next time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.schedules[jobID] = domain.JobSchedule{JobID: jobID, NextRunAt: next}
	r.planned++
	return nil
}

func (r memSchedules) ClaimDue(_ context.Context, jobID uuid.UUID, now time.Time) (time.Time, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.schedules[jobID]
	if !ok || s.NextRunAt.After(now) {
		return time.Time{}, false, nil
	}
	return s.NextRunAt, true, nil
}

func (r memSchedules) LockActiveCron(context.Context) ([]*domain.CronJobSchedule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.CronJobSchedule
	for id, s := range r.schedules {
		j := r.jobs[id]
		if j.IsActive && j.JobType == domain.JobTypeCron {
			out = append(out, &domain.CronJobSchedule{JobID: id, TenantID: j.TenantID, Expression: j.CronExpr(), Timezone: j.Timezone, NextRunAt: s.NextRunAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JobID.String() < out[j].JobID.String() })
	return out, nil
}

// memTasks es el repositorio de tareas con las mismas condiciones que el SQL: toda lectura y
// escritura por id lleva la empresa.
type memTasks struct{ *memStore }

func (r memTasks) Create(_ context.Context, t *domain.ScheduledTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[t.ID] = *t
	return nil
}

func (r memTasks) GetByID(_ context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok || t.TenantID != tenantID {
		return nil, domain.ErrTaskNotFound
	}
	return &t, nil
}

func (r memTasks) GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	return r.GetByID(ctx, id, tenantID)
}

func (r memTasks) ListPending(_ context.Context, f domain.TaskFilter) ([]*domain.ScheduledTask, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []*domain.ScheduledTask
	for _, t := range r.tasks {
		if t.TenantID == f.TenantID && t.Status == domain.TaskStatusScheduled && !t.TriggerAt.After(f.Before) {
			c := t
			all = append(all, &c)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].TriggerAt.Before(all[j].TriggerAt) })
	return pageOf(all, f.Page, f.PerPage), int64(len(all)), nil
}

func (r memTasks) Cancel(_ context.Context, id, tenantID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok || t.TenantID != tenantID || t.Status != domain.TaskStatusScheduled {
		return domain.ErrTaskNotFound
	}
	t.Status = domain.TaskStatusCancelled
	r.tasks[id] = t
	r.taskWrites++
	return nil
}

func (r memTasks) ListDue(_ context.Context, now time.Time) ([]*domain.ScheduledTask, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.ScheduledTask
	for _, t := range r.tasks {
		if t.Status == domain.TaskStatusScheduled && !t.TriggerAt.After(now) {
			c := t
			out = append(out, &c)
		}
	}
	return out, nil
}

func (r memTasks) CancelUndispatchable(_ context.Context, id uuid.UUID, reason string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok || t.Status != domain.TaskStatusScheduled {
		return false, nil
	}
	t.Status, t.FailureReason = domain.TaskStatusCancelled, &reason
	r.tasks[id] = t
	r.taskWrites++
	return true, nil
}

func (r memTasks) MarkExecuted(_ context.Context, id uuid.UUID, at time.Time) (bool, error) {
	r.mu.Lock()
	hook := r.beforeTaskMark
	r.beforeTaskMark = nil
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok || t.Status != domain.TaskStatusScheduled {
		return false, nil
	}
	t.Status, t.ExecutedAt = domain.TaskStatusExecuted, &at
	r.tasks[id] = t
	r.taskWrites++
	return true, nil
}

type memEvents struct{ *memStore }

func (r memEvents) record(ctx context.Context, ev recorded) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failPublish != nil {
		return r.failPublish
	}
	ev.tx = txOf(ctx)
	r.events = append(r.events, ev)
	return nil
}

func (r memEvents) JobStarted(ctx context.Context, _ *domain.JobDefinition, e *domain.JobExecution, timeout int) error {
	return r.record(ctx, recorded{subject: "scheduler.job.started", execID: e.ID, timeout: timeout})
}

func (r memEvents) JobCompleted(ctx context.Context, _ *domain.JobDefinition, e *domain.JobExecution) error {
	return r.record(ctx, recorded{subject: "scheduler.job.completed", execID: e.ID})
}

func (r memEvents) JobFailed(ctx context.Context, _ *domain.JobDefinition, e, retry *domain.JobExecution) error {
	return r.record(ctx, recorded{subject: "scheduler.job.failed", execID: e.ID, retry: retry})
}

func (r memEvents) TaskStarted(ctx context.Context, t *domain.ScheduledTask) error {
	return r.record(ctx, recorded{subject: "scheduler.task.started", execID: t.ID})
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

const (
	tenantHandler   = "reports.daily"
	platformHandler = "ops.cleanup"
)

type fixture struct {
	t        *testing.T
	store    *memStore
	clock    *clock
	uc       *SchedulerUseCase
	tenantID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	catalog, err := domain.NewHandlerCatalog([]domain.HandlerSpec{
		{Name: tenantHandler, Service: "reports", MaxTimeoutSeconds: 600, Scopes: []domain.HandlerScope{domain.ScopeTenant, domain.ScopePlatform}},
		{Name: platformHandler, Service: "ops", MaxTimeoutSeconds: 60, Scopes: []domain.HandlerScope{domain.ScopePlatform}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := newMemStore()
	clk := &clock{t: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	uc := NewSchedulerUseCase(SchedulerDeps{
		Jobs: memJobs{s}, Executions: memExecs{s}, Schedules: memSchedules{s}, Tasks: memTasks{s}, Events: memEvents{s}, Tx: s,
		Catalog: catalog, Retry: domain.RetryPolicy{BaseDelay: time.Minute, MaxDelay: 10 * time.Minute},
		Now: clk.now, Logger: zap.NewNop(),
	})
	return &fixture{t: t, store: s, clock: clk, uc: uc, tenantID: uuid.New()}
}

// addJob guarda un trabajo de la empresa (activo, cada 5 minutos, en UTC, plazo de 120 s,
// dos reintentos y recien creado) con los cambios de mut.
func (f *fixture) addJob(mut func(j *domain.JobDefinition)) domain.JobDefinition {
	tenant, five := f.tenantID, 5
	j := domain.JobDefinition{
		ID: uuid.New(), TenantID: &tenant, Name: "Informe", Code: uuid.NewString(), JobType: domain.JobTypeInterval,
		Timezone: domain.DefaultTimezone, IntervalMinutes: &five, Handler: tenantHandler, IsActive: true, MaxRetries: 2, TimeoutSeconds: 120,
		Version: domain.FirstJobVersion, CreatedAt: f.clock.now(), UpdatedAt: f.clock.now(),
	}
	if mut != nil {
		mut(&j)
	}
	f.store.jobs[j.ID] = j
	return j
}

// versionOf es la version guardada del trabajo: la que leeria quien lo edite ahora.
func (f *fixture) versionOf(id uuid.UUID) int64 { return f.store.jobs[id].Version }

func (f *fixture) exec(id uuid.UUID) domain.JobExecution {
	f.t.Helper()
	e, ok := f.store.execs[id]
	if !ok {
		f.t.Fatalf("no existe la ejecucion %s", id)
	}
	return e
}

func (f *fixture) eventsOf(subject string) []recorded {
	var out []recorded
	for _, e := range f.store.events {
		if e.subject == subject {
			out = append(out, e)
		}
	}
	return out
}

func (f *fixture) run(job domain.JobDefinition) domain.JobExecution {
	f.t.Helper()
	exec, err := f.uc.RunJob(context.Background(), f.tenantID, job.ID)
	if err != nil {
		f.t.Fatalf("run: %v", err)
	}
	return *exec
}
