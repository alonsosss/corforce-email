package domain

import (
	"errors"
	"regexp"
	"testing"
	"time"
)

func TestCadaErrorDeCampoLlevaSuRegla(t *testing.T) {
	job := func(mut func(j *JobDefinition)) error {
		j := validJob()
		mut(&j)
		return j.Validate()
	}
	cron := func(expr string) error {
		_, err := ParseCron(expr, DefaultTimezone)
		return err
	}
	zone := func(name string) error {
		_, err := LoadTimezone(name)
		return err
	}
	catalog, err := NewHandlerCatalog([]HandlerSpec{{Name: "reports.daily", Service: "reports", MaxTimeoutSeconds: 600, Scopes: []HandlerScope{ScopeTenant}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := func(name string, platform bool) error {
		_, err := catalog.Resolve(name, platform)
		return err
	}
	task := func(mut func(t *ScheduledTask)) error {
		task := ScheduledTask{Name: "Aviso", Handler: "reports.daily", TriggerAt: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)}
		mut(&task)
		return task.Validate()
	}
	_, badResult := NormalizeResult([]byte(`{"a":`))
	timeout := validJob()
	timeout.TimeoutSeconds = 601

	cases := []struct {
		name  string
		err   error
		field string
		rule  string
	}{
		{"nombre en blanco", job(func(j *JobDefinition) { j.Name = " " }), FieldName, RuleRequired},
		{"nombre de mas", job(func(j *JobDefinition) { j.Name = text(MaxNameLength + 1) }), FieldName, RuleTooLong},
		{"nombre con NUL", job(func(j *JobDefinition) { j.Name = "a\x00b" }), FieldName, RuleInvalidFormat},
		{"nombre que no es UTF-8", job(func(j *JobDefinition) { j.Name = "a\xffb" }), FieldName, RuleInvalidFormat},
		{"sin codigo", job(func(j *JobDefinition) { j.Code = "" }), FieldCode, RuleRequired},
		{"codigo de mas", job(func(j *JobDefinition) { j.Code = text(MaxCodeLength + 1) }), FieldCode, RuleTooLong},
		{"descripcion de mas", job(func(j *JobDefinition) { j.Description = textPtr(text(MaxDescriptionLength + 1)) }), FieldDescription, RuleTooLong},
		{"sin manejador", job(func(j *JobDefinition) { j.Handler = "" }), FieldHandler, RuleRequired},
		{"sin tipo", job(func(j *JobDefinition) { j.JobType = "" }), FieldJobType, RuleRequired},
		{"tipo desconocido", job(func(j *JobDefinition) { j.JobType = "weekly" }), FieldJobType, RuleNotAllowed},
		{"intervalo sin minutos", job(func(j *JobDefinition) { j.IntervalMinutes = nil }), FieldIntervalMinutes, RuleRequired},
		{"intervalo cero", job(func(j *JobDefinition) { j.IntervalMinutes = intPtr(0) }), FieldIntervalMinutes, RuleOutOfRange},
		{"reintentos de mas", job(func(j *JobDefinition) { j.MaxRetries = MaxJobRetries + 1 }), FieldMaxRetries, RuleOutOfRange},
		{"plazo negativo", job(func(j *JobDefinition) { j.TimeoutSeconds = -1 }), FieldTimeoutSeconds, RuleOutOfRange},
		{"plazo mayor que el del manejador", timeout.CheckTimeoutFor(HandlerSpec{Name: "reports.daily", MaxTimeoutSeconds: 600}), FieldTimeoutSeconds, RuleOutOfRange},
		{"payload de mas", job(func(j *JobDefinition) { j.Payload = textPtr(`"` + text(MaxPayloadBytes-1) + `"`) }), FieldPayload, RuleTooLong},
		{"payload ilegible", job(func(j *JobDefinition) { j.Payload = textPtr(`{"a":`) }), FieldPayload, RuleInvalidFormat},
		{"cron de mas en un intervalo", job(func(j *JobDefinition) { j.CronExpression = textPtr(text(MaxCronExpressionLength + 1)) }), FieldCronExpression, RuleTooLong},
		{"cron vacio", cron("  "), FieldCronExpression, RuleRequired},
		{"cron de mas", cron(text(MaxCronExpressionLength + 1)), FieldCronExpression, RuleTooLong},
		{"cron con la zona dentro", cron("CRON_TZ=America/Lima 0 9 * * *"), FieldCronExpression, RuleInvalidFormat},
		{"descriptor no admitido", cron("@yearly"), FieldCronExpression, RuleNotAllowed},
		{"cron ilegible", cron("0 25 * * *"), FieldCronExpression, RuleInvalidFormat},
		{"@every por debajo del minimo", cron("@every 30s"), FieldCronExpression, RuleOutOfRange},
		{"cron que nunca ocurre", cron("0 0 30 2 *"), FieldCronExpression, RuleNeverMatches},
		{"zona vacia", zone(""), FieldTimezone, RuleRequired},
		{"zona de mas", zone(text(MaxTimezoneLength + 1)), FieldTimezone, RuleTooLong},
		{"desplazamiento suelto", zone("+05:00"), FieldTimezone, RuleInvalidFormat},
		{"zona Local", zone("Local"), FieldTimezone, RuleInvalidFormat},
		{"zona que la base no carga", zone("Mars/Olympus_Mons"), FieldTimezone, RuleNotAllowed},
		{"manejador fuera del catalogo", handler("no.existe", false), FieldHandler, RuleNotAllowed},
		{"manejador de otro alcance", handler("reports.daily", true), FieldHandler, RuleNotAllowed},
		{"tarea sin fecha", task(func(t *ScheduledTask) { t.TriggerAt = time.Time{} }), FieldTriggerAt, RuleRequired},
		{"tarea sin nombre", task(func(t *ScheduledTask) { t.Name = "" }), FieldName, RuleRequired},
		{"resultado ilegible", badResult, FieldResult, RuleInvalidFormat},
	}
	known := map[string]bool{}
	for _, r := range Rules() {
		known[r] = true
	}
	for _, tc := range cases {
		var ferr *FieldError
		if !errors.As(tc.err, &ferr) {
			t.Errorf("%s: %v no es un error de campo", tc.name, tc.err)
			continue
		}
		if ferr.Field != tc.field || ferr.Rule != tc.rule || !known[ferr.Rule] {
			t.Errorf("%s: %s/%s, se esperaba %s/%s", tc.name, ferr.Field, ferr.Rule, tc.field, tc.rule)
		}
	}
}

// Las reglas son contrato del API: estables, sin repetir y en snake_case como el resto.
func TestLasReglasSonUnContratoEstable(t *testing.T) {
	want := []string{"required", "too_long", "out_of_range", "invalid_format", "not_allowed", "never_matches", "duplicate"}
	got := Rules()
	if len(got) != len(want) {
		t.Fatalf("reglas: %v", got)
	}
	snake := regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)
	for i, r := range got {
		if r != want[i] || !snake.MatchString(r) {
			t.Errorf("regla %d: %q, se esperaba %q", i, r, want[i])
		}
	}
}
