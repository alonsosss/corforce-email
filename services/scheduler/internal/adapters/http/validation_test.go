package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/google/uuid"
)

// absent quita el campo del cuerpo; nil lo manda como null.
type absentField struct{}

var absent = absentField{}

// jobBody es un trabajo de intervalo valido con los cambios de fields.
func jobBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	body := map[string]any{"name": "Informe", "code": "informe", "job_type": "interval", "interval_minutes": 15, "handler": "reports.daily"}
	for k, v := range fields {
		if v == absent {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) (code, field string) {
	t.Helper()
	code, field, _ = errorWithRule(t, rec)
	return code, field
}

// errorWithRule lee el codigo, el campo y la regla de un error de campo.
func errorWithRule(t *testing.T, rec *httptest.ResponseRecorder) (code, field, rule string) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil {
		t.Fatalf("se esperaba un error: %d %q", rec.Code, rec.Body.String())
	}
	return env.Error.Code, env.Error.Details["field"], env.Error.Details["rule"]
}

func long(n int) string { return strings.Repeat("a", n) }

func TestUn422NombraElCampoYLaReglaQueFalla(t *testing.T) {
	srv, jobs, _ := timezoneServer(t)
	const v, tz = "VALIDATION_ERROR", "INVALID_TIMEZONE"
	cases := []struct {
		name   string
		fields map[string]any
		code   string
		field  string
		rule   string
	}{
		{"nombre de 256", map[string]any{"name": long(256)}, v, "name", domain.RuleTooLong},
		{"nombre en blanco", map[string]any{"name": "   "}, v, "name", domain.RuleRequired},
		{"sin nombre", map[string]any{"name": absent}, v, "name", domain.RuleRequired},
		{"nombre con NUL", map[string]any{"name": "a\x00b"}, v, "name", domain.RuleInvalidFormat},
		{"codigo de 101", map[string]any{"code": long(101)}, v, "code", domain.RuleTooLong},
		{"sin codigo", map[string]any{"code": absent}, v, "code", domain.RuleRequired},
		{"descripcion de 2001", map[string]any{"description": long(2001)}, v, "description", domain.RuleTooLong},
		{"sin tipo", map[string]any{"job_type": absent}, v, "job_type", domain.RuleRequired},
		{"tipo desconocido", map[string]any{"job_type": "weekly"}, v, "job_type", domain.RuleNotAllowed},
		{"sin manejador", map[string]any{"handler": absent}, v, "handler", domain.RuleRequired},
		{"manejador de 256", map[string]any{"handler": long(256)}, v, "handler", domain.RuleTooLong},
		{"manejador fuera del catalogo", map[string]any{"handler": "no.existe"}, v, "handler", domain.RuleNotAllowed},
		{"cron invalido", map[string]any{"job_type": "cron", "cron_expression": "0 25 * * *", "interval_minutes": nil}, v, "cron_expression", domain.RuleInvalidFormat},
		{"cron que nunca ocurre", map[string]any{"job_type": "cron", "cron_expression": "0 0 30 2 *", "interval_minutes": nil}, v, "cron_expression", domain.RuleNeverMatches},
		{"@every corto", map[string]any{"job_type": "cron", "cron_expression": "@every 10s", "interval_minutes": nil}, v, "cron_expression", domain.RuleOutOfRange},
		{"descriptor no admitido", map[string]any{"job_type": "cron", "cron_expression": "@yearly", "interval_minutes": nil}, v, "cron_expression", domain.RuleNotAllowed},
		{"cron de 101 en un intervalo", map[string]any{"cron_expression": long(101)}, v, "cron_expression", domain.RuleTooLong},
		{"zona vacia", map[string]any{"timezone": ""}, tz, "timezone", domain.RuleRequired},
		{"zona con otra forma", map[string]any{"timezone": "+05:00"}, tz, "timezone", domain.RuleInvalidFormat},
		{"zona invalida", map[string]any{"timezone": "Mars/Olympus_Mons"}, tz, "timezone", domain.RuleNotAllowed},
		{"intervalo cero", map[string]any{"interval_minutes": 0}, v, "interval_minutes", domain.RuleOutOfRange},
		{"intervalo de mas", map[string]any{"interval_minutes": 525601}, v, "interval_minutes", domain.RuleOutOfRange},
		{"intervalo fuera de integer", map[string]any{"interval_minutes": int64(1) << 31}, v, "interval_minutes", domain.RuleOutOfRange},
		{"intervalo null", map[string]any{"interval_minutes": nil}, v, "interval_minutes", domain.RuleRequired},
		{"reintentos negativos", map[string]any{"max_retries": -1}, v, "max_retries", domain.RuleOutOfRange},
		{"reintentos de mas", map[string]any{"max_retries": 11}, v, "max_retries", domain.RuleOutOfRange},
		{"reintentos fuera de integer", map[string]any{"max_retries": int64(1) << 40}, v, "max_retries", domain.RuleOutOfRange},
		{"plazo negativo", map[string]any{"timeout_seconds": -1}, v, "timeout_seconds", domain.RuleOutOfRange},
		{"plazo mayor que el del manejador", map[string]any{"timeout_seconds": 601}, v, "timeout_seconds", domain.RuleOutOfRange},
		{"plazo de mas", map[string]any{"timeout_seconds": 604801}, v, "timeout_seconds", domain.RuleOutOfRange},
		{"payload ilegible", map[string]any{"payload": `{"a":`}, v, "payload", domain.RuleInvalidFormat},
		{"payload con NUL", map[string]any{"payload": `{"a":"\u0000"}`}, v, "payload", domain.RuleInvalidFormat},
		{"payload de mas", map[string]any{"payload": `"` + long(65535) + `"`}, v, "payload", domain.RuleTooLong},
	}
	for _, tc := range cases {
		rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", jobBody(t, tc.fields), uuid.NewString())
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, se esperaba 422", tc.name, rec.Code, rec.Body.String())
			continue
		}
		if code, field, rule := errorWithRule(t, rec); code != tc.code || field != tc.field || rule != tc.rule {
			t.Errorf("%s: %s en %q con %q, se esperaba %s en %q con %q (%s)", tc.name, code, field, rule, tc.code, tc.field, tc.rule, rec.Body.String())
		}
	}
	if jobs.job != nil {
		t.Fatal("un trabajo rechazado no se guarda")
	}
}

