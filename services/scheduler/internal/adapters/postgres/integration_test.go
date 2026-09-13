//go:build integration

// Pruebas contra un Postgres real. Se ejecutan con:
//
//	SCHEDULER_TEST_DSN=postgres://... go test -tags integration ./services/scheduler/...
//
// Aplican la outbox de plataforma y las migraciones del scheduler DOS veces, que es lo que
// garantiza que toleran re-ejecutarse, y vacian las tablas: la base debe ser desechable.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/pkg/events"
	outboxadapter "github.com/alonsosss/corforce-email/services/scheduler/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

var migrations = []string{
	"migrations/tenant/canonical/platform/00_outbox.sql",
	"migrations/tenant/canonical/scheduler/01_scheduler.sql",
	"migrations/tenant/canonical/scheduler/02_execution_lifecycle.sql",
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	clock  *testClock
	tenant uuid.UUID
	deps   app.SchedulerDeps
	uc     *app.SchedulerUseCase
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
}

func applyMigration(t *testing.T, pool *pgxpool.Pool, rel string) {
	t.Helper()
	sql, err := os.ReadFile(filepath.Join(repoRoot(), rel))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("aplicar %s: %v", rel, err)
	}
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("SCHEDULER_TEST_DSN")
	if dsn == "" {
		t.Skip("SCHEDULER_TEST_DSN no definida")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, rel := range append(append([]string{}, migrations...), migrations...) {
		applyMigration(t, pool, rel)
	}
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE scheduler.job_executions, scheduler.job_schedules, scheduler.job_definitions, platform.event_outbox`); err != nil {
		t.Fatal(err)
	}

	catalog, err := domain.NewHandlerCatalog([]domain.HandlerSpec{{
		Name: "it.report", Service: "it", MaxTimeoutSeconds: 60, Scopes: []domain.HandlerScope{domain.ScopeTenant},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctxPool := &db.ContextPool{}
	clock := &testClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	deps := app.SchedulerDeps{
		Jobs: NewJobDefinitionRepo(ctxPool), Executions: NewJobExecutionRepo(ctxPool),
		Tasks: NewScheduledTaskRepo(ctxPool), Schedules: NewJobScheduleRepo(ctxPool),
		Events: outboxadapter.NewPublisher(ctxPool), Tx: NewTransactor(ctxPool), Catalog: catalog,
		Retry: domain.RetryPolicy{BaseDelay: time.Second, MaxDelay: time.Minute}, Now: clock.now, Logger: zap.NewNop(),
	}
	tenant := uuid.New()
	return &env{
		ctx: db.WithTenant(context.Background(), pool, tenant.String()), pool: pool, clock: clock,
		tenant: tenant, deps: deps, uc: app.NewSchedulerUseCase(deps),
	}
}

func (e *env) createJob(t *testing.T, maxRetries int) *domain.JobDefinition {
	t.Helper()
	tenant, five := e.tenant, 5
	job := &domain.JobDefinition{
		TenantID: &tenant, Name: "Informe", Code: "it-" + uuid.NewString(), JobType: domain.JobTypeInterval,
		IntervalMinutes: &five, Handler: "it.report", MaxRetries: maxRetries, TimeoutSeconds: 30,
	}
	if err := e.uc.CreateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

func (e *env) run(t *testing.T, job *domain.JobDefinition) *domain.JobExecution {
	t.Helper()
	exec, err := e.uc.RunJob(e.ctx, e.tenant, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

type outboxRow struct {
	tenantID *string
	event    events.Event
	data     map[string]any
}

func (e *env) outboxRows(t *testing.T, subject, key string, id uuid.UUID) []outboxRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT tenant_id::text, payload FROM platform.event_outbox WHERE subject = $1 AND payload->'data'->>$2 = $3`,
		subject, key, id.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		var payload []byte
		if err := rows.Scan(&r.tenantID, &payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &r.event); err != nil {
			t.Fatal(err)
		}
		r.data, _ = r.event.Data.(map[string]any)
		out = append(out, r)
	}
	return out
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigracionAditivaYRellenoDeLasActivasPrevias(t *testing.T) {
	e := setup(t)
	if n := e.count(t, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'scheduler'
		AND table_name = 'job_executions' AND column_name IN ('deadline_at','next_attempt_at','retry_of','failure_reason')`); n != 4 {
		t.Fatalf("columnas nuevas: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM pg_constraint WHERE conname = 'job_executions_failure_reason_check'`); n != 1 {
		t.Fatalf("restriccion del motivo: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'scheduler' AND indexname IN
		('uq_job_executions_retry_of','idx_job_executions_deadline','idx_job_executions_next_attempt')`); n != 3 {
		t.Fatalf("indices: %d", n)
	}

	// Una ejecucion activa de antes de la migracion (sin plazo) recibe el de su trabajo.
	job := e.createJob(t, 0)
	legacy := uuid.New()
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO scheduler.job_executions (id, job_id, tenant_id, status, started_at)
		VALUES ($1, $2, $3, 'running', now() - interval '1 hour')`, legacy, job.ID, e.tenant); err != nil {
		t.Fatal(err)
	}
	applyMigration(t, e.pool, migrations[2])
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE id = $1 AND deadline_at = started_at + interval '30 seconds'`, legacy); n != 1 {
		t.Fatal("la ejecucion previa no recibio su plazo")
	}
	e.clock.advance(time.Hour)
	if got := e.uc.ExpireOverdue(e.ctx); got != 1 {
		t.Fatalf("el barrido cierra la ejecucion previa vencida: %d", got)
	}
}

// failingAfter encola el evento de verdad y despues falla: si la ejecucion y el evento no
// compartieran transaccion, el evento quedaria en la outbox sin ejecucion.
type failingAfter struct{ ports.EventPublisher }

func (f failingAfter) JobStarted(ctx context.Context, job *domain.JobDefinition, exec *domain.JobExecution, timeout int) error {
	if err := f.EventPublisher.JobStarted(ctx, job, exec, timeout); err != nil {
		return err
	}
	return errors.New("fallo despues de encolar")
}

func TestEjecucionYEventoEnLaMismaTransaccion(t *testing.T) {
	e := setup(t)
	job := e.createJob(t, 0)
	exec := e.run(t, job)

	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE id = $1 AND status = 'running'
		AND deadline_at = started_at + interval '30 seconds'`, exec.ID); n != 1 {
		t.Fatal("la ejecucion despachada no esta en la base con su plazo")
	}
	rows := e.outboxRows(t, "scheduler.job.started", "execution_id", exec.ID)
	if len(rows) != 1 {
		t.Fatalf("eventos de inicio en la outbox: %d", len(rows))
	}
	r := rows[0]
	if r.tenantID == nil || *r.tenantID != e.tenant.String() || r.event.TenantID != e.tenant.String() || r.event.Source != "scheduler-service" {
		t.Fatalf("sobre: %+v (tenant de la fila %v)", r.event, r.tenantID)
	}
	if r.data["handler"] != "it.report" || r.data["timeout_seconds"] != float64(30) || r.data["attempt"] != float64(1) ||
		r.data["tenant_id"] != e.tenant.String() || r.data["job_id"] != job.ID.String() {
		t.Fatalf("data: %v", r.data)
	}

	deps := e.deps
	deps.Events = failingAfter{e.deps.Events}
	if _, err := app.NewSchedulerUseCase(deps).RunJob(e.ctx, e.tenant, job.ID); err == nil {
		t.Fatal("se esperaba el fallo del publicador")
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, job.ID); n != 1 {
		t.Fatalf("la ejecucion del intento fallido se deshizo: hay %d", n)
	}
	if n := len(e.outboxRows(t, "scheduler.job.started", "job_id", job.ID)); n != 1 {
		t.Fatalf("el evento del intento fallido se deshizo con ella: hay %d", n)
	}
}

