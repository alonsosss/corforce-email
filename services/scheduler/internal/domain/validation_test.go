package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func validJob() JobDefinition {
	five := 5
	return JobDefinition{Name: "Informe", Code: "informe", JobType: JobTypeInterval, IntervalMinutes: &five,
		Handler: "reports.daily", Timezone: DefaultTimezone, MaxRetries: 1, TimeoutSeconds: 10}
}

func text(n int) string { return strings.Repeat("a", n) }

func textPtr(s string) *string { return &s }

func intPtr(n int) *int { return &n }

func TestCadaReglaNombraSuCampo(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(j *JobDefinition)
		field string
		cause error
	}{
		{"nombre en blanco", func(j *JobDefinition) { j.Name = " " }, FieldName, ErrInvalidJob},
		{"nombre de mas", func(j *JobDefinition) { j.Name = text(MaxNameLength + 1) }, FieldName, ErrInvalidJob},
		{"nombre con NUL", func(j *JobDefinition) { j.Name = "a\x00b" }, FieldName, ErrInvalidJob},
		{"nombre que no es UTF-8", func(j *JobDefinition) { j.Name = "a\xffb" }, FieldName, ErrInvalidJob},
		{"sin codigo", func(j *JobDefinition) { j.Code = "" }, FieldCode, ErrInvalidJob},
		{"codigo de mas", func(j *JobDefinition) { j.Code = text(MaxCodeLength + 1) }, FieldCode, ErrInvalidJob},
		{"descripcion de mas", func(j *JobDefinition) { j.Description = textPtr(text(MaxDescriptionLength + 1)) }, FieldDescription, ErrInvalidJob},
		{"sin manejador", func(j *JobDefinition) { j.Handler = "" }, FieldHandler, ErrInvalidJob},
		{"manejador de mas", func(j *JobDefinition) { j.Handler = text(MaxHandlerNameLength + 1) }, FieldHandler, ErrInvalidJob},
		{"zona invalida", func(j *JobDefinition) { j.Timezone = "Mars/Olympus_Mons" }, FieldTimezone, ErrInvalidTimezone},
		{"tipo desconocido", func(j *JobDefinition) { j.JobType = "weekly" }, FieldJobType, ErrInvalidJob},
		{"cron invalido", func(j *JobDefinition) {
			j.JobType, j.IntervalMinutes, j.CronExpression = JobTypeCron, nil, textPtr("0 25 * * *")
		}, FieldCronExpression, ErrInvalidCron},
		{"cron de mas en un intervalo", func(j *JobDefinition) { j.CronExpression = textPtr(text(MaxCronExpressionLength + 1)) }, FieldCronExpression, ErrInvalidCron},
		{"intervalo sin minutos", func(j *JobDefinition) { j.IntervalMinutes = nil }, FieldIntervalMinutes, ErrInvalidJob},
		{"intervalo cero", func(j *JobDefinition) { j.IntervalMinutes = intPtr(0) }, FieldIntervalMinutes, ErrInvalidJob},
		{"intervalo de mas", func(j *JobDefinition) { j.IntervalMinutes = intPtr(MaxIntervalMinutes + 1) }, FieldIntervalMinutes, ErrInvalidJob},
		{"minutos fuera de rango en un cron", func(j *JobDefinition) {
			j.JobType, j.CronExpression, j.IntervalMinutes = JobTypeCron, textPtr("0 3 * * *"), intPtr(math.MaxInt32+1)
		}, FieldIntervalMinutes, ErrInvalidJob},
		{"reintentos negativos", func(j *JobDefinition) { j.MaxRetries = -1 }, FieldMaxRetries, ErrInvalidJob},
		{"reintentos de mas", func(j *JobDefinition) { j.MaxRetries = MaxJobRetries + 1 }, FieldMaxRetries, ErrInvalidJob},
		{"plazo negativo", func(j *JobDefinition) { j.TimeoutSeconds = -1 }, FieldTimeoutSeconds, ErrInvalidJob},
		{"plazo de mas", func(j *JobDefinition) { j.TimeoutSeconds = MaxHandlerTimeoutSeconds + 1 }, FieldTimeoutSeconds, ErrInvalidJob},
		{"payload ilegible", func(j *JobDefinition) { j.Payload = textPtr(`{"a":`) }, FieldPayload, ErrInvalidJob},
		{"payload de mas", func(j *JobDefinition) { j.Payload = textPtr(`"` + text(MaxPayloadBytes-1) + `"`) }, FieldPayload, ErrInvalidJob},
	}
	for _, tc := range cases {
		j := validJob()
		tc.mut(&j)
		err := j.Validate()
		var ferr *FieldError
		if !errors.As(err, &ferr) || ferr.Field != tc.field || !errors.Is(err, tc.cause) {
			t.Errorf("%s: %v (campo %v), se esperaba %s con %v", tc.name, err, ferr, tc.field, tc.cause)
		}
	}
}

