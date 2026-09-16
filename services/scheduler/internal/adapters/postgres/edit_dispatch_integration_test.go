//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/google/uuid"
)

// gate detiene una transaccion en un punto exacto, con sus bloqueos tomados y sin confirmar,
// hasta que la prueba la suelta.
type gate struct {
	reached, release chan struct{}
	once             *sync.Once
}

func newGate(t *testing.T) gate {
	g := gate{reached: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
	t.Cleanup(g.open)
	return g
}

func (g gate) stop() {
	close(g.reached)
	<-g.release
}

func (g gate) open() { g.once.Do(func() { close(g.release) }) }

// pausedDeactivate para la transaccion despues de desactivar el trabajo, con el calendario y
// el trabajo bloqueados: el despacho de un one_time (ClaimDue y Deactivate) o una
// desactivacion (lockTenantJob y Deactivate).
type pausedDeactivate struct {
	ports.JobDefinitionRepository
	gate
}

func (p pausedDeactivate) Deactivate(ctx context.Context, id uuid.UUID, owner *uuid.UUID, at time.Time) error {
	if err := p.JobDefinitionRepository.Deactivate(ctx, id, owner, at); err != nil {
		return err
	}
	p.stop()
	return nil
}

// pausedEdit para la edicion despues de escribir la definicion, antes de replanificarla.
type pausedEdit struct {
	ports.JobDefinitionRepository
	gate
}

func (p pausedEdit) Update(ctx context.Context, job *domain.JobDefinition) error {
	if err := p.JobDefinitionRepository.Update(ctx, job); err != nil {
		return err
	}
	p.stop()
	return nil
}

type updateResult struct {
	o   *domain.JobOverview
	err error
}

func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal(what)
	}
	var zero T
	return zero
}

