//go:build integration

package postgres

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// scopeStart es el reloj de estas pruebas: no dependen de la fecha real.
var scopeStart = time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)

func (e *env) insertTask(t *testing.T, tenant uuid.UUID, name string, trigger time.Time, status string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO scheduler.scheduled_tasks (id, tenant_id, name, trigger_at, handler, status) VALUES ($1, $2, $3, $4, 'it.report', $5)`,
		id, tenant, name, trigger, status); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) taskStatus(t *testing.T, id uuid.UUID) (string, *time.Time) {
	t.Helper()
	var status string
	var executedAt *time.Time
	if err := e.pool.QueryRow(context.Background(),
		`SELECT status, executed_at FROM scheduler.scheduled_tasks WHERE id = $1`, id).Scan(&status, &executedAt); err != nil {
		t.Fatal(err)
	}
	return status, executedAt
}

func (e *env) insertExecution(t *testing.T, jobID uuid.UUID, tenant *uuid.UUID, status string, createdAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO scheduler.job_executions (id, job_id, tenant_id, status, created_at) VALUES ($1, $2, $3, $4, $5)`,
		id, jobID, tenant, status, createdAt); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTareasPaginadasYAcotadasALaEmpresa(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	other := uuid.New()
	a := e.insertTask(t, e.tenant, "A", scopeStart.Add(time.Hour), domain.TaskStatusScheduled)
	b := e.insertTask(t, e.tenant, "B", scopeStart.Add(2*time.Hour), domain.TaskStatusScheduled)
	c := e.insertTask(t, e.tenant, "C", scopeStart.Add(2*time.Hour), domain.TaskStatusScheduled)
	e.insertTask(t, e.tenant, "Fuera de la ventana", scopeStart.Add(domain.PendingTasksWindow+time.Minute), domain.TaskStatusScheduled)
	done := e.insertTask(t, e.tenant, "Hecha", scopeStart.Add(-time.Hour), domain.TaskStatusExecuted)
	foreign := e.insertTask(t, other, "Ajena", scopeStart.Add(30*time.Minute), domain.TaskStatusScheduled)

	seen := map[uuid.UUID]int{}
	for page := 1; page <= 2; page++ {
		list, total, err := e.uc.ListPendingTasks(e.ctx, e.tenant, page, 2)
		if err != nil || total != 3 {
			t.Fatalf("pagina %d: %d filas de %d (%v)", page, len(list), total, err)
		}
		for _, task := range list {
			seen[task.ID]++
			if task.TenantID != e.tenant {
				t.Fatalf("pagina %d trae una tarea de otra empresa", page)
			}
		}
		if page == 1 && (len(list) != 2 || list[0].ID != a) {
			t.Fatalf("pagina 1 empieza por la mas proxima: %v", list)
		}
	}
	if len(seen) != 3 || seen[a] != 1 || seen[b] != 1 || seen[c] != 1 {
		t.Fatalf("las dos paginas cubren las tres sin repetir (desempate por id): %v", seen)
	}
	if list, total, err := e.uc.ListPendingTasks(e.ctx, e.tenant, math.MaxInt, 100); err != nil || total != 3 || len(list) != 0 {
		t.Fatalf("una pagina enorme no falla ni tiene filas: %d de %d (%v)", len(list), total, err)
	}

	if _, err := e.uc.GetTask(e.ctx, foreign, e.tenant); !errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("leer una tarea ajena: %v", err)
	}
	for name, id := range map[string]uuid.UUID{"ajena": foreign, "inexistente": uuid.New()} {
		if err := e.uc.CancelTask(e.ctx, id, e.tenant); !errors.Is(err, domain.ErrTaskNotFound) {
			t.Fatalf("cancelar una tarea %s: %v", name, err)
		}
	}
	if status, _ := e.taskStatus(t, foreign); status != domain.TaskStatusScheduled {
		t.Fatalf("la tarea ajena no cambia: %s", status)
	}
	if err := e.uc.CancelTask(e.ctx, done, e.tenant); !errors.Is(err, domain.ErrTaskNotCancellable) {
		t.Fatalf("cancelar una ejecutada: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := e.uc.CancelTask(e.ctx, a, e.tenant); err != nil {
			t.Fatalf("cancelar (vez %d): %v", i+1, err)
		}
	}
	if status, _ := e.taskStatus(t, a); status != domain.TaskStatusCancelled {
		t.Fatalf("cancelada: %s", status)
	}

	// Una cancelada entre la lectura y la escritura del barrido sigue cancelada.
	if marked, err := e.deps.Tasks.MarkExecuted(e.ctx, a, scopeStart); err != nil || marked {
		t.Fatalf("marcar una cancelada: %v (%v)", marked, err)
	}
	e.clock.set(scopeStart.Add(3 * time.Hour))
	e.uc.ProcessPendingTasks(e.ctx)
	for _, id := range []uuid.UUID{b, c} {
		if status, at := e.taskStatus(t, id); status != domain.TaskStatusExecuted || at == nil || !at.Equal(e.clock.now()) {
			t.Fatalf("vencida: %s a las %v, se esperaba el reloj del caso de uso", status, at)
		}
	}
	if status, _ := e.taskStatus(t, a); status != domain.TaskStatusCancelled {
		t.Fatalf("el barrido no reabre la cancelada: %s", status)
	}

	// Un fallo de la base no se confunde con una tarea que no esta.
	gone, cancel := context.WithCancel(e.ctx)
	cancel()
	if _, err := e.uc.GetTask(gone, b, e.tenant); err == nil || errors.Is(err, domain.ErrTaskNotFound) {
		t.Fatalf("con la conexion cancelada: %v, se esperaba un error que no sea no encontrada", err)
	}
}

