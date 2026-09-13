//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schedulerQueries cuenta las consultas al esquema scheduler que pasan por un pool.
type schedulerQueries struct{ n atomic.Int64 }

func (c *schedulerQueries) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "scheduler.") {
		c.n.Add(1)
	}
	return ctx
}

func (c *schedulerQueries) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (e *env) createNamed(t *testing.T, name string) *domain.JobDefinition {
	t.Helper()
	tenant, five := e.tenant, 5
	job := &domain.JobDefinition{TenantID: &tenant, Name: name, Code: "it-" + uuid.NewString(), JobType: domain.JobTypeInterval,
		Timezone: domain.DefaultTimezone, IntervalMinutes: &five, Handler: "it.report", TimeoutSeconds: 30}
	if _, err := e.uc.CreateJob(e.ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

// insertJob guarda un trabajo sin calendario, de la empresa dada o de plataforma (nil).
func (e *env) insertJob(t *testing.T, tenant *uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO scheduler.job_definitions (id, tenant_id, name, code, job_type, interval_minutes, handler)
		 VALUES ($1, $2, $3, $4, 'interval', 5, 'it.report')`, id, tenant, name, "it-"+id.String()); err != nil {
		t.Fatal(err)
	}
	return id
}

func namesOf(list []*domain.JobOverview) string {
	names := make([]string, 0, len(list))
	for _, o := range list {
		names = append(names, o.Job.Name)
	}
	return strings.Join(names, ",")
}

func TestListadoPaginadoConCalendarioYUltimaEjecucion(t *testing.T) {
	e := setup(t)
	start := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	e.clock.set(start)
	alfa, beta, gamma := e.createNamed(t, "Alfa"), e.createNamed(t, "Beta"), e.createNamed(t, "Gamma")
	delta := e.insertJob(t, nil, "Delta")
	other := uuid.New()
	foreign := e.insertJob(t, &other, "Aaa de otra empresa")

	// Alfa: el calendario lo despacha a las 10:00:30.
	if _, err := e.pool.Exec(context.Background(), `UPDATE scheduler.job_schedules SET next_run_at = $1 WHERE job_id = $2`, start, alfa.ID); err != nil {
		t.Fatal(err)
	}
	dispatchedAt := start.Add(30 * time.Second)
	e.clock.set(dispatchedAt)
	e.uc.ProcessDueJobs(e.ctx)

	// Beta: dos lanzamientos manuales; el segundo falla y es la ultima ejecucion.
	e.clock.set(start.Add(time.Minute))
	e.run(t, beta)
	e.clock.set(start.Add(2 * time.Minute))
	second := e.run(t, beta)
	failedAt := start.Add(2*time.Minute + 10*time.Second)
	e.clock.set(failedAt)
	if _, err := e.uc.FailExecution(e.ctx, second.ID, e.tenant, "sin datos", false); err != nil {
		t.Fatal(err)
	}

	if err := e.uc.DisableJob(e.ctx, gamma.ID, e.tenant); err != nil {
		t.Fatal(err)
	}

	page := func(p, perPage int, active *bool) ([]*domain.JobOverview, int64) {
		t.Helper()
		list, total, err := e.uc.ListJobs(e.ctx, domain.JobFilter{TenantID: e.tenant, IsActive: active, Page: p, PerPage: perPage})
		if err != nil {
			t.Fatal(err)
		}
		return list, total
	}
	first, total := page(1, 2, nil)
	if total != 4 || namesOf(first) != "Alfa,Beta" {
		t.Fatalf("pagina 1: %s de %d (la de otra empresa no se ve; la de plataforma si)", namesOf(first), total)
	}
	secondPage, total := page(2, 2, nil)
	if total != 4 || namesOf(secondPage) != "Delta,Gamma" {
		t.Fatalf("pagina 2: %s de %d", namesOf(secondPage), total)
	}
	for _, p := range []int{3, math.MaxInt} {
		if list, total := page(p, 2, nil); len(list) != 0 || total != 4 {
			t.Fatalf("pagina %d: %s de %d", p, namesOf(list), total)
		}
	}

	byName := map[string]*domain.JobOverview{}
	for _, o := range append(first, secondPage...) {
		byName[o.Job.Name] = o
	}
	a := byName["Alfa"]
	if a.LastRunAt == nil || !a.LastRunAt.Equal(dispatchedAt) || a.NextRunAt == nil || !a.NextRunAt.Equal(dispatchedAt.Add(5*time.Minute)) {
		t.Fatalf("Alfa: last_run_at %v, next_run_at %v", a.LastRunAt, a.NextRunAt)
	}
	if a.LastExecution == nil || a.LastExecution.Status != domain.StatusRunning || a.LastExecution.CompletedAt != nil {
		t.Fatalf("Alfa: ultima ejecucion %+v", a.LastExecution)
	}
	b := byName["Beta"]
	if b.LastRunAt != nil || b.NextRunAt == nil || !b.NextRunAt.Equal(start.Add(5*time.Minute)) {
		t.Fatalf("Beta: last_run_at %v (los lanzamientos manuales no la cambian), next_run_at %v", b.LastRunAt, b.NextRunAt)
	}
	if last := b.LastExecution; last == nil || last.ID != second.ID || last.Status != domain.StatusFailed ||
		last.FailureReason == nil || *last.FailureReason != domain.FailureExecutor || last.CompletedAt == nil || !last.CompletedAt.Equal(failedAt) {
		t.Fatalf("Beta: ultima ejecucion %+v", last)
	}
	if d := byName["Delta"]; d.Job.ID != delta || d.Job.TenantID != nil || d.NextRunAt != nil || d.LastRunAt != nil || d.LastExecution != nil {
		t.Fatalf("Delta (plataforma, sin calendario): %+v", d)
	}
	if g := byName["Gamma"]; g.Job.IsActive || g.NextRunAt != nil || g.LastExecution != nil {
		t.Fatalf("Gamma (inactivo): %+v", g)
	}

	yes, no := true, false
	if list, total := page(1, 10, &yes); total != 3 || namesOf(list) != "Alfa,Beta,Delta" {
		t.Fatalf("activos: %s de %d", namesOf(list), total)
	}
	if list, total := page(1, 10, &no); total != 1 || namesOf(list) != "Gamma" {
		t.Fatalf("inactivos: %s de %d", namesOf(list), total)
	}

	got, err := e.uc.GetJobOverview(e.ctx, beta.ID, e.tenant)
	if err != nil || got.LastExecution == nil || got.LastExecution.ID != second.ID {
		t.Fatalf("lectura de uno: %+v (%v)", got, err)
	}
	if _, err := e.uc.GetJobOverview(e.ctx, foreign, e.tenant); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("el trabajo de otra empresa: %v", err)
	}
}

func TestElListadoNoLeeCadaTrabajoPorSeparado(t *testing.T) {
	e := setup(t)
	e.clock.set(time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC))
	const jobs = 30
	for i := 0; i < jobs; i++ {
		e.run(t, e.createNamed(t, fmt.Sprintf("Trabajo %02d", i)))
	}

	counter := &schedulerQueries{}
	cfg, err := pgxpool.ParseConfig(integrationEnv(t, "SCHEDULER_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := db.WithTenant(context.Background(), pool, e.tenant.String())

	for _, perPage := range []int{1, 25, 100} {
		counter.n.Store(0)
		list, total, err := e.uc.ListJobs(ctx, domain.JobFilter{TenantID: e.tenant, Page: 1, PerPage: perPage})
		if err != nil || total != jobs || len(list) != min(perPage, jobs) {
			t.Fatalf("pagina de %d: %d filas de %d (%v)", perPage, len(list), total, err)
		}
		for _, o := range list {
			if o.LastExecution == nil || o.NextRunAt == nil {
				t.Fatalf("%s sin su calendario o su ultima ejecucion", o.Job.Name)
			}
		}
		if n := counter.n.Load(); n != 2 {
			t.Fatalf("pagina de %d: %d consultas, se esperaban 2 (recuento y pagina)", perPage, n)
		}
	}
}

func TestLosTopesDelDominioSonLosAnchosDeLasColumnas(t *testing.T) {
	e := setup(t)
	for col, want := range map[string]int{
		"job_definitions.name":            domain.MaxNameLength,
		"job_definitions.code":            domain.MaxCodeLength,
		"job_definitions.cron_expression": domain.MaxCronExpressionLength,
		"job_definitions.handler":         domain.MaxHandlerNameLength,
		"job_definitions.timezone":        domain.MaxTimezoneLength,
		"scheduled_tasks.name":            domain.MaxNameLength,
		"scheduled_tasks.handler":         domain.MaxHandlerNameLength,
	} {
		table, column, _ := strings.Cut(col, ".")
		var width int
		if err := e.pool.QueryRow(context.Background(),
			`SELECT character_maximum_length FROM information_schema.columns
			  WHERE table_schema = 'scheduler' AND table_name = $1 AND column_name = $2`, table, column).Scan(&width); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if width != want {
			t.Errorf("%s admite %d caracteres y el dominio %d", col, width, want)
		}
	}

	// varchar cuenta caracteres: un nombre en el tope con caracteres de dos bytes se guarda.
	job := e.createNamed(t, strings.Repeat("ñ", domain.MaxNameLength))
	if n := e.count(t, `SELECT char_length(name) FROM scheduler.job_definitions WHERE id = $1`, job.ID); n != domain.MaxNameLength {
		t.Fatalf("nombre guardado de %d caracteres", n)
	}
}
