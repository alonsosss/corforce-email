package app

import (
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// El reloj del fixture marca 2026-09-13 10:00 UTC (domingo).

func cronJob(expr string) func(j *domain.JobDefinition) {
	return func(j *domain.JobDefinition) {
		j.JobType = domain.JobTypeCron
		j.IntervalMinutes = nil
		j.CronExpression = &expr
	}
}

func (f *fixture) schedule(job domain.JobDefinition, next time.Time) {
	f.store.schedules[job.ID] = domain.JobSchedule{JobID: job.ID, NextRunAt: next}
}

func (f *fixture) nextRunOf(job domain.JobDefinition) time.Time {
	f.t.Helper()
	s, ok := f.store.schedules[job.ID]
	if !ok {
		f.t.Fatalf("el trabajo %s no tiene calendario", job.ID)
	}
	return s.NextRunAt
}

func (f *fixture) execsOf(job domain.JobDefinition) int {
	n := 0
	for _, e := range f.store.execs {
		if e.JobID == job.ID {
			n++
		}
	}
	return n
}

func at(h, m, s int) time.Time { return time.Date(2026, 9, 13, h, m, s, 0, time.UTC) }

func TestCrearUnCronLoPlanificaEnSuOcurrencia(t *testing.T) {
	f := newFixture(t)
	tenant := f.tenantID
	newJob := func(code, expr string) *domain.JobDefinition {
		return &domain.JobDefinition{TenantID: &tenant, Name: "n", Code: code, JobType: domain.JobTypeCron,
			CronExpression: &expr, Timezone: domain.DefaultTimezone, Handler: tenantHandler, TimeoutSeconds: 60}
	}
	daily := newJob("diario", "0 3 * * *")
	if _, err := f.uc.CreateJob(ctx, daily); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(*daily); !got.Equal(time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("un cron diario a las 3 creado a las 10 corre manana a las 3: %v", got)
	}

	for _, expr := range []string{"", "0 25 * * *", "@every 30s"} {
		if _, err := f.uc.CreateJob(ctx, newJob("malo-"+expr, expr)); !errors.Is(err, domain.ErrInvalidCron) {
			t.Errorf("crear con %q: %v", expr, err)
		}
	}
	if len(f.store.jobs) != 1 || len(f.store.schedules) != 1 {
		t.Fatal("un cron invalido no se guarda ni se planifica")
	}

	five := 5
	interval := &domain.JobDefinition{TenantID: &tenant, Name: "n", Code: "intervalo", JobType: domain.JobTypeInterval,
		Timezone: domain.DefaultTimezone, IntervalMinutes: &five, Handler: tenantHandler, TimeoutSeconds: 60}
	if _, err := f.uc.CreateJob(ctx, interval); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(*interval); !got.Equal(f.clock.now().Add(5 * time.Minute)) {
		t.Fatalf("un trabajo de intervalo sigue usando interval_minutes: %v", got)
	}
}

func TestTrasDespacharSeCuentaDesdeLaHoraPrevista(t *testing.T) {
	for _, expr := range []string{"*/5 * * * *", "@every 5m"} {
		f := newFixture(t)
		job := f.addJob(cronJob(expr))
		f.schedule(job, at(10, 0, 0))
		// El ticker llega 29 s tarde: la siguiente no se desplaza esos 29 s.
		f.clock.advance(29 * time.Second)
		f.uc.ProcessDueJobs(ctx)
		if n := f.execsOf(job); n != 1 {
			t.Fatalf("%s: ejecuciones %d", expr, n)
		}
		if got := f.nextRunOf(job); !got.Equal(at(10, 5, 0)) {
			t.Errorf("%s: siguiente %v, se esperaba 10:05:00", expr, got)
		}
	}
}

func TestOcurrenciasSaltadasSeLanzanUnaSolaVez(t *testing.T) {
	cases := []struct {
		expr      string
		scheduled time.Time
		want      time.Time
	}{
		{"0 * * * *", at(3, 0, 0), at(11, 0, 0)},
		{"@every 10m", at(3, 0, 0), at(10, 20, 0)},
	}
	for _, tc := range cases {
		f := newFixture(t)
		job := f.addJob(cronJob(tc.expr))
		f.schedule(job, tc.scheduled)
		f.clock.advance(17 * time.Minute)
		for i := 0; i < 3; i++ {
			f.uc.ProcessDueJobs(ctx)
		}
		if n := f.execsOf(job); n != 1 {
			t.Fatalf("%s: siete horas caido lanzan %d ejecuciones, se esperaba 1", tc.expr, n)
		}
		if got := f.nextRunOf(job); !got.Equal(tc.want) {
			t.Errorf("%s: siguiente %v, se esperaba %v", tc.expr, got, tc.want)
		}
	}
}

func TestEditarElCalendarioDeUnCronLoReplanifica(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(cronJob("0 3 * * *"))
	due := at(9, 59, 0)
	f.schedule(job, due)

	edited := job
	edited.Name = "Otro nombre"
	if _, err := f.uc.UpdateJob(ctx, &edited); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(job); !got.Equal(due) || f.store.planned != 0 {
		t.Fatalf("una edicion que no toca el calendario respeta la ejecucion prevista: %v", got)
	}

	expr := "30 12 * * *"
	edited.CronExpression, edited.Version = &expr, f.versionOf(job.ID)
	if _, err := f.uc.UpdateJob(ctx, &edited); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(job); !got.Equal(at(12, 30, 0)) {
		t.Fatalf("cambiar la expresion replanifica: %v", got)
	}

	interval := f.addJob(nil)
	f.schedule(interval, at(10, 3, 0))
	toCron := interval
	cronJob("0 * * * *")(&toCron)
	if _, err := f.uc.UpdateJob(ctx, &toCron); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(interval); !got.Equal(at(11, 0, 0)) {
		t.Fatalf("pasar a cron replanifica: %v", got)
	}

	bad := "0 3 * *"
	broken := f.store.jobs[job.ID]
	broken.CronExpression = &bad
	if _, err := f.uc.UpdateJob(ctx, &broken); !errors.Is(err, domain.ErrInvalidCron) {
		t.Fatalf("editar con una expresion invalida: %v", err)
	}
	if stored := f.store.jobs[job.ID]; stored.CronExpr() != expr {
		got := stored.CronExpr()
		t.Fatalf("la edicion rechazada no se guarda: %q", got)
	}
}

func TestReactivarUnCronNoLanzaLoAtrasado(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(func(j *domain.JobDefinition) { cronJob("0 3 * * *")(j); j.IsActive = false })
	f.schedule(job, time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC))
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(job); !got.Equal(time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("reactivado a las 10 corre manana a las 3: %v", got)
	}
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 0 {
		t.Fatalf("reactivar no lanza lo atrasado: %d", n)
	}

	legacy := f.addJob(func(j *domain.JobDefinition) { cronJob("cada hora")(j); j.IsActive = false })
	if err := f.uc.EnableJob(ctx, legacy.ID, f.tenantID); !errors.Is(err, domain.ErrInvalidCron) {
		t.Fatalf("reactivar un cron con una expresion invalida: %v", err)
	}
	if f.store.jobs[legacy.ID].IsActive {
		t.Fatal("sigue desactivado hasta que se corrija la expresion")
	}
}

