package domain

import (
	"errors"
	"testing"
	"time"
)

func TestDespacharUnIntervaloSigueSuRejilla(t *testing.T) {
	job := resumeIntervalJob(5)
	cases := []struct {
		name           string
		scheduled, now time.Time
		want           time.Time
	}{
		{"el ticker llega 29 s tarde", resumeClock(10, 0, 0), resumeClock(10, 0, 29), resumeClock(10, 5, 0)},
		{"despachado en su hora", resumeClock(10, 0, 0), resumeClock(10, 0, 0), resumeClock(10, 5, 0)},
		{"la rejilla conserva sus segundos", resumeClock(10, 0, 30), resumeClock(10, 1, 10), resumeClock(10, 5, 30)},
		{"mas de un periodo tarde", resumeClock(10, 0, 0), resumeClock(10, 12, 0), resumeClock(10, 15, 0)},
		{"la ocurrencia que cae en now queda cubierta", resumeClock(10, 0, 0), resumeClock(10, 10, 0), resumeClock(10, 15, 0)},
		{"siete horas caido", resumeClock(3, 0, 0), resumeClock(10, 17, 0), resumeClock(10, 20, 0)},
		{"prevista hace siglos no desborda", time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC), resumeClock(10, 0, 0), resumeClock(10, 5, 0)},
		{"una prevista posterior a now no se repite", resumeClock(10, 3, 0), resumeClock(10, 0, 0), resumeClock(10, 8, 0)},
	}
	for _, tc := range cases {
		got, err := job.NextAfterDispatch(tc.scheduled, tc.now)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%s: %v (%v), se esperaba %v", tc.name, got, err, tc.want)
		}
	}
}

func TestElDespachoDeUnIntervaloNoDerivaNiQuedaEnElPasado(t *testing.T) {
	scheduled := resumeClock(10, 0, 0)
	for _, minutes := range []int{1, 5, 7, 60, MaxIntervalMinutes} {
		job := resumeIntervalJob(minutes)
		period := time.Duration(minutes) * time.Minute
		for late := time.Duration(0); late <= 3*time.Hour; late += 13 * time.Second {
			now := scheduled.Add(late)
			got, err := job.NextAfterDispatch(scheduled, now)
			if err != nil {
				t.Fatalf("%d min, %v tarde: %v", minutes, late, err)
			}
			if !got.After(now) || got.After(now.Add(period)) || got.Sub(scheduled)%period != 0 {
				t.Fatalf("%d min, %v tarde: %v no es la primera de la rejilla de %v tras %v", minutes, late, got, scheduled, now)
			}
		}
	}
}

// En Madrid el reloj se atrasa el 2026-10-25 a las 01:00 UTC (las 02:00 a 03:00 de pared se
// repiten) y se adelanta el 2026-03-29 a las 01:00 UTC. Un intervalo cuenta tiempo
// transcurrido: la zona del trabajo no lo mueve.
func TestUnIntervaloCuentaTiempoTranscurridoSinCambiosDeHora(t *testing.T) {
	cases := []struct {
		name           string
		minutes        int
		scheduled, now time.Time
		want           time.Time
	}{
		{"se atrasa el reloj", 60, utc(2026, 10, 25, 0, 30, 0), utc(2026, 10, 25, 1, 10, 0), utc(2026, 10, 25, 1, 30, 0)},
		{"se adelanta el reloj con el proceso caido", 90, utc(2026, 3, 28, 23, 0, 0), utc(2026, 3, 29, 5, 17, 0), utc(2026, 3, 29, 6, 30, 0)},
	}
	for _, tc := range cases {
		for _, tz := range []string{DefaultTimezone, "Europe/Madrid", "America/Santiago", "Pacific/Chatham"} {
			minutes := tc.minutes
			job := JobDefinition{JobType: JobTypeInterval, IntervalMinutes: &minutes, Timezone: tz}
			got, err := job.NextAfterDispatch(tc.scheduled, tc.now)
			if err != nil || !got.Equal(tc.want) {
				t.Errorf("%s en %s: %v (%v), se esperaba %v", tc.name, tz, got, err, tc.want)
			}
		}
	}
}

func TestUnaDefinicionQueNoSePlanificaNombraSuCampo(t *testing.T) {
	now := resumeClock(10, 0, 0)
	zero, negative, bad := 0, -5, "cada hora"
	cases := []struct {
		name  string
		job   JobDefinition
		field string
		rule  string
	}{
		{"intervalo sin minutos", JobDefinition{JobType: JobTypeInterval, Timezone: DefaultTimezone}, FieldIntervalMinutes, RuleRequired},
		{"intervalo a cero", JobDefinition{JobType: JobTypeInterval, IntervalMinutes: &zero, Timezone: DefaultTimezone}, FieldIntervalMinutes, RuleOutOfRange},
		{"intervalo negativo", JobDefinition{JobType: JobTypeInterval, IntervalMinutes: &negative, Timezone: DefaultTimezone}, FieldIntervalMinutes, RuleOutOfRange},
		{"cron invalido", JobDefinition{JobType: JobTypeCron, CronExpression: &bad, Timezone: DefaultTimezone}, FieldCronExpression, RuleInvalidFormat},
		{"tipo desconocido", JobDefinition{JobType: "weekly"}, FieldJobType, RuleNotAllowed},
	}
	for _, tc := range cases {
		_, dispatchErr := tc.job.NextAfterDispatch(now.Add(-time.Minute), now)
		_, firstErr := tc.job.FirstRunAt(now)
		for name, err := range map[string]error{"despacho": dispatchErr, "alta o edicion": firstErr} {
			var ferr *FieldError
			if !errors.As(err, &ferr) || ferr.Field != tc.field || ferr.Rule != tc.rule {
				t.Errorf("%s (%s): %v, se esperaba %s/%s", tc.name, name, err, tc.field, tc.rule)
			}
		}
	}
}

