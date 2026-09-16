//go:build integration

// Despacho real de las tareas puntuales y trabajo de plataforma sembrado, contra Postgres.
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// taskDispatchMigration es la que anade el motivo y siembra el trabajo de la poda.
const taskDispatchMigration = "migrations/tenant/canonical/scheduler/05_task_dispatch.sql"

func TestMigracionDelDespachoDeTareasYTrabajoSembrado(t *testing.T) {
	e := setup(t)
	// setup vacia las tablas: la siembra se vuelve a aplicar aqui, dos veces, que es lo que
	// prueba que tolera re-ejecutarse.
	applyMigration(t, e.pool, taskDispatchMigration)
	applyMigration(t, e.pool, taskDispatchMigration)

	if n := e.count(t, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'scheduler'
		AND table_name = 'scheduled_tasks' AND column_name = 'failure_reason'`); n != 1 {
		t.Fatalf("columna del motivo: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM pg_constraint WHERE conname = 'scheduled_tasks_failure_reason_check'`); n != 1 {
		t.Fatalf("restriccion del motivo: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE code = 'analytics-retention-prune'`); n != 1 {
		t.Fatalf("el trabajo sembrado existe una sola vez: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions
		WHERE code = 'analytics-retention-prune' AND tenant_id IS NULL AND is_active
		  AND job_type = 'cron' AND cron_expression = '@daily' AND timezone = 'UTC'
		  AND handler = 'analytics.retention.prune'`); n != 1 {
		t.Fatal("el trabajo sembrado es de plataforma, diario y apunta al manejador de la poda")
	}
	// Su primera pasada es la medianoche siguiente: sembrarlo no dispara una poda en el
	// propio despliegue.
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_schedules js
		JOIN scheduler.job_definitions jd ON jd.id = js.job_id
		WHERE jd.code = 'analytics-retention-prune' AND js.next_run_at > now()
		  AND js.last_run_at IS NULL`); n != 1 {
		t.Fatal("el calendario sembrado apunta al futuro y sin pasadas previas")
	}
	// Una empresa no puede cambiarlo: es de plataforma.
	if _, err := e.uc.GetJob(e.ctx, e.seededJobID(t), e.tenant); err != nil {
		t.Fatalf("la empresa lo ve en su base: %v", err)
	}
	if err := e.uc.DisableJob(e.ctx, e.seededJobID(t), e.tenant); err != domain.ErrPlatformJob {
		t.Fatalf("desactivarlo desde una empresa: %v, se esperaba ErrPlatformJob", err)
	}
}

func (e *env) seededJobID(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(context.Background(),
		`SELECT id FROM scheduler.job_definitions WHERE code = 'analytics-retention-prune'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// scheduleTask guarda una tarea de la empresa que vence hace un minuto.
func (e *env) scheduleTask(t *testing.T, handler string) *domain.ScheduledTask {
	t.Helper()
	task := &domain.ScheduledTask{
		TenantID: e.tenant, Name: "Poda puntual", TriggerAt: e.clock.now().Add(-time.Minute), Handler: handler,
	}
	if err := e.uc.ScheduleTask(e.ctx, task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestUnaTareaVencidaSeMarcaYSeDespachaPorLaOutbox(t *testing.T) {
	e := setup(t)
	applyMigration(t, e.pool, taskDispatchMigration)
	task := e.scheduleTask(t, "it.report")

	e.uc.ProcessPendingTasks(e.ctx)

	if n := e.count(t, `SELECT count(*) FROM scheduler.scheduled_tasks
		WHERE id = $1 AND status = 'executed' AND executed_at IS NOT NULL AND failure_reason IS NULL`, task.ID); n != 1 {
		t.Fatal("la tarea queda ejecutada")
	}
	rows := e.outboxRows(t, "scheduler.task.started", "task_id", task.ID)
	if len(rows) != 1 {
		t.Fatalf("un solo despacho en la outbox: %d", len(rows))
	}
	if got := rows[0].data["handler"]; got != "it.report" {
		t.Fatalf("el despacho lleva su manejador: %v", got)
	}
	if got := rows[0].data["tenant_id"]; got != e.tenant.String() {
		t.Fatalf("y su empresa: %v", got)
	}
	// Repetir el barrido no la vuelve a despachar: ya no esta programada.
	e.uc.ProcessPendingTasks(e.ctx)
	if rows := e.outboxRows(t, "scheduler.task.started", "task_id", task.ID); len(rows) != 1 {
		t.Fatalf("el barrido no duplica el despacho: %d", len(rows))
	}
}

// La condicion del SQL es lo que sostiene la carrera: una tarea que se cancelo entre la
// lectura del barrido y su escritura no se reabre.
func TestMarcarUnaTareaQueYaNoEstaProgramadaNoLaCambia(t *testing.T) {
	e := setup(t)
	applyMigration(t, e.pool, taskDispatchMigration)
	repo := e.deps.Tasks
	cancelled := e.scheduleTask(t, "it.report")
	if err := repo.Cancel(e.ctx, cancelled.ID, e.tenant); err != nil {
		t.Fatal(err)
	}

	marked, err := repo.MarkExecuted(e.ctx, cancelled.ID, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if marked {
		t.Fatal("una tarea cancelada no se marca ejecutada")
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.scheduled_tasks WHERE id = $1 AND status = 'cancelled'`, cancelled.ID); n != 1 {
		t.Fatal("sigue cancelada")
	}

	// Y una ya ejecutada no se cancela por un manejador retirado.
	done := e.scheduleTask(t, "it.report")
	if _, err := repo.MarkExecuted(e.ctx, done.ID, e.clock.now()); err != nil {
		t.Fatal(err)
	}
	cancelledNow, err := repo.CancelUndispatchable(e.ctx, done.ID, domain.FailureHandlerNotAllowed)
	if err != nil {
		t.Fatal(err)
	}
	if cancelledNow {
		t.Fatal("una tarea ejecutada no se cancela despues")
	}
}

func TestUnaTareaConManejadorRetiradoQuedaCanceladaConSuMotivo(t *testing.T) {
	e := setup(t)
	applyMigration(t, e.pool, taskDispatchMigration)
	// Se escribe a mano: ScheduleTask ya no admite un manejador fuera del catalogo, y lo que
	// se prueba es la fila que quedo guardada antes de que el manejador se retirara.
	id := uuid.New()
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO scheduler.scheduled_tasks (id, tenant_id, name, trigger_at, handler, status)
		 VALUES ($1, $2, 'Retirada', $3, 'se.retiro', 'scheduled')`,
		id, e.tenant, e.clock.now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	e.uc.ProcessPendingTasks(e.ctx)

	if n := e.count(t, `SELECT count(*) FROM scheduler.scheduled_tasks
		WHERE id = $1 AND status = 'cancelled' AND failure_reason = 'handler_not_allowed' AND executed_at IS NULL`, id); n != 1 {
		t.Fatal("queda cancelada con su motivo y sin marcarse ejecutada")
	}
	if rows := e.outboxRows(t, "scheduler.task.started", "task_id", id); len(rows) != 0 {
		t.Fatalf("no se despacha a nadie: %d", len(rows))
	}
}
