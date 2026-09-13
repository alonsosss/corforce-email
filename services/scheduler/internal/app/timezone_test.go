package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"go.uber.org/zap"
)

func utcAt(y int, m time.Month, d, h, min, s int) time.Time {
	return time.Date(y, m, d, h, min, s, 0, time.UTC)
}

func inZone(expr, timezone string) func(j *domain.JobDefinition) {
	return func(j *domain.JobDefinition) {
		cronJob(expr)(j)
		j.Timezone = timezone
	}
}

func TestUnCronSePlanificaEnLaZonaDelTrabajo(t *testing.T) {
	f := newFixture(t)
	tenant, expr := f.tenantID, "0 8 * * *"
	job := &domain.JobDefinition{TenantID: &tenant, Name: "n", Code: "lima", JobType: domain.JobTypeCron,
		CronExpression: &expr, Timezone: "America/Lima", Handler: tenantHandler, TimeoutSeconds: 60}
	if err := f.uc.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(*job); !got.Equal(at(13, 0, 0)) {
		t.Fatalf("las 08:00 de Lima son las 13:00 UTC: %v", got)
	}

	for _, tz := range []string{"", "+05:00", "Local", "America/Nowhere"} {
		bad := *job
		bad.Code, bad.Timezone = "malo-"+tz, tz
		if err := f.uc.CreateJob(ctx, &bad); !errors.Is(err, domain.ErrInvalidTimezone) {
			t.Errorf("crear con la zona %q: %v", tz, err)
		}
	}
	if len(f.store.jobs) != 1 || len(f.store.schedules) != 1 {
		t.Fatal("un trabajo con una zona invalida no se guarda ni se planifica")
	}
}

func TestCambiarLaZonaReplanifica(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(cronJob("0 3 * * *"))
	f.schedule(job, utcAt(2026, 9, 14, 3, 0, 0))

	edited := job
	edited.Timezone = "America/Lima"
	if err := f.uc.UpdateJob(ctx, &edited); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(job); !got.Equal(utcAt(2026, 9, 14, 8, 0, 0)) {
		t.Fatalf("las 03:00 pasan a ser las de Lima: %v", got)
	}

	edited.Timezone = "UTC+5"
	if err := f.uc.UpdateJob(ctx, &edited); !errors.Is(err, domain.ErrInvalidTimezone) {
		t.Fatalf("editar con una zona invalida: %v", err)
	}
	if stored := f.store.jobs[job.ID]; stored.Timezone != "America/Lima" {
		t.Fatalf("la edicion rechazada no se guarda: %q", stored.Timezone)
	}
}

// Las pasadas del ticker cubren el cambio de hora; en cada caso la ocurrencia afectada se
// lanza una sola vez y la siguiente queda en su hora de pared.
func TestCambioDeHoraSeLanzaUnaSolaVez(t *testing.T) {
	cases := []struct {
		name, expr string
		start      time.Time
		ticks      []time.Time
		next       time.Time
	}{
		{"hora saltada: 02:30 se lanza a las 03:00 EDT", "30 2 * * *", utcAt(2026, 3, 8, 5, 0, 0),
			[]time.Time{utcAt(2026, 3, 8, 6, 30, 10), utcAt(2026, 3, 8, 7, 0, 10), utcAt(2026, 3, 8, 7, 30, 10), utcAt(2026, 3, 8, 8, 0, 10)},
			utcAt(2026, 3, 9, 6, 30, 0)},
		{"hora repetida: 01:30 solo en la primera pasada", "30 1 * * *", utcAt(2026, 11, 1, 4, 0, 0),
			[]time.Time{utcAt(2026, 11, 1, 5, 30, 10), utcAt(2026, 11, 1, 6, 0, 10), utcAt(2026, 11, 1, 6, 30, 10), utcAt(2026, 11, 1, 7, 0, 10)},
			utcAt(2026, 11, 2, 6, 30, 0)},
	}
	for _, tc := range cases {
		f := newFixture(t)
		f.clock.t = tc.start
		tenant, expr := f.tenantID, tc.expr
		job := &domain.JobDefinition{TenantID: &tenant, Name: "n", Code: "nueva-york", JobType: domain.JobTypeCron,
			CronExpression: &expr, Timezone: "America/New_York", Handler: tenantHandler, TimeoutSeconds: 60}
		if err := f.uc.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		for _, tick := range tc.ticks {
			f.clock.t = tick
			f.uc.ProcessDueJobs(ctx)
		}
		if n := f.execsOf(*job); n != 1 {
			t.Errorf("%s: %d ejecuciones, se esperaba 1", tc.name, n)
		}
		if got := f.nextRunOf(*job); !got.Equal(tc.next) {
			t.Errorf("%s: siguiente %v, se esperaba %v", tc.name, got, tc.next)
		}
	}
}