func TestHistorialActivasYEscriturasSoloDeLaEmpresa(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	other := uuid.New()
	job := e.createJob(t, 0)
	first := e.insertExecution(t, job.ID, &e.tenant, domain.StatusCompleted, scopeStart.Add(-2*time.Hour))
	second := e.insertExecution(t, job.ID, &e.tenant, domain.StatusCompleted, scopeStart.Add(-2*time.Hour))
	running := e.insertExecution(t, job.ID, &e.tenant, domain.StatusRunning, scopeStart.Add(-time.Hour))
	// Una fila ajena colgada del trabajo propio no se cuenta ni se ve.
	e.insertExecution(t, job.ID, &other, domain.StatusRunning, scopeStart.Add(-time.Minute))
	foreignJob := e.insertJob(t, &other, "Ajeno")
	e.insertExecution(t, foreignJob, &other, domain.StatusRunning, scopeStart)
	platformJob := e.insertJob(t, nil, "Plataforma")
	platformRun := e.insertExecution(t, platformJob, nil, domain.StatusPending, scopeStart)

	if _, _, err := e.uc.GetJobHistory(e.ctx, foreignJob, e.tenant, 1, 20); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("historial de un trabajo ajeno: %v", err)
	}
	seen := map[uuid.UUID]bool{}
	for page := 1; page <= 3; page++ {
		list, total, err := e.uc.GetJobHistory(e.ctx, job.ID, e.tenant, page, 1)
		if err != nil || total != 3 || len(list) != 1 {
			t.Fatalf("pagina %d del historial: %d de %d (%v)", page, len(list), total, err)
		}
		seen[list[0].ID] = true
	}
	if !seen[first] || !seen[second] || !seen[running] {
		t.Fatalf("tres paginas de una cubren las tres sin repetir: %v", seen)
	}
	if list, total, err := e.uc.GetJobHistory(e.ctx, job.ID, e.tenant, math.MaxInt, 100); err != nil || total != 3 || len(list) != 0 {
		t.Fatalf("una pagina enorme no es un 500: %d de %d (%v)", len(list), total, err)
	}
	if list, _, err := e.uc.GetJobHistory(e.ctx, platformJob, e.tenant, 1, 20); err != nil || len(list) != 1 {
		t.Fatalf("el historial de plataforma se lee: %d (%v)", len(list), err)
	}

	active, err := e.uc.GetRunningJobs(e.ctx, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[uuid.UUID]bool{}
	for _, x := range active {
		ids[x.ID] = true
	}
	if len(active) != 2 || !ids[running] || !ids[platformRun] {
		t.Fatalf("activas de la empresa y de plataforma, sin las ajenas: %d", len(active))
	}

	if err := e.uc.DisableJob(e.ctx, foreignJob, e.tenant); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("desactivar un trabajo ajeno: %v", err)
	}
	if err := e.deps.Jobs.Deactivate(e.ctx, foreignJob, &e.tenant, scopeStart); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("desactivar por id con otra empresa: %v", err)
	}
	stolen := *job
	stolen.TenantID = &other
	if err := e.deps.Jobs.Update(e.ctx, &stolen); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("editar por id con otra empresa: %v", err)
	}
	exec, err := e.uc.GetExecution(e.ctx, running, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	exec.TenantID = &other
	if err := e.deps.Executions.Update(e.ctx, exec); !errors.Is(err, domain.ErrExecutionNotFound) {
		t.Fatalf("escribir una ejecucion con otra empresa: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = ANY($1) AND is_active AND name <> 'Informe_ajeno'`,
		[]uuid.UUID{foreignJob, job.ID}); n != 2 {
		t.Fatalf("los trabajos siguen activos y sin tocar: %d", n)
	}
}

func TestReactivarContraLaBase(t *testing.T) {
	e := setup(t)
	e.clock.set(scopeStart)
	interval := e.createJob(t, 0)
	if _, err := e.pool.Exec(context.Background(), `UPDATE scheduler.job_schedules SET next_run_at = $1 WHERE job_id = $2`,
		scopeStart.Add(-48*time.Minute), interval.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.uc.DisableJob(e.ctx, interval.ID, e.tenant); err != nil {
		t.Fatal(err)
	}
	if err := e.uc.EnableJob(e.ctx, interval.ID, e.tenant); err != nil {
		t.Fatal(err)
	}
	if next, _ := e.schedule(t, interval.ID); !next.Equal(scopeStart.Add(2 * time.Minute)) {
		t.Fatalf("intervalo de 5 min con la rejilla de las 09:12: %v, se esperaba 10:02", next)
	}
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, interval.ID); n != 0 {
		t.Fatalf("reactivar no lanza lo atrasado: %d", n)
	}

	tenant := e.tenant
	once := &domain.JobDefinition{TenantID: &tenant, Name: "Una vez", Code: "it-" + uuid.NewString(), JobType: domain.JobTypeOneTime,
		Timezone: domain.DefaultTimezone, Handler: "it.report", TimeoutSeconds: 30}
	if _, err := e.uc.CreateJob(e.ctx, once); err != nil {
		t.Fatal(err)
	}
	e.clock.set(scopeStart.Add(30 * time.Second))
	e.uc.ProcessDueJobs(e.ctx)
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND NOT is_active`, once.ID); n != 1 {
		t.Fatal("el one_time se desactiva tras despacharse")
	}
	if err := e.uc.EnableJob(e.ctx, once.ID, e.tenant); !errors.Is(err, domain.ErrOneTimeAlreadyRun) {
		t.Fatalf("reactivar un one_time ya despachado: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND NOT is_active`, once.ID); n != 1 {
		t.Fatal("sigue inactivo")
	}
}
