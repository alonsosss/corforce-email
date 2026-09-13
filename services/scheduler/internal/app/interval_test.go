package app

import (
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
)

// El reloj del fixture marca 2026-09-13 10:00 UTC y addJob guarda un intervalo de 5 minutos.

func everyMinutes(n int) func(j *domain.JobDefinition) {
	return func(j *domain.JobDefinition) { j.IntervalMinutes = &n }
}

func TestUnIntervaloDespachadoConRetrasoNoDeriva(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	f.schedule(job, at(10, 0, 0))
	for i, late := range []time.Duration{29 * time.Second, 47 * time.Second, 3*time.Minute + 5*time.Second} {
		f.clock.t = f.nextRunOf(job).Add(late)
		f.uc.ProcessDueJobs(ctx)
		if got, want := f.nextRunOf(job), at(10, 5*(i+1), 0); !got.Equal(want) {
			t.Fatalf("pasada %d con %v de retraso: siguiente %v, se esperaba %v", i+1, late, got, want)
		}
		if last := f.store.schedules[job.ID].LastRunAt; last == nil || !last.Equal(f.clock.now()) {
			t.Fatalf("pasada %d: last_run_at %v, se esperaba la hora real del despacho", i+1, last)
		}
	}
	if n := f.execsOf(job); n != 3 {
		t.Fatalf("ejecuciones %d, se esperaban 3", n)
	}
}

func TestUnIntervaloTrasUnaCaidaLargaSeLanzaUnaSolaVez(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(everyMinutes(7))
	f.schedule(job, at(3, 0, 0))
	f.clock.advance(17 * time.Minute)
	for i := 0; i < 3; i++ {
		f.uc.ProcessDueJobs(ctx)
	}
	if n := f.execsOf(job); n != 1 {
		t.Fatalf("siete horas caido lanzan %d ejecuciones, se esperaba 1", n)
	}
	// 03:00 mas 63 periodos de 7 min son las 10:21, la primera de su rejilla tras las 10:17.
	if got := f.nextRunOf(job); !got.Equal(at(10, 21, 0)) {
		t.Fatalf("siguiente %v, se esperaba 10:21", got)
	}
}

// En Madrid el reloj se atrasa el 2026-10-25 a las 01:00 UTC: un intervalo cuenta tiempo
// transcurrido y su zona no lo mueve.
func TestUnIntervaloNoDependeDeLaZonaNiDeLosCambiosDeHora(t *testing.T) {
	scheduled := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	for _, tz := range []string{domain.DefaultTimezone, "Europe/Madrid"} {
		f := newFixture(t)
		f.clock.t = time.Date(2026, 10, 25, 1, 10, 0, 0, time.UTC)
		job := f.addJob(func(j *domain.JobDefinition) { everyMinutes(60)(j); j.Timezone = tz })
		f.schedule(job, scheduled)
		f.uc.ProcessDueJobs(ctx)
		if got := f.nextRunOf(job); !got.Equal(scheduled.Add(time.Hour)) || f.execsOf(job) != 1 {
			t.Fatalf("%s: siguiente %v con %d ejecuciones, se esperaba %v", tz, got, f.execsOf(job), scheduled.Add(time.Hour))
		}
	}
}

func TestUnaDefinicionGuardadaQueNoSePlanificaSeDesactivaSinLanzarse(t *testing.T) {
	zero, negative := 0, -5
	for name, mut := range map[string]func(j *domain.JobDefinition){
		"intervalo sin minutos": func(j *domain.JobDefinition) { j.IntervalMinutes = nil },
		"intervalo a cero":      func(j *domain.JobDefinition) { j.IntervalMinutes = &zero },
		"intervalo negativo":    func(j *domain.JobDefinition) { j.IntervalMinutes = &negative },
		"tipo desconocido":      func(j *domain.JobDefinition) { j.JobType = "weekly" },
	} {
		f := newFixture(t)
		job := f.addJob(mut)
		f.schedule(job, at(9, 59, 0))
		for i := 0; i < 3; i++ {
			f.uc.ProcessDueJobs(ctx)
			f.clock.advance(30 * time.Second)
		}
		if n := f.execsOf(job); n != 0 || f.store.jobs[job.ID].IsActive || len(f.store.events) != 0 {
			t.Fatalf("%s: ejecuciones %d, activo %v, eventos %d", name, n, f.store.jobs[job.ID].IsActive, len(f.store.events))
		}
	}
}