func TestUnCronConZonaInvalidaGuardadaSeDesactiva(t *testing.T) {
	f := newFixture(t)
	due := f.addJob(inZone("0 3 * * *", "Mars/Olympus_Mons"))
	f.schedule(due, f.clock.now().Add(-time.Minute))
	f.clock.advance(time.Second)
	f.uc.ProcessDueJobs(ctx)
	stored := f.store.jobs[due.ID]
	if n := f.execsOf(due); n != 0 || stored.IsActive || len(f.store.events) != 0 {
		t.Fatalf("no se lanza a deshora: ejecuciones %d, activo %v, eventos %d", n, stored.IsActive, len(f.store.events))
	}
	if !stored.UpdatedAt.Equal(f.clock.now()) {
		t.Fatalf("la desactivacion lleva la hora del reloj del caso de uso: %v", stored.UpdatedAt)
	}

	pending := f.addJob(inZone("0 3 * * *", "Mars/Olympus_Mons"))
	f.schedule(pending, f.clock.now().Add(time.Hour))
	lima := f.addJob(inZone("0 8 * * *", "America/Lima"))
	f.schedule(lima, utcAt(2026, 9, 14, 8, 0, 0))
	fixed, err := f.uc.ReconcileCronSchedules(ctx)
	if err != nil || fixed != 2 {
		t.Fatalf("corregidos %d (%v), se esperaban 2", fixed, err)
	}
	if f.store.jobs[pending.ID].IsActive {
		t.Fatal("reconciliar desactiva un cron con una zona invalida")
	}
	if got := f.nextRunOf(lima); !got.Equal(at(13, 0, 0)) {
		t.Fatalf("reconciliar lleva el calendario a las 08:00 de Lima: %v", got)
	}
}

func TestDesactivarUsaElRelojDelCasoDeUso(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	f.clock.advance(90 * time.Second)
	if err := f.uc.DisableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if stored := f.store.jobs[job.ID]; stored.IsActive || !stored.UpdatedAt.Equal(f.clock.now()) {
		t.Fatalf("activo %v, updated_at %v (reloj %v)", stored.IsActive, stored.UpdatedAt, f.clock.now())
	}
}

type horizonTasks struct {
	ports.ScheduledTaskRepository
	before time.Time
}

func (r *horizonTasks) ListPending(_ context.Context, before time.Time) ([]*domain.ScheduledTask, error) {
	r.before = before
	return nil, nil
}

func TestTareasPendientesConElRelojDelCasoDeUso(t *testing.T) {
	clk := &clock{t: utcAt(2026, 9, 13, 10, 0, 0)}
	tasks := &horizonTasks{}
	uc := NewSchedulerUseCase(SchedulerDeps{Tasks: tasks, Now: clk.now, Logger: zap.NewNop()})
	if _, err := uc.ListPendingTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if want := utcAt(2026, 9, 14, 10, 0, 0); !tasks.before.Equal(want) {
		t.Fatalf("horizonte %v, se esperaba %v (24 h desde el reloj)", tasks.before, want)
	}
}
