package app

import (
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
)

func TestAltaYEdicionDevuelvenElTrabajoLeido(t *testing.T) {
	f := newFixture(t)
	tenant, daily := f.tenantID, "0 3 * * *"
	job := &domain.JobDefinition{TenantID: &tenant, Name: "n", Code: "diario", JobType: domain.JobTypeCron,
		CronExpression: &daily, Timezone: domain.DefaultTimezone, Handler: tenantHandler, TimeoutSeconds: 60}
	created, err := f.uc.CreateJob(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if created.Job.ID != job.ID || created.NextRunAt == nil || !created.NextRunAt.Equal(utcAt(2026, 9, 14, 3, 0, 0)) ||
		created.LastRunAt != nil || created.LastExecution != nil {
		t.Fatalf("alta: %+v", created)
	}

	hourly := "0 * * * *"
	edited := *job
	edited.CronExpression = &hourly
	updated, err := f.uc.UpdateJob(ctx, &edited)
	if err != nil {
		t.Fatal(err)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(at(11, 0, 0)) || updated.Job.CronExpr() != hourly {
		t.Fatalf("edicion: %+v", updated)
	}
}

func TestUltimaPasadaDelCalendarioYUltimaEjecucion(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	f.schedule(job, f.clock.now())
	f.clock.advance(10 * time.Second)
	dispatchedAt := f.clock.now()
	f.uc.ProcessDueJobs(ctx)

	o, err := f.uc.GetJobOverview(ctx, job.ID, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if o.LastRunAt == nil || !o.LastRunAt.Equal(dispatchedAt) || !o.NextRunAt.Equal(dispatchedAt.Add(5*time.Minute)) {
		t.Fatalf("tras la pasada: last_run_at %v, next_run_at %v", o.LastRunAt, o.NextRunAt)
	}
	if o.LastExecution == nil || o.LastExecution.Status != domain.StatusRunning || o.LastExecution.CompletedAt != nil {
		t.Fatalf("la ejecucion despachada: %+v", o.LastExecution)
	}

	// Un lanzamiento manual es la ultima ejecucion, pero no una pasada del calendario.
	f.clock.advance(time.Minute)
	manual := f.run(job)
	failedAt := f.clock.now()
	if _, err := f.uc.FailExecution(ctx, manual.ID, f.tenantID, "sin datos", false); err != nil {
		t.Fatal(err)
	}
	o, err = f.uc.GetJobOverview(ctx, job.ID, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}
	last := o.LastExecution
	if last == nil || last.ID != manual.ID || last.Status != domain.StatusFailed || last.FailureReason == nil ||
		*last.FailureReason != domain.FailureExecutor || last.CompletedAt == nil || !last.CompletedAt.Equal(failedAt) {
		t.Fatalf("ultima ejecucion: %+v", last)
	}
	if !o.LastRunAt.Equal(dispatchedAt) {
		t.Fatalf("el lanzamiento manual no cambia last_run_at: %v", o.LastRunAt)
	}

	if err := f.uc.DisableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if o, err = f.uc.GetJobOverview(ctx, job.ID, f.tenantID); err != nil || o.NextRunAt != nil {
		t.Fatalf("desactivado no tiene proxima ejecucion: %v (%v)", o.NextRunAt, err)
	}
}

func TestUnPlazoMayorQueElDelManejadorNoSeGuarda(t *testing.T) {
	f := newFixture(t)
	tenant, five := f.tenantID, 5
	job := &domain.JobDefinition{TenantID: &tenant, Name: "n", Code: "plazo", JobType: domain.JobTypeInterval,
		Timezone: domain.DefaultTimezone, IntervalMinutes: &five, Handler: tenantHandler, TimeoutSeconds: 601}
	var ferr *domain.FieldError
	if _, err := f.uc.CreateJob(ctx, job); !errors.As(err, &ferr) || ferr.Field != domain.FieldTimeoutSeconds {
		t.Fatalf("plazo mayor que el del manejador (600): %v", err)
	}
	if len(f.store.jobs) != 0 {
		t.Fatal("el trabajo rechazado no se guarda")
	}
	job.TimeoutSeconds = 600
	if _, err := f.uc.CreateJob(ctx, job); err != nil {
		t.Fatalf("plazo igual al del manejador: %v", err)
	}
}

func TestUnaTareaInvalidaNoLlegaAlRepositorio(t *testing.T) {
	uc := NewSchedulerUseCase(SchedulerDeps{Now: (&clock{t: at(10, 0, 0)}).now})
	task := &domain.ScheduledTask{Name: "Aviso", TriggerAt: at(12, 0, 0)}
	if err := uc.ScheduleTask(ctx, task); !errors.Is(err, domain.ErrInvalidTask) {
		t.Fatalf("tarea sin manejador: %v", err)
	}
}