func TestLosTopesExactosSeAceptan(t *testing.T) {
	srv, jobs, _ := timezoneServer(t)
	name := strings.Repeat("ñ", 255)
	rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", jobBody(t, map[string]any{
		"name": name, "code": long(100), "description": long(2000), "interval_minutes": 525600,
		"max_retries": 10, "timeout_seconds": 600, "payload": `"` + long(65534) + `"`,
	}), uuid.NewString())
	if rec.Code != http.StatusCreated {
		t.Fatalf("en el tope: %d %s", rec.Code, rec.Body.String())
	}
	if utf8.RuneCountInString(jobs.job.Name) != 255 || len(*jobs.job.Payload) != 65536 {
		t.Fatalf("guardado: nombre de %d caracteres, payload de %d bytes", utf8.RuneCountInString(jobs.job.Name), len(*jobs.job.Payload))
	}
}

func TestEditarConUnNombreDeMasEs422(t *testing.T) {
	srv, jobs, _ := timezoneServer(t)
	if rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", jobBody(t, nil), uuid.NewString()); rec.Code != http.StatusCreated {
		t.Fatalf("crear: %d %s", rec.Code, rec.Body.String())
	}
	rec := request(t, srv, http.MethodPut, "/api/v1/scheduler/jobs/"+jobs.job.ID.String(),
		jobBody(t, map[string]any{"code": absent, "name": long(256), "version": 1}), uuid.NewString())
	if code, field, rule := errorWithRule(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || field != "name" || rule != domain.RuleTooLong {
		t.Fatalf("editar: %d %s", rec.Code, rec.Body.String())
	}
	if jobs.job.Name != "Informe" {
		t.Fatalf("la edicion rechazada no se guarda: %q", jobs.job.Name)
	}
}

