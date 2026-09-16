//go:build integration

package postgres

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

func TestUnIntervaloSeDespachaEnSuRejillaContraLaBase(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createJob(t, 0)
	if next, _ := e.schedule(t, job.ID); !next.Equal(scopeStart.Add(5 * time.Minute)) {
		t.Fatalf("alta: %v, se esperaba 10:05", next)
	}

	// El ticker llega 29 s tarde: la siguiente sigue en la rejilla, no en la hora real.
	e.clock.set(scopeStart.Add(5*time.Minute + 29*time.Second))
	e.uc.ProcessDueJobs(e.ctx)
	next, last := e.schedule(t, job.ID)
	if !next.Equal(scopeStart.Add(10*time.Minute)) || last == nil || !last.Equal(e.clock.now()) {
		t.Fatalf("tras despachar tarde: next_run_at %v (se esperaba 10:10), last_run_at %v", next, last)
	}

	// Tres horas caido: una sola ejecucion y la primera de la rejilla tras ahora.
	e.clock.set(scopeStart.Add(3*time.Hour + 17*time.Minute + 13*time.Second))
	e.uc.ProcessDueJobs(e.ctx)
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, job.ID); n != 2 {
		t.Fatalf("ejecuciones %d, se esperaban 2", n)
	}
	if next, _ := e.schedule(t, job.ID); !next.Equal(scopeStart.Add(3*time.Hour + 20*time.Minute)) {
		t.Fatalf("tras la caida: %v, se esperaba 13:20", next)
	}

	// Unos minutos guardados sin validar no lanzan el trabajo en cada pasada: se desactiva.
	zero := 0
	broken := e.insertLegacyJob(t, domain.JobTypeInterval, nil, &zero, e.clock.now().Add(-time.Minute))
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, broken); n != 0 {
		t.Fatalf("un intervalo a cero se lanzo %d veces", n)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND NOT is_active`, broken); n != 1 {
		t.Fatal("un intervalo a cero se desactiva")
	}
}

func TestEditarElCalendarioContraLaBase(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	created := e.createJob(t, 0)
	e.clock.set(scopeStart.Add(5 * time.Minute))
	e.uc.ProcessDueJobs(e.ctx)
	_, ranAt := e.schedule(t, created.ID)
	if ranAt == nil {
		t.Fatal("el despacho anota last_run_at")
	}

	e.clock.set(scopeStart.Add(7 * time.Minute))
	job, err := e.uc.GetJob(e.ctx, created.ID, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	hourly := 60
	job.IntervalMinutes = &hourly
	o, err := e.uc.UpdateJob(e.ctx, job)
	want := scopeStart.Add(67 * time.Minute)
	next, last := e.schedule(t, job.ID)
	if err != nil || !next.Equal(want) || o.NextRunAt == nil || !o.NextRunAt.Equal(want) || last == nil || !last.Equal(*ranAt) {
		t.Fatalf("otros minutos: next_run_at %v (se esperaba %v), last_run_at %v (era %v), %v", next, want, last, ranAt, err)
	}

	e.clock.set(scopeStart.Add(9 * time.Minute))
	job.Name, job.Version = "Otro nombre", o.Job.Version
	if o, err = e.uc.UpdateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	if next, _ := e.schedule(t, job.ID); !next.Equal(want) {
		t.Fatalf("sin tocar el calendario se respeta la prevista: %v", next)
	}

	// Pasa a una sola vez: un calendario nuevo que sale en la pasada siguiente.
	job.JobType, job.IntervalMinutes, job.Version = domain.JobTypeOneTime, nil, o.Job.Version
	o, err = e.uc.UpdateJob(e.ctx, job)
	next, last = e.schedule(t, job.ID)
	if err != nil || !next.Equal(e.clock.now()) || last != nil || o.AlreadyRun {
		t.Fatalf("a una sola vez: next_run_at %v, last_run_at %v, already_run %v (%v)", next, last, o.AlreadyRun, err)
	}
	e.clock.set(scopeStart.Add(9*time.Minute + 30*time.Second))
	e.uc.ProcessDueJobs(e.ctx)
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, job.ID); n != 2 {
		t.Fatalf("una ejecucion del intervalo y una del one_time: %d", n)
	}
	o, err = e.uc.GetJobOverview(e.ctx, job.ID, e.tenant)
	if err != nil || o.Job.IsActive || !o.AlreadyRun || o.LastRunAt == nil || !o.LastRunAt.Equal(e.clock.now()) {
		t.Fatalf("tras su pasada: %+v (%v)", o, err)
	}
	if err := e.uc.EnableJob(e.ctx, job.ID, e.tenant); !errors.Is(err, domain.ErrOneTimeAlreadyRun) {
		t.Fatalf("already_run y el rechazo son la misma regla: %v", err)
	}
}

func TestEjecucionesActivasPaginadasContraLaBase(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	other := uuid.New()
	job := e.createJob(t, 0)
	oldest := e.insertExecution(t, job.ID, &e.tenant, domain.StatusRunning, scopeStart.Add(-3*time.Hour))
	tieA := e.insertExecution(t, job.ID, &e.tenant, domain.StatusPending, scopeStart.Add(-time.Hour))
	tieB := e.insertExecution(t, job.ID, &e.tenant, domain.StatusRunning, scopeStart.Add(-time.Hour))
	middle := e.insertExecution(t, job.ID, &e.tenant, domain.StatusRunning, scopeStart.Add(-2*time.Hour))
	e.insertExecution(t, job.ID, &e.tenant, domain.StatusCompleted, scopeStart)
	platform := e.insertExecution(t, e.insertJob(t, nil, "Plataforma"), nil, domain.StatusRunning, scopeStart.Add(-30*time.Minute))
	foreignJob := e.insertJob(t, &other, "Ajeno")
	e.insertExecution(t, foreignJob, &other, domain.StatusRunning, scopeStart)

	var got []uuid.UUID
	for page := 1; page <= 3; page++ {
		list, total, err := e.uc.GetRunningJobs(e.ctx, e.tenant, page, 2)
		if err != nil || total != 5 {
			t.Fatalf("pagina %d: %d filas de %d (%v)", page, len(list), total, err)
		}
		for _, x := range list {
			got = append(got, x.ID)
		}
	}
	// La mas reciente primero y, con el mismo created_at, el id mayor.
	first, second := tieA, tieB
	if tieB.String() > tieA.String() {
		first, second = tieB, tieA
	}
	want := []uuid.UUID{platform, first, second, middle, oldest}
	if len(got) != len(want) {
		t.Fatalf("tres paginas de dos cubren las cinco activas visibles: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("posicion %d: %s, se esperaba %s (orden %v)", i, got[i], want[i], got)
		}
	}
	if list, total, err := e.uc.GetRunningJobs(e.ctx, e.tenant, math.MaxInt, 100); err != nil || total != 5 || len(list) != 0 {
		t.Fatalf("una pagina enorme no es un 500: %d de %d (%v)", len(list), total, err)
	}
}
