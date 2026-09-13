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
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil {
		t.Fatalf("se esperaba un error: %d %q", rec.Code, rec.Body.String())
	}
	return env.Error.Code, env.Error.Details["field"]
}

func long(n int) string { return strings.Repeat("a", n) }

func TestUn422NombraElCampoQueFalla(t *testing.T) {
	srv, jobs, _ := timezoneServer(t)
	cases := []struct {
		name   string
		fields map[string]any
		code   string
		field  string
	}{
		{"nombre de 256", map[string]any{"name": long(256)}, "VALIDATION_ERROR", "name"},
		{"nombre en blanco", map[string]any{"name": "   "}, "VALIDATION_ERROR", "name"},
		{"sin nombre", map[string]any{"name": absent}, "VALIDATION_ERROR", "name"},
		{"nombre con NUL", map[string]any{"name": "a\x00b"}, "VALIDATION_ERROR", "name"},
		{"codigo de 101", map[string]any{"code": long(101)}, "VALIDATION_ERROR", "code"},
		{"sin codigo", map[string]any{"code": absent}, "VALIDATION_ERROR", "code"},
		{"descripcion de 2001", map[string]any{"description": long(2001)}, "VALIDATION_ERROR", "description"},
		{"sin tipo", map[string]any{"job_type": absent}, "VALIDATION_ERROR", "job_type"},
		{"tipo desconocido", map[string]any{"job_type": "weekly"}, "VALIDATION_ERROR", "job_type"},
		{"sin manejador", map[string]any{"handler": absent}, "VALIDATION_ERROR", "handler"},
		{"manejador de 256", map[string]any{"handler": long(256)}, "VALIDATION_ERROR", "handler"},
		{"manejador fuera del catalogo", map[string]any{"handler": "no.existe"}, "VALIDATION_ERROR", "handler"},
		{"cron invalido", map[string]any{"job_type": "cron", "cron_expression": "0 25 * * *", "interval_minutes": nil}, "VALIDATION_ERROR", "cron_expression"},
		{"cron de 101 en un intervalo", map[string]any{"cron_expression": long(101)}, "VALIDATION_ERROR", "cron_expression"},
		{"zona invalida", map[string]any{"timezone": "Mars/Olympus_Mons"}, "INVALID_TIMEZONE", "timezone"},
		{"intervalo cero", map[string]any{"interval_minutes": 0}, "VALIDATION_ERROR", "interval_minutes"},
		{"intervalo de mas", map[string]any{"interval_minutes": 525601}, "VALIDATION_ERROR", "interval_minutes"},
		{"intervalo fuera de integer", map[string]any{"interval_minutes": int64(1) << 31}, "VALIDATION_ERROR", "interval_minutes"},
		{"intervalo null", map[string]any{"interval_minutes": nil}, "VALIDATION_ERROR", "interval_minutes"},
		{"reintentos negativos", map[string]any{"max_retries": -1}, "VALIDATION_ERROR", "max_retries"},
		{"reintentos de mas", map[string]any{"max_retries": 11}, "VALIDATION_ERROR", "max_retries"},
		{"reintentos fuera de integer", map[string]any{"max_retries": int64(1) << 40}, "VALIDATION_ERROR", "max_retries"},
		{"plazo negativo", map[string]any{"timeout_seconds": -1}, "VALIDATION_ERROR", "timeout_seconds"},
		{"plazo mayor que el del manejador", map[string]any{"timeout_seconds": 601}, "VALIDATION_ERROR", "timeout_seconds"},
		{"plazo de mas", map[string]any{"timeout_seconds": 604801}, "VALIDATION_ERROR", "timeout_seconds"},
		{"payload ilegible", map[string]any{"payload": `{"a":`}, "VALIDATION_ERROR", "payload"},
		{"payload con NUL", map[string]any{"payload": `{"a":"\u0000"}`}, "VALIDATION_ERROR", "payload"},
		{"payload de mas", map[string]any{"payload": `"` + long(65535) + `"`}, "VALIDATION_ERROR", "payload"},
	}
	for _, tc := range cases {
		rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", jobBody(t, tc.fields), uuid.NewString())
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, se esperaba 422", tc.name, rec.Code, rec.Body.String())
			continue
		}
		if code, field := errorOf(t, rec); code != tc.code || field != tc.field {
			t.Errorf("%s: %s en %q, se esperaba %s en %q (%s)", tc.name, code, field, tc.code, tc.field, rec.Body.String())
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
		jobBody(t, map[string]any{"code": absent, "name": long(256)}), uuid.NewString())
	if code, field := errorOf(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || field != "name" {
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

func TestCodigoRepetidoEs409ConElCampo(t *testing.T) {
	schedules := &plannedSchedules{}
	srv := newJobServer(t, takenCode{&storedJobs{schedules: schedules}}, schedules)
	rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", jobBody(t, nil), uuid.NewString())
	if code, field := errorOf(t, rec); rec.Code != http.StatusConflict || code != "CONFLICT" || field != "code" {
		t.Fatalf("codigo repetido: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnaTareaInvalidaNombraSuCampo(t *testing.T) {
	srv, _, _ := timezoneServer(t)
	const trigger = "2026-09-14T10:00:00Z"
	for body, field := range map[string]string{
		`{}`: "name",
		`{"name":"` + long(256) + `","trigger_at":"` + trigger + `","handler":"reports.daily"}`:                        "name",
		`{"name":"Aviso","trigger_at":"manana","handler":"reports.daily"}`:                                             "trigger_at",
		`{"name":"Aviso","handler":"reports.daily"}`:                                                                   "trigger_at",
		`{"name":"Aviso","trigger_at":"` + trigger + `"}`:                                                              "handler",
		`{"name":"Aviso","trigger_at":"` + trigger + `","handler":"` + long(256) + `"}`:                                "handler",
		`{"name":"Aviso","trigger_at":"` + trigger + `","handler":"reports.daily","description":"` + long(2001) + `"}`: "description",
		`{"name":"Aviso","trigger_at":"` + trigger + `","handler":"reports.daily","payload":"{"}`:                      "payload",
	} {
		rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/tasks", body, uuid.NewString())
		if code, got := errorOf(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || got != field {
			t.Errorf("tarea %.60s: %d %s, se esperaba el campo %s", body, rec.Code, rec.Body.String(), field)
		}
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

func TestElCierreDelEjecutorNombraSuCampo(t *testing.T) {
	f := newInternalFixture(t, domain.StatusRunning)
	for action, cases := range map[string]map[string]string{
		"fail":     {`{"error":"x"}`: "retryable", `{"error":"  ","retryable":false}`: "error"},
		"complete": {`{"result":{"a":"\u0000"}}`: "result"},
	} {
		for body, field := range cases {
			rec := f.post(t, action, body)
			if code, got := errorOf(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || got != field {
				t.Errorf("%s %s: %d %s, se esperaba el campo %s", action, body, rec.Code, rec.Body.String(), field)
			}
		}
	}
}