// waitLockWaiters espera a que n transacciones de esta base esten paradas en un bloqueo.
func (e *env) waitLockWaiters(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for e.count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) < n {
		if time.Now().After(deadline) {
			t.Fatalf("no hay %d transacciones esperando un bloqueo", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) createOneTime(t *testing.T) *domain.JobDefinition {
	t.Helper()
	tenant := e.tenant
	job := &domain.JobDefinition{TenantID: &tenant, Name: "Una vez", Code: "it-" + uuid.NewString(), JobType: domain.JobTypeOneTime,
		Timezone: domain.DefaultTimezone, Handler: "it.report", TimeoutSeconds: 30}
	if _, err := e.uc.CreateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

func (e *env) executionsOf(t *testing.T, jobID uuid.UUID) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, jobID)
}

// El despacho de un one_time tiene tomados el calendario y el trabajo cuando llegan una
// edicion leida antes (el PUT lee con GetJob) y una reactivacion: esperan a que confirme y
// leen lo que dejo. Ninguna lo vuelve a activar.
func TestLaEdicionYLaReactivacionEsperanAlDespachoEnCurso(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createOneTime(t)
	stale, err := e.uc.GetJob(e.ctx, job.ID, e.tenant)
	if err != nil || !stale.IsActive {
		t.Fatalf("lectura del PUT: %+v (%v)", stale, err)
	}

	g := newGate(t)
	deps := e.deps
	deps.Jobs = pausedDeactivate{deps.Jobs, g}
	dispatcher := app.NewSchedulerUseCase(deps)
	e.clock.set(scopeStart.Add(30 * time.Second))
	dispatched := make(chan struct{})
	go func() { dispatcher.ProcessDueJobs(e.ctx); close(dispatched) }()
	within(t, g.reached, "el despacho no llego a desactivar el one_time")

	edited := make(chan updateResult, 1)
	enabled := make(chan error, 1)
	stale.Name = "Editado"
	go func() {
		o, err := e.uc.UpdateJob(e.ctx, stale)
		edited <- updateResult{o, err}
	}()
	go func() { enabled <- e.uc.EnableJob(e.ctx, job.ID, e.tenant) }()
	e.waitLockWaiters(t, 2)
	g.open()
	within(t, dispatched, "el despacho no termino")

	r := within(t, edited, "la edicion no termino")
	if r.err != nil || r.o.Job.Name != "Editado" || r.o.Job.IsActive || !r.o.AlreadyRun {
		t.Fatalf("la edicion se guarda sin reactivarlo: %+v (%v)", r.o, r.err)
	}
	if err := within(t, enabled, "la reactivacion no termino"); !errors.Is(err, domain.ErrOneTimeAlreadyRun) {
		t.Fatalf("reactivar lo que el despacho acaba de lanzar: %v", err)
	}
	e.clock.advance(time.Minute)
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.executionsOf(t, job.ID); n != 1 {
		t.Fatalf("un one_time corre una vez: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND NOT is_active AND name = 'Editado'`, job.ID); n != 1 {
		t.Fatal("queda inactivo con la edicion guardada")
	}
}

// Una edicion que cambia el calendario de un one_time vencido tiene tomados el calendario y
// el trabajo: el despacho salta ese calendario sin esperarla (ni interbloquearse con ella) y,
// al confirmarse, rige el calendario nuevo.
func TestUnaEdicionEnCursoApartaAlDespacho(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createOneTime(t)
	stale, err := e.uc.GetJob(e.ctx, job.ID, e.tenant)
	if err != nil {
		t.Fatal(err)
	}

	g := newGate(t)
	deps := e.deps
	deps.Jobs = pausedEdit{deps.Jobs, g}
	editor := app.NewSchedulerUseCase(deps)
	e.clock.set(scopeStart.Add(30 * time.Second))
	five := 5
	stale.JobType, stale.IntervalMinutes = domain.JobTypeInterval, &five
	edited := make(chan updateResult, 1)
	go func() {
		o, err := editor.UpdateJob(e.ctx, stale)
		edited <- updateResult{o, err}
	}()
	within(t, g.reached, "la edicion no llego a escribir")

	dispatched := make(chan struct{})
	go func() { e.uc.ProcessDueJobs(e.ctx); close(dispatched) }()
	within(t, dispatched, "el despacho espero a la edicion en vez de saltar el calendario")
	if n := e.executionsOf(t, job.ID); n != 0 {
		t.Fatalf("durante la edicion no se despacha: %d", n)
	}

	g.open()
	r := within(t, edited, "la edicion no termino")
	want := e.clock.now().Add(5 * time.Minute)
	next, last := e.schedule(t, job.ID)
	if r.err != nil || !r.o.Job.IsActive || r.o.Job.JobType != domain.JobTypeInterval || !next.Equal(want) || last != nil {
		t.Fatalf("rige el calendario nuevo: %+v (%v), next_run_at %v (se esperaba %v), last_run_at %v", r.o, r.err, next, want, last)
	}
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.executionsOf(t, job.ID); n != 0 {
		t.Fatalf("antes de su hora no corre: %d", n)
	}
	e.clock.set(want)
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.executionsOf(t, job.ID); n != 1 {
		t.Fatalf("en su hora corre una vez: %d", n)
	}
}

func TestEscriturasDelEstadoYBloqueosContraLaBase(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createJob(t, 0)
	if err := e.uc.DisableJob(e.ctx, job.ID, e.tenant); err != nil {
		t.Fatal(err)
	}
	stale := *job
	stale.IsActive, stale.Name, stale.UpdatedAt = true, "Editado", scopeStart
	if err := e.deps.Jobs.Update(e.ctx, &stale); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND name = 'Editado' AND NOT is_active`, job.ID); n != 1 {
		t.Fatal("Update guarda la definicion y no toca is_active")
	}

	other := uuid.New()
	if err := e.deps.Jobs.Activate(e.ctx, job.ID, &other, scopeStart); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("activar con otra empresa: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND NOT is_active`, job.ID); n != 1 {
		t.Fatal("otra empresa no lo activa")
	}
	if err := e.deps.Jobs.Activate(e.ctx, job.ID, &e.tenant, scopeStart); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND is_active`, job.ID); n != 1 {
		t.Fatal("su empresa lo activa")
	}

	foreign := e.insertJob(t, &other, "Ajeno")
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO scheduler.job_schedules (job_id, next_run_at) VALUES ($1, $2)`, foreign, scopeStart); err != nil {
		t.Fatal(err)
	}
	if _, err := e.deps.Jobs.GetForUpdate(e.ctx, foreign, e.tenant); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("bloquear un trabajo ajeno: %v", err)
	}
	if s, err := e.deps.Schedules.GetForUpdate(e.ctx, foreign, e.tenant); err != nil || s != nil {
		t.Fatalf("bloquear el calendario de un trabajo ajeno: %+v (%v)", s, err)
	}
	s, err := e.deps.Schedules.GetForUpdate(e.ctx, job.ID, e.tenant)
	if err != nil || s == nil || !s.NextRunAt.Equal(scopeStart.Add(5*time.Minute)) || s.LastRunAt != nil || s.TenantID == nil || *s.TenantID != e.tenant {
		t.Fatalf("calendario propio: %+v (%v)", s, err)
	}
	if s, err := e.deps.Schedules.GetForUpdate(e.ctx, uuid.New(), e.tenant); err != nil || s != nil {
		t.Fatalf("sin calendario: %+v (%v)", s, err)
	}
}
