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
	events    []recorded
	writes    []recorded
	txSeq     int
	// failPublish hace fallar la publicacion para comprobar que arrastra al dato.
	failPublish error
	jobUpdates  int
	deactivated int
	// planned cuenta las planificaciones sin ejecucion (SetNextRun).
	planned int
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
	}
}

type memSnapshot struct {
	jobs          map[uuid.UUID]domain.JobDefinition
	execs         map[uuid.UUID]domain.JobExecution
	schedules     map[uuid.UUID]domain.JobSchedule
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
		schedules: map[uuid.UUID]domain.JobSchedule{}, events: len(s.events), write: len(s.writes)}
	for k, v := range s.jobs {
		snap.jobs[k] = v
	}
	for k, v := range s.execs {
		snap.execs[k] = v
	}
	for k, v := range s.schedules {
		snap.schedules[k] = v
	}
	s.mu.Unlock()
	if err := fn(context.WithValue(ctx, txKey{}, id)); err != nil {
		s.mu.Lock()
		s.jobs, s.execs, s.schedules = snap.jobs, snap.execs, snap.schedules
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

func (r memJobs) Update(_ context.Context, j *domain.JobDefinition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[j.ID] = *j
	r.jobUpdates++
	return nil
}

func (r memJobs) Deactivate(_ context.Context, id uuid.UUID, updatedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.jobs[id]
	j.IsActive = false
	j.UpdatedAt = updatedAt
	r.jobs[id] = j
	r.deactivated++
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

func (r memExecs) GetByJob(context.Context, uuid.UUID, int, int) ([]*domain.JobExecution, int64, error) {
	return nil, 0, nil
}

func (r memExecs) Update(ctx context.Context, e *domain.JobExecution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execs[e.ID] = *e
	r.writes = append(r.writes, recorded{subject: "update", execID: e.ID, tx: txOf(ctx)})
	return nil
}

func (r memExecs) ListRunning(context.Context) ([]*domain.JobExecution, error) { return nil, nil }

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
			out = append(out, &domain.CronJobSchedule{JobID: id, Expression: j.CronExpr(), Timezone: j.Timezone, NextRunAt: s.NextRunAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JobID.String() < out[j].JobID.String() })
	return out, nil
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
		Jobs: memJobs{s}, Executions: memExecs{s}, Schedules: memSchedules{s}, Events: memEvents{s}, Tx: s,
		Catalog: catalog, Retry: domain.RetryPolicy{BaseDelay: time.Minute, MaxDelay: 10 * time.Minute},
		Now: clk.now, Logger: zap.NewNop(),
	})
	return &fixture{t: t, store: s, clock: clk, uc: uc, tenantID: uuid.New()}
}

// addJob guarda un trabajo de la empresa (activo, cada 5 minutos, en UTC, plazo de 120 s y
// dos reintentos) con los cambios de mut.
func (f *fixture) addJob(mut func(j *domain.JobDefinition)) domain.JobDefinition {
	tenant, five := f.tenantID, 5
	j := domain.JobDefinition{
		ID: uuid.New(), TenantID: &tenant, Name: "Informe", Code: uuid.NewString(), JobType: domain.JobTypeInterval,
		Timezone: domain.DefaultTimezone, IntervalMinutes: &five, Handler: tenantHandler, IsActive: true, MaxRetries: 2, TimeoutSeconds: 120,
		CreatedAt: f.clock.now(), UpdatedAt: f.clock.now(),
	}
	if mut != nil {
		mut(&j)
	}
	f.store.jobs[j.ID] = j
	return j
}

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