func TestCierreIdempotente(t *testing.T) {
	e := setup(t)
	exec := e.run(t, e.createJob(t, 0))
	result := `{"rows": 3}`
	for i := 0; i < 2; i++ {
		got, err := e.uc.CompleteExecution(e.ctx, exec.ID, e.tenant, &result)
		if err != nil || got.Status != domain.StatusCompleted {
			t.Fatalf("completar (vez %d): %v %+v", i+1, err, got)
		}
	}
	if n := len(e.outboxRows(t, "scheduler.job.completed", "execution_id", exec.ID)); n != 1 {
		t.Fatalf("eventos de exito: %d", n)
	}
	if _, err := e.uc.FailExecution(e.ctx, exec.ID, e.tenant, "tarde", true); !errors.Is(err, domain.ErrExecutionConflict) {
		t.Fatalf("fallar una completada: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE id = $1 AND status = 'completed' AND result->>'rows' = '3'`, exec.ID); n != 1 {
		t.Fatal("la ejecucion sigue completada con su resultado")
	}
}

func concurrently(n int, fn func() int) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := fn()
			mu.Lock()
			total += got
			mu.Unlock()
		}()
	}
	wg.Wait()
	return total
}

func TestDosBarridosConcurrentesNoMarcanDosVeces(t *testing.T) {
	e := setup(t)
	exec := e.run(t, e.createJob(t, 3))
	e.clock.advance(31 * time.Second)

	if got := concurrently(8, func() int { return e.uc.ExpireOverdue(e.ctx) }); got != 1 {
		t.Fatalf("ocho barridos a la vez cerraron %d ejecuciones, se esperaba 1", got)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE id = $1 AND status = 'failed' AND failure_reason = 'timeout'`, exec.ID); n != 1 {
		t.Fatal("la ejecucion vencida no quedo fallida por timeout")
	}
	if n := len(e.outboxRows(t, "scheduler.job.failed", "execution_id", exec.ID)); n != 1 {
		t.Fatalf("eventos de fallo: %d", n)
	}
	var retry uuid.UUID
	if err := e.pool.QueryRow(context.Background(),
		`SELECT id FROM scheduler.job_executions WHERE retry_of = $1 AND status = 'pending'`, exec.ID).Scan(&retry); err != nil {
		t.Fatalf("un solo reintento en espera: %v", err)
	}

	e.clock.advance(time.Second)
	if got := concurrently(8, func() int { return e.uc.DispatchRetries(e.ctx) }); got != 1 {
		t.Fatalf("ocho barridos despacharon %d reintentos, se esperaba 1", got)
	}
	rows := e.outboxRows(t, "scheduler.job.started", "execution_id", retry)
	if len(rows) != 1 || rows[0].data["attempt"] != float64(2) {
		t.Fatalf("inicio del reintento: %+v", rows)
	}
}

func TestUnSoloReintentoManualConcurrente(t *testing.T) {
	e := setup(t)
	exec := e.run(t, e.createJob(t, 3))
	if _, err := e.uc.FailExecution(e.ctx, exec.ID, e.tenant, "definitivo", false); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var oks, dup int
	concurrently(4, func() int {
		_, err := e.uc.RetryFailedExecution(e.ctx, exec.ID, e.tenant)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			oks++
		case errors.Is(err, domain.ErrAlreadyRetried):
			dup++
		default:
			t.Errorf("reintento manual: %v", err)
		}
		return 0
	})
	if oks != 1 || dup != 3 {
		t.Fatalf("reintentos aceptados %d, rechazados %d", oks, dup)
	}
}

func TestTrabajoVencidoSeLanzaUnaSolaVez(t *testing.T) {
	e := setup(t)
	job := e.createJob(t, 0)
	if _, err := e.pool.Exec(context.Background(), `UPDATE scheduler.job_schedules SET next_run_at = $1 WHERE job_id = $2`,
		e.clock.now().Add(-time.Second), job.ID); err != nil {
		t.Fatal(err)
	}
	concurrently(4, func() int { e.uc.ProcessDueJobs(e.ctx); return 0 })
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, job.ID); n != 1 {
		t.Fatalf("cuatro pasadas a la vez lanzaron %d ejecuciones, se esperaba 1", n)
	}
	if n := len(e.outboxRows(t, "scheduler.job.started", "job_id", job.ID)); n != 1 {
		t.Fatalf("eventos de inicio: %d", n)
	}
}

func (e *env) schedule(t *testing.T, jobID uuid.UUID) (time.Time, *time.Time) {
	t.Helper()
	var next time.Time
	var last *time.Time
	if err := e.pool.QueryRow(context.Background(),
		`SELECT next_run_at, last_run_at FROM scheduler.job_schedules WHERE job_id = $1`, jobID).Scan(&next, &last); err != nil {
		t.Fatal(err)
	}
	return next, last
}

func mustNextRun(t *testing.T, expr string, after time.Time) time.Time {
	t.Helper()
	next, err := domain.NextRun(expr, after)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestCronSeDespachaEnSuOcurrenciaSinDeriva(t *testing.T) {
	e := setup(t)
	tenant, expr := e.tenant, "*/5 * * * *"
	job := &domain.JobDefinition{TenantID: &tenant, Name: "Cron", Code: "it-" + uuid.NewString(), JobType: domain.JobTypeCron,
		CronExpression: &expr, Handler: "it.report", TimeoutSeconds: 30}
	if err := e.uc.CreateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	first := mustNextRun(t, expr, e.clock.now())
	if next, last := e.schedule(t, job.ID); !next.Equal(first) || last != nil {
		t.Fatalf("alta: next_run_at %v (se esperaba %v), last_run_at %v", next, first, last)
	}

	// El ticker llega 29 s despues de la hora prevista.
	e.clock.advance(first.Sub(e.clock.now()) + 29*time.Second)
	e.uc.ProcessDueJobs(e.ctx)
	next, last := e.schedule(t, job.ID)
	if !next.Equal(first.Add(5*time.Minute)) || last == nil {
		t.Fatalf("tras despachar: next_run_at %v (se esperaba %v), last_run_at %v", next, first.Add(5*time.Minute), last)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_executions WHERE job_id = $1`, job.ID); n != 1 {
		t.Fatalf("ejecuciones: %d", n)
	}

	// Cambiar la expresion replanifica sin anotar una ejecucion que no hubo.
	ranAt := *last
	daily := "0 3 * * *"
	job.CronExpression = &daily
	if err := e.uc.UpdateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	next, last = e.schedule(t, job.ID)
	if want := mustNextRun(t, daily, e.clock.now()); !next.Equal(want) || last == nil || !last.Equal(ranAt) {
		t.Fatalf("editar: next_run_at %v (se esperaba %v), last_run_at %v (era %v)", next, want, last, ranAt)
	}

	bad := "0 3 * *"
	job.CronExpression = &bad
	if err := e.uc.UpdateJob(e.ctx, job); !errors.Is(err, domain.ErrInvalidCron) {
		t.Fatalf("editar con una expresion invalida: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND cron_expression = $2`, job.ID, daily); n != 1 {
		t.Fatal("la edicion rechazada no se guarda")
	}
}

// insertLegacyJob guarda un trabajo y su calendario como los dejaba la version que no
// evaluaba la expresion.
func (e *env) insertLegacyJob(t *testing.T, jobType string, expr *string, minutes *int, next time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO scheduler.job_definitions (id, tenant_id, name, code, job_type, cron_expression, interval_minutes, handler)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, e.tenant, "Heredado", "it-"+id.String(), jobType, expr, minutes, "it.report"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO scheduler.job_schedules (job_id, next_run_at) VALUES ($1, $2)`, id, next); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestReconciliarLosCalendariosCronHeredados(t *testing.T) {
	e := setup(t)
	now := e.clock.now()
	daily, quarter, broken, five := "0 3 * * *", "*/15 * * * *", "cada hora", 5
	offGrid := now.Add(37*time.Minute + 12*time.Second + 345*time.Microsecond)
	legacy := e.insertLegacyJob(t, domain.JobTypeCron, &daily, nil, offGrid)
	correct := e.insertLegacyJob(t, domain.JobTypeCron, &quarter, nil, mustNextRun(t, quarter, now))
	invalid := e.insertLegacyJob(t, domain.JobTypeCron, &broken, nil, offGrid)
	interval := e.insertLegacyJob(t, domain.JobTypeInterval, nil, &five, offGrid)

	fixed, err := e.uc.ReconcileCronSchedules(e.ctx)
	if err != nil || fixed != 2 {
		t.Fatalf("corregidos %d (%v), se esperaban 2", fixed, err)
	}
	for id, want := range map[uuid.UUID]time.Time{
		legacy: mustNextRun(t, daily, now), correct: mustNextRun(t, quarter, now), interval: offGrid,
	} {
		if next, last := e.schedule(t, id); !next.Equal(want) || last != nil {
			t.Errorf("trabajo %s: next_run_at %v (se esperaba %v), last_run_at %v", id, next, want, last)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM scheduler.job_definitions WHERE id = $1 AND NOT is_active`, invalid); n != 1 {
		t.Fatal("un cron con una expresion invalida se desactiva")
	}
	if fixed, err := e.uc.ReconcileCronSchedules(e.ctx); err != nil || fixed != 0 {
		t.Fatalf("reconciliar dos veces no cambia nada: %d (%v)", fixed, err)
	}

	// Un calendario bloqueado por otra transaccion (un despacho en curso) se salta sin esperar.
	if _, err := e.pool.Exec(context.Background(), `UPDATE scheduler.job_schedules SET next_run_at = $1 WHERE job_id = $2`, offGrid, legacy); err != nil {
		t.Fatal(err)
	}
	tx, err := e.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT 1 FROM scheduler.job_schedules WHERE job_id = $1 FOR UPDATE`, legacy); err != nil {
		t.Fatal(err)
	}
	lockedCtx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	fixed, err = e.uc.ReconcileCronSchedules(lockedCtx)
	if rerr := tx.Rollback(context.Background()); rerr != nil {
		t.Fatal(rerr)
	}
	if err != nil || fixed != 0 {
		t.Fatalf("con el calendario bloqueado: %d (%v)", fixed, err)
	}
	if fixed, err := e.uc.ReconcileCronSchedules(e.ctx); err != nil || fixed != 1 {
		t.Fatalf("liberado el bloqueo se corrige: %d (%v)", fixed, err)
	}
}
