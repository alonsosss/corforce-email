package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

// resumeClock es una hora del 2026-09-13 en UTC, el reloj de estas pruebas.
func resumeClock(h, m, s int) time.Time { return time.Date(2026, 9, 13, h, m, s, 0, time.UTC) }

func resumeIntervalJob(minutes int) JobDefinition {
	return JobDefinition{JobType: JobTypeInterval, IntervalMinutes: &minutes, Timezone: DefaultTimezone}
}

func TestReactivarUnIntervaloSigueSuRejillaSinDeriva(t *testing.T) {
	now := resumeClock(10, 0, 0)
	job := resumeIntervalJob(5)
	cases := []struct {
		name     string
		schedule *JobSchedule
		want     time.Time
	}{
		{"sin calendario cuenta un periodo desde ahora", nil, resumeClock(10, 5, 0)},
		{"guardada en el pasado: la primera de su rejilla", &JobSchedule{NextRunAt: resumeClock(9, 12, 0)}, resumeClock(10, 2, 0)},
		{"la rejilla conserva sus segundos", &JobSchedule{NextRunAt: resumeClock(9, 12, 30)}, resumeClock(10, 2, 30)},
		{"guardada justo ahora: la siguiente, no un lanzamiento atrasado", &JobSchedule{NextRunAt: now}, resumeClock(10, 5, 0)},
		{"guardada dentro del periodo se respeta", &JobSchedule{NextRunAt: resumeClock(10, 3, 0)}, resumeClock(10, 3, 0)},
		{"guardada a mas de un periodo (se acorto el intervalo)", &JobSchedule{NextRunAt: resumeClock(12, 0, 0)}, resumeClock(10, 5, 0)},
		{"guardada hace siglos no desborda", &JobSchedule{NextRunAt: time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC)}, resumeClock(10, 5, 0)},
	}
	for _, tc := range cases {
		got, err := job.ResumeAt(tc.schedule, now)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%s: %v (%v), se esperaba %v", tc.name, got, err, tc.want)
		}
	}
}

func TestUnIntervaloReactivadoNuncaQuedaEnElPasado(t *testing.T) {
	now := resumeClock(10, 0, 0)
	for _, minutes := range []int{1, 5, 7, 60, MaxIntervalMinutes} {
		job := resumeIntervalJob(minutes)
		period := time.Duration(minutes) * time.Minute
		for back := time.Duration(0); back <= 3*time.Hour; back += 13 * time.Second {
			anchor := now.Add(-back)
			got, err := job.ResumeAt(&JobSchedule{NextRunAt: anchor}, now)
			if err != nil {
				t.Fatalf("%d min, guardada %v: %v", minutes, anchor, err)
			}
			if !got.After(now) || got.After(now.Add(period)) || got.Sub(anchor)%period != 0 {
				t.Fatalf("%d min, guardada %v: %v no es la primera de la rejilla tras %v", minutes, anchor, got, now)
			}
		}
	}
}

func TestReactivarUnOneTime(t *testing.T) {
	now := resumeClock(10, 0, 0)
	job := JobDefinition{JobType: JobTypeOneTime, Timezone: DefaultTimezone}
	for name, schedule := range map[string]*JobSchedule{
		"sin calendario":        nil,
		"nunca despachado":      {NextRunAt: resumeClock(9, 0, 0)},
		"previsto en el futuro": {NextRunAt: resumeClock(11, 0, 0)},
	} {
		if got, err := job.ResumeAt(schedule, now); err != nil || !got.Equal(now) {
			t.Errorf("%s: %v (%v), se esperaba ahora", name, got, err)
		}
	}
	ran := resumeClock(9, 0, 30)
	if _, err := job.ResumeAt(&JobSchedule{NextRunAt: ran, LastRunAt: &ran}, now); !errors.Is(err, ErrOneTimeAlreadyRun) {
		t.Fatalf("ya despachado por el calendario: %v", err)
	}
}

func TestReactivarUnCronYLosDatosQueNoSeReanudan(t *testing.T) {
	now := resumeClock(10, 0, 0)
	daily := "0 3 * * *"
	cron := JobDefinition{JobType: JobTypeCron, CronExpression: &daily, Timezone: DefaultTimezone}
	if got, err := cron.ResumeAt(&JobSchedule{NextRunAt: time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)}, now); err != nil ||
		!got.Equal(time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("cron: %v (%v)", got, err)
	}

	bad := "cada hora"
	cases := []struct {
		name  string
		job   JobDefinition
		field string
		rule  string
	}{
		{"cron invalido", JobDefinition{JobType: JobTypeCron, CronExpression: &bad, Timezone: DefaultTimezone}, FieldCronExpression, RuleInvalidFormat},
		{"intervalo sin minutos", JobDefinition{JobType: JobTypeInterval, Timezone: DefaultTimezone}, FieldIntervalMinutes, RuleRequired},
		{"intervalo cero", resumeIntervalJob(0), FieldIntervalMinutes, RuleOutOfRange},
		{"intervalo de mas", resumeIntervalJob(math.MaxInt32), FieldIntervalMinutes, RuleOutOfRange},
		{"tipo desconocido", JobDefinition{JobType: "weekly"}, FieldJobType, RuleNotAllowed},
	}
	for _, tc := range cases {
		_, err := tc.job.ResumeAt(nil, now)
		var ferr *FieldError
		if !errors.As(err, &ferr) || ferr.Field != tc.field || ferr.Rule != tc.rule {
			t.Errorf("%s: %v, se esperaba %s/%s", tc.name, err, tc.field, tc.rule)
		}
	}
}

func TestCancelarUnaTarea(t *testing.T) {
	task := ScheduledTask{Status: TaskStatusScheduled}
	if changed, err := task.Cancel(); !changed || err != nil || task.Status != TaskStatusCancelled {
		t.Fatalf("programada: %v %v %s", changed, err, task.Status)
	}
	if changed, err := task.Cancel(); changed || err != nil || task.Status != TaskStatusCancelled {
		t.Fatalf("cancelar dos veces no cambia nada: %v %v %s", changed, err, task.Status)
	}
	done := ScheduledTask{Status: TaskStatusExecuted}
	if changed, err := done.Cancel(); changed || !errors.Is(err, ErrTaskNotCancellable) || done.Status != TaskStatusExecuted {
		t.Fatalf("ejecutada: %v %v %s", changed, err, done.Status)
	}
}

func TestDesplazamientoDeCualquierPagina(t *testing.T) {
	for _, tc := range []struct {
		page, perPage int
		want          int64
	}{
		{0, 20, 0}, {1, 20, 0}, {3, 20, 40}, {2, 0, 0}, {-5, 20, 0},
		{math.MaxInt, 100, math.MaxInt64}, {math.MaxInt64/100 + 2, 100, math.MaxInt64},
	} {
		if got := PageOffset(tc.page, tc.perPage); got != tc.want {
			t.Errorf("pagina %d de %d: %d, se esperaba %d", tc.page, tc.perPage, got, tc.want)
		}
		if got := (TaskFilter{Page: tc.page, PerPage: tc.perPage}).Offset(); got != tc.want {
			t.Errorf("tareas, pagina %d de %d: %d", tc.page, tc.perPage, got)
		}
	}
}
