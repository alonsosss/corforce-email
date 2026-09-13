package app

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// El reloj del fixture marca 2026-09-13 10:00 UTC.

func (f *fixture) addTask(tenant uuid.UUID, status string, trigger time.Time) domain.ScheduledTask {
	task := domain.ScheduledTask{ID: uuid.New(), TenantID: tenant, Name: "Aviso", Handler: tenantHandler,
		TriggerAt: trigger, Status: status, CreatedAt: f.clock.now()}
	f.store.tasks[task.ID] = task
	return task
}

func TestUnaTareaDeOtraEmpresaEsComoUnaQueNoExiste(t *testing.T) {
	f := newFixture(t)
	foreign := f.addTask(uuid.New(), domain.TaskStatusScheduled, at(12, 0, 0))

	for name, id := range map[string]uuid.UUID{"de otra empresa": foreign.ID, "inexistente": uuid.New()} {
		if err := f.uc.CancelTask(ctx, id, f.tenantID); !errors.Is(err, domain.ErrTaskNotFound) {
			t.Errorf("cancelar una tarea %s: %v", name, err)
		}
		if _, err := f.uc.GetTask(ctx, id, f.tenantID); !errors.Is(err, domain.ErrTaskNotFound) {
			t.Errorf("leer una tarea %s: %v", name, err)
		}
	}
	if got := f.store.tasks[foreign.ID]; got.Status != domain.TaskStatusScheduled || f.store.taskWrites != 0 {
		t.Fatalf("la tarea ajena no cambia: %s, escrituras %d", got.Status, f.store.taskWrites)
	}
	list, total, err := f.uc.ListPendingTasks(ctx, f.tenantID, 1, 20)
	if err != nil || total != 0 || len(list) != 0 {
		t.Fatalf("el listado no ve la tarea ajena: %d de %d (%v)", len(list), total, err)
	}
}

func TestCancelarUnaTareaDeLaEmpresa(t *testing.T) {
	f := newFixture(t)
	own := f.addTask(f.tenantID, domain.TaskStatusScheduled, at(12, 0, 0))
	for i := 0; i < 2; i++ {
		if err := f.uc.CancelTask(ctx, own.ID, f.tenantID); err != nil {
			t.Fatalf("cancelar (vez %d): %v", i+1, err)
		}
	}
	if got := f.store.tasks[own.ID]; got.Status != domain.TaskStatusCancelled || f.store.taskWrites != 1 {
		t.Fatalf("cancelada una vez: %s, escrituras %d", got.Status, f.store.taskWrites)
	}

	done := f.addTask(f.tenantID, domain.TaskStatusExecuted, at(9, 0, 0))
	if err := f.uc.CancelTask(ctx, done.ID, f.tenantID); !errors.Is(err, domain.ErrTaskNotCancellable) {
		t.Fatalf("cancelar una ejecutada: %v", err)
	}
	if got := f.store.tasks[done.ID]; got.Status != domain.TaskStatusExecuted {
		t.Fatalf("la ejecutada sigue ejecutada: %s", got.Status)
	}

	// La pasada del ticker marca la vencida con su reloj y no reabre la cancelada.
	due := f.addTask(f.tenantID, domain.TaskStatusScheduled, at(11, 0, 0))
	f.clock.advance(3 * time.Hour)
	f.uc.ProcessPendingTasks(ctx)
	if got := f.store.tasks[due.ID]; got.Status != domain.TaskStatusExecuted || got.ExecutedAt == nil || !got.ExecutedAt.Equal(f.clock.now()) {
		t.Fatalf("vencida: %s a las %v", got.Status, got.ExecutedAt)
	}
	if got := f.store.tasks[own.ID]; got.Status != domain.TaskStatusCancelled {
		t.Fatalf("la cancelada sigue cancelada: %s", got.Status)
	}
}

func TestLasTareasPendientesSePaginan(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		f.addTask(f.tenantID, domain.TaskStatusScheduled, at(11, i, 0))
	}
	f.addTask(f.tenantID, domain.TaskStatusScheduled, f.clock.now().Add(domain.PendingTasksWindow+time.Minute))
	list, total, err := f.uc.ListPendingTasks(ctx, f.tenantID, 2, 2)
	if err != nil || total != 5 || len(list) != 2 || !list[0].TriggerAt.Equal(at(11, 2, 0)) {
		t.Fatalf("pagina 2 de 2: %d de %d (%v)", len(list), total, err)
	}
	if list, total, err := f.uc.ListPendingTasks(ctx, f.tenantID, math.MaxInt, 100); err != nil || total != 5 || len(list) != 0 {
		t.Fatalf("una pagina enorme no tiene filas: %d de %d (%v)", len(list), total, err)
	}
}