func TestEditarLosMinutosDeUnIntervaloActivoLoReplanificaDesdeAhora(t *testing.T) {
	cases := []struct {
		name     string
		from, to int
		stored   time.Time
		want     time.Time
	}{
		{"alargarlo con la proxima cerca", 5, 60, at(10, 3, 0), at(11, 0, 0)},
		{"acortarlo con la proxima lejos", 60, 5, at(10, 50, 0), at(10, 5, 0)},
		{"vencida sin despachar: ni rafaga ni hora pasada", 5, 15, at(9, 58, 0), at(10, 15, 0)},
	}
	for _, tc := range cases {
		f := newFixture(t)
		job := f.addJob(everyMinutes(tc.from))
		ran := at(9, 0, 0)
		f.store.schedules[job.ID] = domain.JobSchedule{JobID: job.ID, NextRunAt: tc.stored, LastRunAt: &ran}
		edited := job
		edited.IntervalMinutes = &tc.to
		o, err := f.uc.UpdateJob(ctx, &edited)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := f.nextRunOf(job); !got.Equal(tc.want) || o.NextRunAt == nil || !o.NextRunAt.Equal(tc.want) {
			t.Fatalf("%s: siguiente %v (leida %v), se esperaba %v", tc.name, got, o.NextRunAt, tc.want)
		}
		if last := f.store.schedules[job.ID].LastRunAt; last == nil || !last.Equal(ran) {
			t.Fatalf("%s: la edicion no anota ni olvida una pasada: %v", tc.name, last)
		}
		f.uc.ProcessDueJobs(ctx)
		if n := f.execsOf(job); n != 0 {
			t.Fatalf("%s: editar lanzo %d ejecuciones", tc.name, n)
		}
		f.clock.t = tc.want
		f.uc.ProcessDueJobs(ctx)
		if n, next := f.execsOf(job), f.nextRunOf(job); n != 1 || !next.Equal(tc.want.Add(time.Duration(tc.to)*time.Minute)) {
			t.Fatalf("%s: en su hora corre una vez (%d) y sigue su periodo nuevo (%v)", tc.name, n, next)
		}
	}
}

func TestUnaEdicionQueNoTocaElCalendarioLoRespeta(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(nil)
	f.schedule(job, at(10, 3, 0))
	five := 5
	edited := job
	edited.Name, edited.IntervalMinutes, edited.Timezone, edited.MaxRetries = "Otro nombre", &five, "America/Lima", 4
	if _, err := f.uc.UpdateJob(ctx, &edited); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(job); !got.Equal(at(10, 3, 0)) || f.store.planned != 0 {
		t.Fatalf("respeta la ejecucion prevista: %v, planificaciones %d", got, f.store.planned)
	}
}

func TestCambiarElTipoEmpiezaUnCalendarioNuevo(t *testing.T) {
	f := newFixture(t)
	ran := at(9, 55, 0)
	job := f.addJob(nil)
	f.store.schedules[job.ID] = domain.JobSchedule{JobID: job.ID, NextRunAt: at(10, 3, 0), LastRunAt: &ran}
	once := job
	once.JobType, once.IntervalMinutes = domain.JobTypeOneTime, nil
	o, err := f.uc.UpdateJob(ctx, &once)
	if err != nil || o.AlreadyRun || o.LastRunAt != nil || !f.nextRunOf(job).Equal(f.clock.now()) {
		t.Fatalf("a una sola vez sale en la pasada siguiente y olvida la pasada del intervalo: %+v (%v)", o, err)
	}
	// Desactivado antes de su pasada se reactiva: nunca corrio como one_time.
	if err := f.uc.DisableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatalf("reactivar un one_time que nunca corrio como tal: %v", err)
	}
	f.clock.advance(30 * time.Second)
	f.uc.ProcessDueJobs(ctx)
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 1 || f.store.jobs[job.ID].IsActive {
		t.Fatalf("se lanza una vez y se desactiva: %d, activo %v", n, f.store.jobs[job.ID].IsActive)
	}
	if o, err := f.uc.GetJobOverview(ctx, job.ID, f.tenantID); err != nil || !o.AlreadyRun {
		t.Fatalf("tras su pasada consta como despachado: %+v (%v)", o, err)
	}
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); !errors.Is(err, domain.ErrOneTimeAlreadyRun) {
		t.Fatalf("ya despachado como one_time: %v", err)
	}

	cron := f.addJob(cronJob("0 3 * * *"))
	f.schedule(cron, time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC))
	toInterval := cron
	fifteen := 15
	toInterval.JobType, toInterval.CronExpression, toInterval.IntervalMinutes = domain.JobTypeInterval, nil, &fifteen
	if _, err := f.uc.UpdateJob(ctx, &toInterval); err != nil {
		t.Fatal(err)
	}
	if got := f.nextRunOf(cron); !got.Equal(f.clock.now().Add(15 * time.Minute)) {
		t.Fatalf("de cron a intervalo, un periodo desde ahora: %v", got)
	}
}

func TestEditarUnIntervaloInactivoNoLoLanzaAlReactivarlo(t *testing.T) {
	f := newFixture(t)
	job := f.addJob(func(j *domain.JobDefinition) { j.IsActive = false })
	f.schedule(job, at(9, 12, 0))
	ten := 10
	edited := job
	edited.IntervalMinutes = &ten
	o, err := f.uc.UpdateJob(ctx, &edited)
	if err != nil || o.NextRunAt != nil {
		t.Fatalf("inactivo no expone proxima ejecucion: %+v (%v)", o, err)
	}
	f.clock.advance(25 * time.Minute)
	if err := f.uc.EnableJob(ctx, job.ID, f.tenantID); err != nil {
		t.Fatal(err)
	}
	// La edicion dejo la rejilla en las 10:10 cada 10 min: reactivado a las 10:25, las 10:30.
	if got := f.nextRunOf(job); !got.Equal(at(10, 30, 0)) {
		t.Fatalf("siguiente %v, se esperaba 10:30", got)
	}
	f.uc.ProcessDueJobs(ctx)
	if n := f.execsOf(job); n != 0 {
		t.Fatalf("reactivar no lanza lo atrasado: %d", n)
	}
}
