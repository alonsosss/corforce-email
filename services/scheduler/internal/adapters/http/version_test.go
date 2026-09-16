package http

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Contrato de la concurrencia optimista del PUT: el trabajo lleva su version, la edicion la
// exige (428 VERSION_REQUIRED sin ella o con null), la sube en uno y, con una anterior,
// responde 409 VERSION_CONFLICT sin escribir nada. Una version imposible es un 422 que nombra
// el campo y una ilegible, un 400.
func TestElPUTExigeLaVersionLeida(t *testing.T) {
	srv, jobs, schedules := timezoneServer(t)
	code, env := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", createBody(`,"timezone":"America/Lima"`))
	if code != http.StatusCreated || string(env.Data["version"]) != "1" {
		t.Fatalf("el alta nace en la version 1: %d %s", code, env.Data["version"])
	}
	path := "/api/v1/scheduler/jobs/" + jobs.job.ID.String()
	if obj, _ := keysOf(t, getData(t, srv, path)); string(obj["version"]) != "1" {
		t.Fatalf("GET: version %s", obj["version"])
	}

	for _, body := range []string{`{` + jobFields + `,"timezone":"Europe/Berlin"}`, `{"version":null,` + jobFields + `,"timezone":"Europe/Berlin"}`} {
		code, env = send(t, srv, http.MethodPut, path, body)
		if code != http.StatusPreconditionRequired || env.Error == nil || env.Error.Code != codeVersionRequired {
			t.Fatalf("sin version (%s): %d %+v", body, code, env.Error)
		}
	}
	if jobs.job.Timezone != "America/Lima" || jobs.job.Version != 1 {
		t.Fatalf("sin version no se escribe: %q, version %d", jobs.job.Timezone, jobs.job.Version)
	}

	code, env = send(t, srv, http.MethodPut, path, updateBody(1, `,"timezone":"Europe/Berlin"`))
	if code != http.StatusOK || string(env.Data["version"]) != "2" || jobs.job.Timezone != "Europe/Berlin" {
		t.Fatalf("con la version leida: %d, version %s, zona %q", code, env.Data["version"], jobs.job.Timezone)
	}
	next := schedules.next

	// Otro administrador leyo la version 1 y no manda la zona: el servicio no la toma de la
	// fila, rechaza la edicion entera.
	code, env = send(t, srv, http.MethodPut, path,
		`{"version":1,"name":"Otro nombre","job_type":"cron","cron_expression":"0 8 * * *","handler":"reports.daily"}`)
	if code != http.StatusConflict || env.Error == nil || env.Error.Code != codeVersionConflict || env.Error.Details != nil {
		t.Fatalf("con una version anterior: %d %+v", code, env.Error)
	}
	if jobs.job.Name != "Informe" || jobs.job.Timezone != "Europe/Berlin" || jobs.job.Version != 2 || !schedules.next.Equal(next) {
		t.Fatalf("el 409 no escribe: %+v, planificado %v", jobs.job, schedules.next)
	}

	for _, v := range []string{"0", "-3"} {
		code, env = send(t, srv, http.MethodPut, path, `{"version":`+v+`,`+jobFields+`}`)
		if code != http.StatusUnprocessableEntity || env.Error == nil || env.Error.Code != codeValidation ||
			env.Error.Details["field"] != "version" || env.Error.Details["rule"] != "out_of_range" {
			t.Errorf("version %s: %d %+v", v, code, env.Error)
		}
	}
	for _, v := range []string{`"2"`, "2.5", "9223372036854775808"} {
		if code, _ = send(t, srv, http.MethodPut, path, `{"version":`+v+`,`+jobFields+`}`); code != http.StatusBadRequest {
			t.Errorf("version %s: %d", v, code)
		}
	}
	if jobs.job.Version != 2 {
		t.Fatalf("ningun rechazo sube la version: %d", jobs.job.Version)
	}
}

// Activar y desactivar no llevan version ni la cambian: fijan un estado que el PUT no
// escribe, y una edicion leida antes sigue valiendo.
func TestActivarYDesactivarNoLlevanVersionNiLaCambian(t *testing.T) {
	srv, jobs, _ := timezoneServer(t)
	if code, _ := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", createBody("")); code != http.StatusCreated {
		t.Fatalf("crear: %d", code)
	}
	path := "/api/v1/scheduler/jobs/" + jobs.job.ID.String()
	for _, action := range []string{"/disable", "/enable"} {
		if rec := request(t, srv, http.MethodPost, path+action, "", uuid.NewString()); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", action, rec.Code, rec.Body.String())
		}
	}
	if jobs.job.Version != 1 || !jobs.job.IsActive {
		t.Fatalf("version %d, activo %v", jobs.job.Version, jobs.job.IsActive)
	}
	if code, env := send(t, srv, http.MethodPut, path, updateBody(1, "")); code != http.StatusOK || string(env.Data["version"]) != "2" {
		t.Fatalf("la edicion leida antes: %d %s", code, env.Data["version"])
	}
}