func TestElHistorialYLasActivasSonDeLaEmpresa(t *testing.T) {
	f := newFixture(t)
	other := uuid.New()
	own := f.addJob(nil)
	platform := f.addJob(func(j *domain.JobDefinition) { j.TenantID = nil; j.Handler = platformHandler })
	foreign := f.addJob(func(j *domain.JobDefinition) { j.TenantID = &other })
	addExec := func(job domain.JobDefinition, status string, minute int) domain.JobExecution {
		e := domain.JobExecution{ID: uuid.New(), JobID: job.ID, TenantID: job.TenantID, Status: status, CreatedAt: at(9, minute, 0)}
		f.store.execs[e.ID] = e
		return e
	}
	older, newer := addExec(own, domain.StatusCompleted, 1), addExec(own, domain.StatusRunning, 2)
	platformRun := addExec(platform, domain.StatusRunning, 3)
	addExec(foreign, domain.StatusRunning, 4)

	if _, _, err := f.uc.GetJobHistory(ctx, foreign.ID, f.tenantID, 1, 20); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("historial de un trabajo ajeno: %v", err)
	}
	if _, _, err := f.uc.GetJobHistory(ctx, uuid.New(), f.tenantID, 1, 20); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("historial de un trabajo inexistente: %v", err)
	}
	page, total, err := f.uc.GetJobHistory(ctx, own.ID, f.tenantID, 2, 1)
	if err != nil || total != 2 || len(page) != 1 || page[0].ID != older.ID {
		t.Fatalf("pagina 2 del historial: %v de %d (%v), se esperaba %s", page, total, err, older.ID)
	}
	if page, total, err := f.uc.GetJobHistory(ctx, own.ID, f.tenantID, math.MaxInt, 100); err != nil || total != 2 || len(page) != 0 {
		t.Fatalf("pagina enorme del historial: %d de %d (%v)", len(page), total, err)
	}
	if page, _, err := f.uc.GetJobHistory(ctx, platform.ID, f.tenantID, 1, 20); err != nil || len(page) != 1 {
		t.Fatalf("el historial de un trabajo de plataforma se lee: %d (%v)", len(page), err)
	}

	running, total, err := f.uc.GetRunningJobs(ctx, f.tenantID, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]bool{}
	for _, e := range running {
		seen[e.ID] = true
	}
	if len(running) != 2 || total != 2 || !seen[newer.ID] || !seen[platformRun.ID] {
		t.Fatalf("activas de la empresa y de plataforma, sin la ajena: %v (total %d)", running, total)
	}
	// La mas reciente primero, una por pagina; una pagina enorme no tiene filas.
	page, total, err = f.uc.GetRunningJobs(ctx, f.tenantID, 2, 1)
	if err != nil || total != 2 || len(page) != 1 || page[0].ID != newer.ID {
		t.Fatalf("pagina 2 de las activas: %v de %d (%v), se esperaba %s", page, total, err, newer.ID)
	}
	if page, total, err := f.uc.GetRunningJobs(ctx, f.tenantID, math.MaxInt, 100); err != nil || total != 2 || len(page) != 0 {
		t.Fatalf("pagina enorme de las activas: %d de %d (%v)", len(page), total, err)
	}
}

func TestUnTrabajoDeOtraEmpresaNoSeActivaNiSeDesactiva(t *testing.T) {
	f := newFixture(t)
	other := uuid.New()
	foreign := f.addJob(func(j *domain.JobDefinition) { j.TenantID = &other; j.IsActive = false })
	if err := f.uc.EnableJob(ctx, foreign.ID, f.tenantID); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("activar: %v", err)
	}
	if err := f.uc.DisableJob(ctx, foreign.ID, f.tenantID); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("desactivar: %v", err)
	}
	if f.store.jobUpdates+f.store.deactivated+f.store.planned != 0 || f.store.jobs[foreign.ID].IsActive {
		t.Fatalf("hubo efectos: updated=%d deactivated=%d planned=%d", f.store.jobUpdates, f.store.deactivated, f.store.planned)
	}
}

func TestReactivarUnIntervaloNoLanzaLoAtrasadoNiSaleDeSuRejilla(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(func(j *domain.JobDefinition) { j.IsActive = false })
	f.schedule(job, at(9, 12, 0))
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(job); !got.Equal(at(10, 2, 0)) {
		t.Fatalf("reactivado a las 10:00 con la rejilla de las 09:12 cada 5 min: %v, se esperaba 10:02", got)
	}
	o, err := f.uc.GetJobOverview(ctx, job.ID, f.tenantID)
	if err != nil || o.NextRunAt == nil || o.NextRunAt.Before(f.clock.now()) {
		t.Fatalf("next_run_at no queda en el pasado: %+v (%v)", o, err)
	}
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 0 {
		t.Fatalf("reactivar no lanza lo atrasado: %d", n)
	}
	f.clock.advance(2 * time.Minute)
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 1 {
		t.Fatalf("a las 10:02 corre: %d", n)
	}
}

func TestReactivarUnIntervaloSinMinutosSeRechaza(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(func(j *domain.JobDefinition) { j.IsActive = false; j.IntervalMinutes = nil })
	var ferr *domain.FieldError
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); !errors.As(err, &ferr) || ferr.Field != domain.FieldIntervalMinutes || ferr.Rule != domain.RuleRequired {
		t.Fatalf("sin interval_minutes: %v", err)
	}
	if f.store.jobs[job.ID].IsActive || f.store.planned != 0 {
		t.Fatal("sigue inactivo y sin planificar")
	}
}

func TestReactivarUnOneTimeSoloSiNuncaSeDespacho(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(func(j *domain.JobDefinition) {
		j.JobType, j.IntervalMinutes, j.IsActive = domain.JobTypeOneTime, nil, false
	})
	f.schedule(job, at(9, 0, 0))
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(job); !got.Equal(f.clock.now()) {
		t.Fatalf("nunca despachado: sale en la pasada siguiente, %v", got)
	}
	f.clock.advance(30 * time.Second)
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 1 || f.store.jobs[job.ID].IsActive {
		t.Fatalf("se lanza una vez y se desactiva: %d ejecuciones, activo %v", n, f.store.jobs[job.ID].IsActive)
	}

	updates, planned := f.store.jobUpdates, f.store.planned
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); !errors.Is(err, domain.ErrOneTimeAlreadyRun) {
		t.Fatalf("reactivar uno ya despachado: %v", err)
	}
	if f.store.jobs[job.ID].IsActive || f.store.jobUpdates != updates || f.store.planned != planned {
		t.Fatal("el rechazo no cambia nada")
	}
	f.clock.advance(time.Minute)
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 1 {
		t.Fatalf("no vuelve a lanzarse: %d", n)
	}
}
