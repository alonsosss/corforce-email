package http

import (
	"net/http"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
)

func TestEditarElIntervaloDeUnTrabajoActivoLoReplanificaDesdeAhora(t *testing.T) {
	srv, jobs, schedules := timezoneServer(t)
	if code, _ := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs",
		`{"name":"Cada cuarto","code":"cuarto","job_type":"interval","interval_minutes":15,"handler":"reports.daily"}`); code != http.StatusCreated {
		t.Fatalf("crear: %d", code)
	}
	path := "/api/v1/scheduler/jobs/" + jobs.job.ID.String()

	code, env := send(t, srv, http.MethodPut, path, `{"name":"Cada hora","job_type":"interval","interval_minutes":60,"handler":"reports.daily"}`)
	if code != http.StatusOK || string(env.Data["next_run_at"]) != `"2026-09-13T11:00:00Z"` || !schedules.next.Equal(clockNow.Add(time.Hour)) {
		t.Fatalf("otros minutos replanifican desde ahora: %d, next_run_at %s, planificado %v", code, env.Data["next_run_at"], schedules.next)
	}

	schedules.next = clockNow.Add(20 * time.Minute)
	code, env = send(t, srv, http.MethodPut, path, `{"name":"Otro nombre","job_type":"interval","interval_minutes":60,"handler":"reports.daily","max_retries":2}`)
	if code != http.StatusOK || string(env.Data["next_run_at"]) != `"2026-09-13T10:20:00Z"` || !schedules.next.Equal(clockNow.Add(20*time.Minute)) {
		t.Fatalf("sin tocar el calendario se respeta la prevista: %d, next_run_at %s", code, env.Data["next_run_at"])
	}
}

// already_run sale de la regla con la que el servicio rechaza reactivar: con true, el enable
// responde 409 JOB_ALREADY_RUN.
func TestAlreadyRunEsLaReglaDelRechazoAlReactivar(t *testing.T) {
	srv, jobs, schedules := timezoneServer(t)
	code, env := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs",
		`{"name":"Una vez","code":"una-vez","job_type":"one_time","handler":"reports.daily"}`)
	if code != http.StatusCreated || string(env.Data["already_run"]) != "false" {
		t.Fatalf("recien creado no consta como despachado: %d %s", code, env.Data["already_run"])
	}
	path := "/api/v1/scheduler/jobs/" + jobs.job.ID.String()

	ran := clockNow.Add(-time.Hour)
	jobs.job.IsActive, schedules.last = false, &ran
	obj, _ := keysOf(t, getData(t, srv, path))
	if string(obj["already_run"]) != "true" || string(obj["last_run_at"]) != `"2026-09-13T09:00:00Z"` || string(obj["next_run_at"]) != "null" {
		t.Fatalf("despachado por el calendario: already_run %s, last_run_at %s", obj["already_run"], obj["last_run_at"])
	}
	code, env = send(t, srv, http.MethodPost, path+"/enable", "")
	if code != http.StatusConflict || env.Error == nil || env.Error.Code != codeJobAlreadyRun || jobs.job.IsActive {
		t.Fatalf("reactivarlo: %d %+v", code, env.Error)
	}

	// Un intervalo con la misma ultima pasada no consta como despachado y se reactiva.
	c := contractServer()
	c.job.IsActive = false
	c.jobs.overview = domain.NewJobOverview(*c.job, nil, &ran, nil)
	if obj, _ := keysOf(t, getData(t, c.srv, "/api/v1/scheduler/jobs/"+c.job.ID.String())); string(obj["already_run"]) != "false" {
		t.Fatalf("un cron no es un one_time despachado: %s", obj["already_run"])
	}
}
