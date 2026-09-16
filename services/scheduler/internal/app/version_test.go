package app

import (
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
)

// Dos administradores leen el trabajo con la misma version. El primero cambia la zona; el
// segundo, que no la vio, guarda otro nombre: su edicion desharia la zona y se rechaza sin
// escribir ni replanificar nada. Releido, se aplica encima de la zona nueva.
func TestUnaEdicionConUnaVersionAnteriorNoPisaLaDeOtro(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(cronJob("0 3 * * *"))
	f.schedule(job, utcAt(2026, 9, 14, 3, 0, 0))
	first, err := f.uc.GetJob(ctx, job.ID, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.uc.GetJob(ctx, job.ID, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}

	first.Timezone = "America/Lima"
	o, err := f.uc.UpdateJob(ctx, first)
	if err != nil || o.Job.Version != domain.FirstJobVersion+1 {
		t.Fatalf("la primera edicion sube la version: %+v (%v)", o, err)
	}
	planned, next, writes := f.store.planned, f.nextRunOf(job), f.store.jobUpdates

	second.Name = "Otro nombre"
	if _, err := f.uc.UpdateJob(ctx, second); !errors.Is(err, domain.ErrJobVersionConflict) {
		t.Fatalf("la segunda, con la version anterior: %v", err)
	}
	stored := f.store.jobs[job.ID]
	if stored.Timezone != "America/Lima" || stored.Name != job.Name || stored.Version != 2 ||
		f.store.planned != planned || !f.nextRunOf(job).Equal(next) || f.store.jobUpdates != writes {
		t.Fatalf("el rechazo no escribe: %+v, planificaciones %d, proxima %v", stored, f.store.planned, f.nextRunOf(job))
	}

	again, err := f.uc.GetJob(ctx, job.ID, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}
	again.Name = "Otro nombre"
	o, err = f.uc.UpdateJob(ctx, again)
	if err != nil || o.Job.Version != 3 || o.Job.Timezone != "America/Lima" || o.Job.Name != "Otro nombre" {
		t.Fatalf("releida se aplica sobre la zona nueva: %+v (%v)", o, err)
	}
}

// Una version menor que la de un alta no es de ninguna lectura: 422 con el campo, sin
// bloquear ni escribir.
func TestUnaVersionImposibleEs422(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	for _, v := range []int64{0, -1} {
		edited := job
		edited.Name, edited.Version = "Otro nombre", v
		var ferr *domain.FieldError
		if _, err := f.uc.UpdateJob(ctx, &edited); !errors.As(err, &ferr) || ferr.Field != domain.FieldVersion || ferr.Rule != domain.RuleOutOfRange {
			t.Errorf("version %d: %v", v, err)
		}
	}
	if f.store.jobUpdates != 0 || len(f.store.locks) != 0 {
		t.Fatalf("hubo efectos: escrituras %d, bloqueos %v", f.store.jobUpdates, f.store.locks)
	}
}

// Activar, desactivar y el despacho cambian is_active, que el PUT no escribe: no invalidan la
// edicion de quien leyo antes, que se guarda sin reactivar el one_time ya despachado.
func TestElEstadoYElCalendarioNoCambianLaVersion(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(oneTime)
	f.schedule(job, at(10, 0, 0))
	read, err := f.uc.GetJob(ctx, job.ID, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.uc.DisableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 1 || f.versionOf(job.ID) != domain.FirstJobVersion {
		t.Fatalf("despachado una vez sin cambiar la version: %d ejecuciones, version %d", n, f.versionOf(job.ID))
	}

	read.Name = "Otro nombre"
	o, err := f.uc.UpdateJob(ctx, read)
	if err != nil || o.Job.Version != 2 || o.Job.IsActive || !o.AlreadyRun {
		t.Fatalf("la edicion leida antes se guarda y lo deja inactivo: %+v (%v)", o, err)
	}
}