// takenCode es un repositorio donde todo codigo ya existe.
type takenCode struct{ *storedJobs }

func (takenCode) GetByCode(context.Context, string) (*domain.JobDefinition, error) {
	return &domain.JobDefinition{}, nil
}

func TestCodigoRepetidoEs409ConElCampoYLaRegla(t *testing.T) {
	schedules := &plannedSchedules{}
	srv := newJobServer(t, takenCode{&storedJobs{schedules: schedules}}, schedules)
	rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", jobBody(t, nil), uuid.NewString())
	if code, field, rule := errorWithRule(t, rec); rec.Code != http.StatusConflict || code != "CONFLICT" || field != "code" || rule != domain.RuleDuplicate {
		t.Fatalf("codigo repetido: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnaTareaInvalidaNombraSuCampoYSuRegla(t *testing.T) {
	srv, _, _ := timezoneServer(t)
	const trigger = "2026-09-14T10:00:00Z"
	for body, want := range map[string][2]string{
		`{}`: {"name", domain.RuleRequired},
		`{"name":"` + long(256) + `","trigger_at":"` + trigger + `","handler":"reports.daily"}`:                        {"name", domain.RuleTooLong},
		`{"name":"Aviso","trigger_at":"manana","handler":"reports.daily"}`:                                             {"trigger_at", domain.RuleInvalidFormat},
		`{"name":"Aviso","handler":"reports.daily"}`:                                                                   {"trigger_at", domain.RuleRequired},
		`{"name":"Aviso","trigger_at":"` + trigger + `"}`:                                                              {"handler", domain.RuleRequired},
		`{"name":"Aviso","trigger_at":"` + trigger + `","handler":"` + long(256) + `"}`:                                {"handler", domain.RuleTooLong},
		`{"name":"Aviso","trigger_at":"` + trigger + `","handler":"reports.daily","description":"` + long(2001) + `"}`: {"description", domain.RuleTooLong},
		`{"name":"Aviso","trigger_at":"` + trigger + `","handler":"reports.daily","payload":"{"}`:                      {"payload", domain.RuleInvalidFormat},
	} {
		rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/tasks", body, uuid.NewString())
		if code, field, rule := errorWithRule(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || field != want[0] || rule != want[1] {
			t.Errorf("tarea %.60s: %d %s, se esperaba %s/%s", body, rec.Code, rec.Body.String(), want[0], want[1])
		}
	}
}

func TestUnFiltroIlegibleNombraSuCampoYSuRegla(t *testing.T) {
	c := contractServer()
	rec := request(t, c.srv, http.MethodGet, "/api/v1/scheduler/jobs?is_active=quizas", "", uuid.NewString())
	if code, field, rule := errorWithRule(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" ||
		field != domain.FieldIsActive || rule != domain.RuleInvalidFormat {
		t.Fatalf("is_active ilegible: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnCuerpoDeMasEs400(t *testing.T) {
	srv, jobs, _ := timezoneServer(t)
	rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/jobs",
		jobBody(t, map[string]any{"payload": `"` + long(maxRequestBody) + `"`}), uuid.NewString())
	if rec.Code != http.StatusBadRequest || jobs.job != nil {
		t.Fatalf("cuerpo de mas: %d %s", rec.Code, rec.Body.String())
	}
}

func TestElCierreDelEjecutorNombraSuCampoYSuRegla(t *testing.T) {
	f := newInternalFixture(t, domain.StatusRunning)
	for action, cases := range map[string]map[string][2]string{
		"fail": {
			`{"error":"x"}`:                    {"retryable", domain.RuleRequired},
			`{"error":"  ","retryable":false}`: {"error", domain.RuleRequired},
		},
		"complete": {`{"result":{"a":"\u0000"}}`: {"result", domain.RuleInvalidFormat}},
	} {
		for body, want := range cases {
			rec := f.post(t, action, body)
			if code, field, rule := errorWithRule(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || field != want[0] || rule != want[1] {
				t.Errorf("%s %s: %d %s, se esperaba %s/%s", action, body, rec.Code, rec.Body.String(), want[0], want[1])
			}
		}
	}
}