func TestUnCronGuardadoConExpresionInvalidaSeDesactiva(t *testing.T) {
	f := newFixture(t)
	legacy := f.addJob(cronJob("todos los dias"))
	f.schedule(legacy, f.clock.now().Add(-time.Minute))
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(legacy); n != 0 || f.store.jobs[legacy.ID].IsActive || len(f.store.events) != 0 {
		t.Fatalf("no se lanza a deshora: ejecuciones %d, activo %v, eventos %d",
			n, f.store.jobs[legacy.ID].IsActive, len(f.store.events))
	}
}

func TestReconciliarLosCalendariosCron(t *testing.T) {
	f := newFixture(t)
	// Calendarios escritos por la version que no evaluaba la expresion: la ultima pasada mas
	// una hora, a cualquier minuto.
	offGrid := f.addJob(cronJob("0 3 * * *"))
	f.schedule(offGrid, at(10, 37, 12))
	missed := f.addJob(cronJob("0 3 * * *"))
	f.schedule(missed, at(2, 37, 12))
	correct := f.addJob(cronJob("*/15 * * * *"))
	f.schedule(correct, at(10, 15, 0))
	broken := f.addJob(cronJob("0 3 * *"))
	f.schedule(broken, at(10, 37, 12))
	interval := f.addJob(nil)
	f.schedule(interval, at(10, 37, 12))
	inactive := f.addJob(func(j *domain.JobDefinition) { cronJob("0 3 * * *")(j); j.IsActive = false })
	f.schedule(inactive, at(10, 37, 12))

	fixed, err := f.uc.ReconcileCronSchedules(ctx)
	if err != nil || fixed != 3 {
		t.Fatalf("corregidos %d (%v), se esperaban 3", fixed, err)
	}
	want := map[uuid.UUID]time.Time{
		offGrid.ID:  time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC),
		missed.ID:   at(3, 0, 0),
		correct.ID:  at(10, 15, 0),
		interval.ID: at(10, 37, 12),
		inactive.ID: at(10, 37, 12),
	}
	for id, w := range want {
		if got := f.store.schedules[id].NextRunAt; !got.Equal(w) {
			j := f.store.jobs[id]
			t.Errorf("trabajo %q: %v, se esperaba %v", j.CronExpr(), got, w)
		}
	}
	if f.store.jobs[broken.ID].IsActive {
		t.Fatal("un cron con una expresion invalida se desactiva")
	}
	if fixed, err := f.uc.ReconcileCronSchedules(ctx); err != nil || fixed != 0 {
		t.Fatalf("reconciliar dos veces no cambia nada: %d (%v)", fixed, err)
	}

	// La ocurrencia de las 3 que se salto el proceso se lanza una vez y sigue la de manana.
	f.uc.ProcessDueJobs(ctx)
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(missed); n != 1 {
		t.Fatalf("la ocurrencia saltada se lanza una vez: %d", n)
	}
	if got := f.nextRunOf(missed); !got.Equal(time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("siguiente tras la saltada: %v", got)
	}
	if n := f.execsOf(offGrid) + f.execsOf(broken); n != 0 {
		t.Fatalf("nada se lanza a deshora: %d", n)
	}
}
