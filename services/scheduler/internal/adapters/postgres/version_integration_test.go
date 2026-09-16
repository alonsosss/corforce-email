//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// La migracion 04 anade la version con su restriccion y las filas previas empiezan en 1.
func TestMigracionDeLaVersionDelTrabajo(t *testing.T) {
	e := setup(t)
	if n := e.count(t, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'scheduler'
		AND table_name = 'job_definitions' AND column_name = 'version' AND data_type = 'bigint'
		AND is_nullable = 'NO' AND column_default = '1'`); n != 1 {
		t.Fatalf("columna version: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM pg_constraint WHERE conname = 'job_definitions_version_check'`); n != 1 {
		t.Fatalf("restriccion de la version: %d", n)
	}
	legacy := e.insertJob(t, &e.tenant, "Previo")
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND version = 1`, legacy); n != 1 {
		t.Fatal("una fila previa empieza en la version 1")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE scheduler.job_definitions SET version = 0 WHERE id = $1`, legacy); err == nil {
		t.Fatal("una version menor que 1 no se guarda")
	}
}

// Update escribe solo sobre la version leida y la sube; con otra, ErrJobVersionConflict sin
// escribir, y el trabajo de otra empresa sigue siendo ErrJobNotFound. Desactivar, reactivar y
// el despacho no cambian la version.
func TestLaEscrituraDeLaDefinicionExigeLaVersion(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createJob(t, 0)
	if err := e.uc.DisableJob(e.ctx, job.ID, e.tenant); err != nil {
		t.Fatal(err)
	}
	if err := e.uc.EnableJob(e.ctx, job.ID, e.tenant); err != nil {
		t.Fatal(err)
	}
	e.clock.set(scopeStart.Add(5 * time.Minute))
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.executionsOf(t, job.ID); n != 1 {
		t.Fatalf("despachado: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND version = 1`, job.ID); n != 1 {
		t.Fatal("el estado y el calendario no cambian la version")
	}

	edited := *job
	edited.Name, edited.UpdatedAt = "Primera", scopeStart
	if err := e.deps.Jobs.Update(e.ctx, &edited); err != nil {
		t.Fatal(err)
	}
	stale := *job
	stale.Name = "Tarde"
	if err := e.deps.Jobs.Update(e.ctx, &stale); !errors.Is(err, domain.ErrJobVersionConflict) {
		t.Fatalf("con la version anterior: %v", err)
	}
	other := uuid.New()
	foreign := edited
	foreign.TenantID, foreign.Version = &other, 2
	if err := e.deps.Jobs.Update(e.ctx, &foreign); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("con otra empresa: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND name = 'Primera' AND version = 2`, job.ID); n != 1 {
		t.Fatal("solo la primera escritura se guardo, en la version 2")
	}
}

// Dos administradores leen la misma version de un cron. El primero cambia la zona; el
// segundo, que no la vio, cambia el nombre: 409 sin escribir, y la zona y el calendario del
// primero siguen. Releido, se aplica encima.
func TestDosEdicionesDeLaMismaLecturaNoSePisan(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	tenant, expr := e.tenant, "0 8 * * *"
	job := &domain.JobDefinition{TenantID: &tenant, Name: "Cron", Code: "it-" + uuid.NewString(), JobType: domain.JobTypeCron,
		CronExpression: &expr, Timezone: domain.DefaultTimezone, Handler: "it.report", TimeoutSeconds: 30}
	if _, err := e.uc.CreateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	first, err := e.uc.GetJob(e.ctx, job.ID, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.uc.GetJob(e.ctx, job.ID, e.tenant)
	if err != nil {
		t.Fatal(err)
	}

	first.Timezone = "America/Lima"
	o, err := e.uc.UpdateJob(e.ctx, first)
	lima := time.Date(2026, 9, 13, 13, 0, 0, 0, time.UTC)
	if err != nil || o.Job.Version != 2 || o.NextRunAt == nil || !o.NextRunAt.Equal(lima) {
		t.Fatalf("la primera edicion: %+v (%v)", o, err)
	}

	second.Name = "Pisado"
	if _, err := e.uc.UpdateJob(e.ctx, second); !errors.Is(err, domain.ErrJobVersionConflict) {
		t.Fatalf("la segunda, con la version anterior: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND timezone = 'America/Lima' AND name = 'Cron' AND version = 2`, job.ID); n != 1 {
		t.Fatal("la segunda no escribe")
	}
	if next, _ := e.schedule(t, job.ID); !next.Equal(lima) {
		t.Fatalf("ni replanifica: %v", next)
	}

	again, err := e.uc.GetJob(e.ctx, job.ID, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	again.Name = "Pisado"
	o, err = e.uc.UpdateJob(e.ctx, again)
	if err != nil || o.Job.Version != 3 || o.Job.Timezone != "America/Lima" || o.Job.Name != "Pisado" {
		t.Fatalf("releida se aplica encima: %+v (%v)", o, err)
	}
}

// La segunda edicion llega mientras la primera tiene el calendario y el trabajo bloqueados:
// espera, lee bajo el bloqueo la version que dejo la primera y responde 409 sin escribir.
func TestUnaEdicionConcurrenteEsperaYSeRechaza(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	job := e.createJob(t, 0)
	first, err := e.uc.GetJob(e.ctx, job.ID, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.uc.GetJob(e.ctx, job.ID, e.tenant)
	if err != nil {
		t.Fatal(err)
	}

	g := newGate(t)
	deps := e.deps
	deps.Jobs = pausedEdit{deps.Jobs, g}
	editor := app.NewSchedulerUseCase(deps)
	ten := 10
	first.IntervalMinutes = &ten
	edited := make(chan updateResult, 1)
	go func() {
		o, err := editor.UpdateJob(e.ctx, first)
		edited <- updateResult{o, err}
	}()
	within(t, g.reached, "la primera edicion no llego a escribir")

	rejected := make(chan error, 1)
	second.Name = "Pisado"
	go func() {
		_, err := e.uc.UpdateJob(e.ctx, second)
		rejected <- err
	}()
	e.waitLockWaiters(t, 1)
	g.open()
	if r := within(t, edited, "la primera edicion no termino"); r.err != nil || r.o.Job.Version != 2 {
		t.Fatalf("la primera: %+v (%v)", r.o, r.err)
	}
	if err := within(t, rejected, "la segunda edicion no termino"); !errors.Is(err, domain.ErrJobVersionConflict) {
		t.Fatalf("la segunda: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND interval_minutes = 10 AND name = 'Informe' AND version = 2`, job.ID); n != 1 {
		t.Fatal("queda la primera edicion y nada de la segunda")
	}
}
