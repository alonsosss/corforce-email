package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
)

func oneTime(j *domain.JobDefinition) { j.JobType, j.IntervalMinutes = domain.JobTypeOneTime, nil }

// assertSpent comprueba un one_time que el calendario ya despacho una vez: inactivo, sin otra
// ejecucion en las pasadas siguientes y sin reactivacion posible.
func (f *fixture) assertSpent(job domain.JobDefinition) {
	f.t.Helper()
	for i := 0; i < 2; i++ {
		f.clock.advance(time.Minute)
		f.uc.ProcessDueJobs(ctx)
	}
	if n := f.execsOf(job); n != 1 || f.store.jobs[job.ID].IsActive {
		f.t.Fatalf("un one_time despachado corre una vez y queda inactivo: %d ejecuciones, activo %v", n, f.store.jobs[job.ID].IsActive)
	}
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); !errors.Is(err, domain.ErrOneTimeAlreadyRun) {
		f.t.Fatalf("reactivarlo: %v", err)
	}
	if f.store.jobs[job.ID].IsActive {
		f.t.Fatal("el rechazo no lo reactiva")
	}
}

// El PUT lee el trabajo (GetJob) antes de que el calendario despache el one_time y lo escribe
// despues: la edicion se guarda y el trabajo sigue inactivo y despachado.
func TestUnaEdicionLeidaAntesDelDespachoNoReactivaElOneTime(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(oneTime)
	f.schedule(job, at(10, 0, 0))
	read, err := f.uc.GetJob(ctx, job.ID, f.tenantID)
	if err != nil || !read.IsActive {
		t.Fatalf("lectura del PUT: %+v (%v)", read, err)
	}
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 1 || f.store.jobs[job.ID].IsActive {
		t.Fatalf("el calendario lo despacha y lo desactiva: %d, activo %v", n, f.store.jobs[job.ID].IsActive)
	}

	read.Name = "Otro nombre"
	o, err := f.uc.UpdateJob(ctx, read)
	if err != nil || o.Job.Name != "Otro nombre" || o.Job.IsActive || !o.AlreadyRun || o.NextRunAt != nil {
		t.Fatalf("la edicion se guarda sin reactivarlo: %+v (%v)", o, err)
	}
	f.assertSpent(job)
}

// El despacho corre entre la lectura de UpdateJob y su escritura. Contra la base no ocurre
// (los dos bloquean el calendario), pero la escritura tampoco lo permitiria: la edicion no
// escribe is_active.
func TestUnDespachoEntreLaLecturaYLaEscrituraDeLaEdicionNoLaReactiva(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(oneTime)
	f.schedule(job, at(10, 0, 0))
	f.store.beforeJobUpdate = func() { f.uc.ProcessDueJobs(context.Background()) }

	edited := job
	edited.Name = "Otro nombre"
	o, err := f.uc.UpdateJob(ctx, &edited)
	if err != nil || o.Job.Name != "Otro nombre" || o.Job.IsActive || !o.AlreadyRun {
		t.Fatalf("la edicion se guarda sin reactivarlo: %+v (%v)", o, err)
	}
	f.assertSpent(job)
}

// Editar, desactivar y reactivar toman el calendario antes que el trabajo, en el orden del
// despacho (ClaimDue y despues Deactivate): con el orden inverso, una edicion que replanifica
// y un despacho se interbloquean, y una desactivacion no apartaria al despacho del calendario.
func TestEditarDesactivarYReactivarBloqueanElCalendarioAntesQueElTrabajo(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	f.schedule(job, at(10, 3, 0))
	edited := job
	edited.IntervalMinutes = new(int)
	*edited.IntervalMinutes = 10
	if _, err := f.uc.UpdateJob(ctx, &edited); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.DisableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if want := []string{"schedule", "job", "schedule", "job", "schedule", "job"}; !slices.Equal(f.store.locks, want) {
		t.Fatalf("bloqueos %v, se esperaba %v", f.store.locks, want)
	}
}

// Un trabajo de plataforma se rechaza antes de bloquear nada: una empresa no aparta al
// despacho de un trabajo que no puede cambiar.
func TestUnTrabajoDePlataformaSeRechazaSinBloquearlo(t *testing.T) {
	f := newFixture(t)
	platform := f.addJob(func(j *domain.JobDefinition) { j.TenantID = nil; j.Handler = platformHandler })
	f.schedule(platform, at(10, 0, 0))
	if err := f.uc.DisableJob(ctx, platform.ID, f.tenantID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Fatalf("desactivar: %v", err)
	}
	if err := f.uc.EnableJob(ctx, platform.ID, f.tenantID); !errors.Is(err, domain.ErrPlatformJob) {
		t.Fatalf("reactivar: %v", err)
	}
	if len(f.store.locks) != 0 {
		t.Fatalf("bloqueos: %v", f.store.locks)
	}
}

// Desactivado antes de la pasada en la que vence, el trabajo no se despacha en ella ni en
// las siguientes, y no expone proxima ejecucion.
func TestDesactivarAntesDeLaPasadaNoLaLanza(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	f.schedule(job, at(10, 0, 0))
	if err := f.uc.DisableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		f.uc.ProcessDueJobs(ctx)
		f.clock.advance(5 * time.Minute)
	}
	if n := f.execsOf(job); n != 0 || f.store.jobs[job.ID].IsActive {
		t.Fatalf("desactivado no se lanza: %d ejecuciones, activo %v", n, f.store.jobs[job.ID].IsActive)
	}
	if o, err := f.uc.GetJobOverview(ctx, job.ID, f.tenantID); err != nil || o.NextRunAt != nil {
		t.Fatalf("sin proxima ejecucion: %+v (%v)", o, err)
	}
}