func TestPrimeraEjecucionDeUnaDefinicion(t *testing.T) {
	now := resumeClock(10, 0, 0)
	daily := "0 3 * * *"
	cases := []struct {
		name string
		job  JobDefinition
		want time.Time
	}{
		// Las 10:00 UTC son las 05:00 en Lima: las 03:00 de Lima de manana son las 08:00 UTC.
		{"cron: la primera ocurrencia en su zona", JobDefinition{JobType: JobTypeCron, CronExpression: &daily, Timezone: "America/Lima"}, utc(2026, 9, 14, 8, 0, 0)},
		{"interval: un periodo desde ahora", resumeIntervalJob(15), resumeClock(10, 15, 0)},
		{"one_time: en la pasada siguiente", JobDefinition{JobType: JobTypeOneTime, Timezone: DefaultTimezone}, now},
	}
	for _, tc := range cases {
		got, err := tc.job.FirstRunAt(now)
		if err != nil || !got.Equal(tc.want) || got.Before(now) {
			t.Errorf("%s: %v (%v), se esperaba %v", tc.name, got, err, tc.want)
		}
	}
}

func TestQueEdicionCambiaElCalendario(t *testing.T) {
	five, fiveAgain, ten := 5, 5, 10
	hourly, hourlyAgain, daily := "0 * * * *", "0 * * * *", "0 3 * * *"
	interval := JobDefinition{JobType: JobTypeInterval, IntervalMinutes: &five, Timezone: DefaultTimezone, Name: "a"}
	cron := JobDefinition{JobType: JobTypeCron, CronExpression: &hourly, Timezone: DefaultTimezone}
	once := JobDefinition{JobType: JobTypeOneTime, Timezone: DefaultTimezone}
	with := func(j JobDefinition, mut func(*JobDefinition)) *JobDefinition {
		mut(&j)
		return &j
	}
	cases := []struct {
		name       string
		prev, next *JobDefinition
		want       bool
	}{
		{"sin definicion guardada", nil, &interval, true},
		{"nombre, manejador, reintentos y plazo", &interval, with(interval, func(j *JobDefinition) {
			j.Name, j.Handler, j.MaxRetries, j.TimeoutSeconds = "b", "otro", 3, 9
		}), false},
		{"los mismos minutos", &interval, with(interval, func(j *JobDefinition) { j.IntervalMinutes = &fiveAgain }), false},
		{"otros minutos", &interval, with(interval, func(j *JobDefinition) { j.IntervalMinutes = &ten }), true},
		{"la zona no mueve un intervalo", &interval, with(interval, func(j *JobDefinition) { j.Timezone = "America/Lima" }), false},
		{"la misma expresion", &cron, with(cron, func(j *JobDefinition) { j.CronExpression = &hourlyAgain }), false},
		{"otra expresion", &cron, with(cron, func(j *JobDefinition) { j.CronExpression = &daily }), true},
		{"otra zona de un cron", &cron, with(cron, func(j *JobDefinition) { j.Timezone = "America/Lima" }), true},
		{"otro tipo", &interval, &once, true},
		{"un one_time con otro nombre", &once, with(once, func(j *JobDefinition) { j.Name = "otro" }), false},
	}
	for _, tc := range cases {
		if got := tc.next.ScheduleChanged(tc.prev); got != tc.want {
			t.Errorf("%s: %v, se esperaba %v", tc.name, got, tc.want)
		}
	}
}

// already_run del API y el rechazo de ResumeAt son la misma regla.
func TestUnOneTimeYaDespachadoEsElQueNoSeReactiva(t *testing.T) {
	ran := resumeClock(9, 0, 0)
	once := JobDefinition{JobType: JobTypeOneTime, Timezone: DefaultTimezone}
	cases := []struct {
		name string
		job  JobDefinition
		last *time.Time
		want bool
	}{
		{"one_time despachado", once, &ran, true},
		{"one_time nunca despachado", once, nil, false},
		{"un intervalo con pasadas", resumeIntervalJob(5), &ran, false},
	}
	for _, tc := range cases {
		overview := NewJobOverview(tc.job, nil, tc.last, nil)
		_, err := tc.job.ResumeAt(&JobSchedule{NextRunAt: ran, LastRunAt: tc.last}, resumeClock(10, 0, 0))
		refused := errors.Is(err, ErrOneTimeAlreadyRun)
		if tc.job.OneTimeAlreadyRun(tc.last) != tc.want || overview.AlreadyRun != tc.want || refused != tc.want {
			t.Errorf("%s: regla %v, overview %v, rechazo %v; se esperaba %v",
				tc.name, tc.job.OneTimeAlreadyRun(tc.last), overview.AlreadyRun, refused, tc.want)
		}
	}
}