func TestLosTopesExactosSonValidos(t *testing.T) {
	j := validJob()
	j.Name = strings.Repeat("ñ", MaxNameLength)
	j.Code = text(MaxCodeLength)
	j.Description = textPtr(text(MaxDescriptionLength))
	j.IntervalMinutes = intPtr(MaxIntervalMinutes)
	j.MaxRetries = MaxJobRetries
	j.TimeoutSeconds = MaxHandlerTimeoutSeconds
	j.Payload = textPtr(`"` + text(MaxPayloadBytes-2) + `"`)
	if err := j.Validate(); err != nil {
		t.Fatalf("en el tope: %v", err)
	}
}

func TestElMensajeConservaElErrorDelDominio(t *testing.T) {
	j := validJob()
	j.Name = ""
	if err := j.Validate(); err == nil || err.Error() != "invalid job: name is required" {
		t.Fatalf("mensaje: %v", err)
	}
}

func TestElPlazoNoSuperaElDelManejador(t *testing.T) {
	spec := HandlerSpec{Name: "reports.daily", MaxTimeoutSeconds: 600}
	j := validJob()
	for _, ok := range []int{0, 600} {
		j.TimeoutSeconds = ok
		if err := j.CheckTimeoutFor(spec); err != nil {
			t.Errorf("plazo %d: %v", ok, err)
		}
	}
	j.TimeoutSeconds = 601
	var ferr *FieldError
	if err := j.CheckTimeoutFor(spec); !errors.As(err, &ferr) || ferr.Field != FieldTimeoutSeconds || !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("plazo de mas para el manejador: %v", err)
	}
}

func TestTareaValida(t *testing.T) {
	valid := func() ScheduledTask {
		return ScheduledTask{Name: "Aviso", Handler: "reports.daily", TriggerAt: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)}
	}
	if task := valid(); task.Validate() != nil {
		t.Fatal("una tarea valida se rechaza")
	}
	for field, mut := range map[string]func(t *ScheduledTask){
		FieldName:        func(t *ScheduledTask) { t.Name = text(MaxNameLength + 1) },
		FieldHandler:     func(t *ScheduledTask) { t.Handler = "" },
		FieldTriggerAt:   func(t *ScheduledTask) { t.TriggerAt = time.Time{} },
		FieldDescription: func(t *ScheduledTask) { t.Description = textPtr(text(MaxDescriptionLength + 1)) },
		FieldPayload:     func(t *ScheduledTask) { t.Payload = textPtr("{") },
	} {
		task := valid()
		mut(&task)
		var ferr *FieldError
		if err := task.Validate(); !errors.As(err, &ferr) || ferr.Field != field || !errors.Is(err, ErrInvalidTask) {
			t.Errorf("%s: %v", field, err)
		}
	}
}

func TestLosTopesDeTextoSonLosDeSusColumnas(t *testing.T) {
	// Anchos de 01_scheduler.sql y 03_job_timezone.sql; la prueba de integracion los lee de
	// la base.
	for name, got := range map[string]int{
		"name": MaxNameLength, "code": MaxCodeLength, "cron_expression": MaxCronExpressionLength,
		"handler": MaxHandlerNameLength, "timezone": MaxTimezoneLength,
	} {
		want := map[string]int{"name": 255, "code": 100, "cron_expression": 100, "handler": 255, "timezone": 64}[name]
		if got != want {
			t.Errorf("%s: %d, la columna admite %d", name, got, want)
		}
	}
	if MinIntervalMinutes != 1 || MaxIntervalMinutes > math.MaxInt32 || MaxJobRetries > math.MaxInt32 || MaxHandlerTimeoutSeconds > math.MaxInt32 {
		t.Fatal("los topes numericos caben en integer")
	}
}

func TestDesplazamientoDeLaPagina(t *testing.T) {
	for _, tc := range []struct {
		page, perPage int
		want          int64
	}{
		{0, 20, 0}, {1, 20, 0}, {3, 20, 40}, {math.MaxInt, 100, math.MaxInt64},
	} {
		if got := (JobFilter{Page: tc.page, PerPage: tc.perPage}).Offset(); got != tc.want {
			t.Errorf("pagina %d de %d: %d, se esperaba %d", tc.page, tc.perPage, got, tc.want)
		}
	}
}

func TestUnTrabajoInactivoNoTieneProximaEjecucion(t *testing.T) {
	next := time.Date(2026, 9, 13, 11, 0, 0, 0, time.UTC)
	j := validJob()
	j.IsActive = true
	if o := NewJobOverview(j, &next, nil, nil); o.NextRunAt == nil || !o.NextRunAt.Equal(next) {
		t.Fatalf("activo: %v", o.NextRunAt)
	}
	j.IsActive = false
	if o := NewJobOverview(j, &next, nil, nil); o.NextRunAt != nil {
		t.Fatalf("inactivo: %v", o.NextRunAt)
	}
}

func TestJobTypesEnOrden(t *testing.T) {
	if got := strings.Join(JobTypes(), ","); got != "cron,interval,one_time" {
		t.Fatalf("job types: %s", got)
	}
}
