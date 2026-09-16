//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/google/uuid"
)

// pausedReplan para el despacho de un interval despues de replanificarlo: tiene el calendario
// (ClaimDue), la ejecucion creada y su evento de inicio en la outbox, sin confirmar.
type pausedReplan struct {
	ports.JobScheduleRepository
	gate
}

func (p pausedReplan) UpdateNextRun(ctx context.Context, jobID uuid.UUID, next, ranAt time.Time) error {
	if err := p.JobScheduleRepository.UpdateNextRun(ctx, jobID, next, ranAt); err != nil {
		return err
	}
	p.stop()
	return nil
}

// assertStopped comprueba un trabajo desactivado con runs ejecuciones: inactivo, sin proxima
// ejecucion expuesta y sin otra en las pasadas de los dos periodos siguientes.
func (e *env) assertStopped(t *testing.T, jobID uuid.UUID, runs int) {
	t.Helper()
	for i := 0; i < 2; i++ {
		e.clock.advance(5 * time.Minute)
		e.uc.ProcessDueJobs(e.ctx)
	}
	if n := e.executionsOf(t, jobID); n != runs {
		t.Fatalf("ejecuciones %d, se esperaban %d", n, runs)
	}
	if n := len(e.outboxRows(t, "scheduler.job.started", "job_id", jobID)); n != runs {
		t.Fatalf("eventos de inicio %d, se esperaban %d", n, runs)
	}
	o, err := e.uc.GetJobOverview(e.ctx, jobID, e.tenant)
	if err != nil || o.Job.IsActive || o.NextRunAt != nil {
		t.Fatalf("desactivado: %+v (%v)", o, err)
	}
}

// Un despacho tiene reclamado el calendario de un interval vencido (la ejecucion creada y su
// evento encolado, sin confirmar) cuando llega la desactivacion: espera a que confirme y
// desactiva despues. La ejecucion reclamada sale; las siguientes no. Sin el calendario, la
// desactivacion confirmaba al momento y la pasada salia despues de ella.
func TestDesactivarEsperaAlDespachoEnCursoYLaEjecucionReclamadaSale(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createJob(t, 0)

	g := newGate(t)
	deps := e.deps
	deps.Schedules = pausedReplan{deps.Schedules, g}
	dispatcher := app.NewSchedulerUseCase(deps)
	e.clock.set(scopeStart.Add(5*time.Minute + 10*time.Second))
	dispatched := make(chan struct{})
	go func() { dispatcher.ProcessDueJobs(e.ctx); close(dispatched) }()
	within(t, g.reached, "el despacho no llego a replanificar")

	disabled := make(chan error, 1)
	go func() { disabled <- e.uc.DisableJob(e.ctx, job.ID, e.tenant) }()
	e.waitLockWaiters(t, 1)
	select {
	case err := <-disabled:
		t.Fatalf("la desactivacion no espero al despacho en curso: %v", err)
	default:
	}
	g.open()
	within(t, dispatched, "el despacho no termino")
	if err := within(t, disabled, "la desactivacion no termino"); err != nil {
		t.Fatalf("desactivar tras el despacho: %v", err)
	}
	e.assertStopped(t, job.ID, 1)
}

// La desactivacion llega antes que el despacho a un interval vencido y tiene el calendario y
// el trabajo sin confirmar: el despacho salta el calendario sin esperarla y, confirmada, ya no
// lo ve activo. La pasada vencida no sale.
func TestUnaDesactivacionEnCursoApartaAlDespacho(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createJob(t, 0)
	e.clock.set(scopeStart.Add(5*time.Minute + 10*time.Second))

	g := newGate(t)
	deps := e.deps
	deps.Jobs = pausedDeactivate{deps.Jobs, g}
	disabler := app.NewSchedulerUseCase(deps)
	disabled := make(chan error, 1)
	go func() { disabled <- disabler.DisableJob(e.ctx, job.ID, e.tenant) }()
	within(t, g.reached, "la desactivacion no llego a escribir")

	dispatched := make(chan struct{})
	go func() { e.uc.ProcessDueJobs(e.ctx); close(dispatched) }()
	within(t, dispatched, "el despacho espero a la desactivacion en vez de saltar el calendario")
	if n := e.executionsOf(t, job.ID); n != 0 {
		t.Fatalf("durante la desactivacion no se despacha: %d", n)
	}
	g.open()
	if err := within(t, disabled, "la desactivacion no termino"); err != nil {
		t.Fatal(err)
	}
	e.uc.ProcessDueJobs(e.ctx)
	e.assertStopped(t, job.ID, 0)
}

// El despacho de un one_time tiene el calendario y el trabajo (lo acaba de desactivar) cuando
// llega la desactivacion: con el mismo orden de bloqueos espera y termina sin interbloqueo, y
// el one_time corre una vez.
func TestDesactivarUnOneTimeEnPlenoDespachoNoSeInterbloquea(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createOneTime(t)

	g := newGate(t)
	deps := e.deps
	deps.Jobs = pausedDeactivate{deps.Jobs, g}
	dispatcher := app.NewSchedulerUseCase(deps)
	e.clock.set(scopeStart.Add(30 * time.Second))
	dispatched := make(chan struct{})
	go func() { dispatcher.ProcessDueJobs(e.ctx); close(dispatched) }()
	within(t, g.reached, "el despacho no llego a desactivar el one_time")

	disabled := make(chan error, 1)
	go func() { disabled <- e.uc.DisableJob(e.ctx, job.ID, e.tenant) }()
	e.waitLockWaiters(t, 1)
	g.open()
	within(t, dispatched, "el despacho no termino")
	if err := within(t, disabled, "la desactivacion no termino"); err != nil {
		t.Fatalf("desactivar un one_time en pleno despacho: %v", err)
	}
	e.assertStopped(t, job.ID, 1)
}
