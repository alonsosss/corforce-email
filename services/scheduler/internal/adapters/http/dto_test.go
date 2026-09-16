package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type jobRepo struct {
	ports.JobDefinitionRepository
	job      *domain.JobDefinition
	overview *domain.JobOverview
	page     []*domain.JobOverview
	total    int64
	// filter es el ultimo filtro que llego al listado.
	filter domain.JobFilter
}

func (r *jobRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.JobDefinition, error) {
	return r.job, nil
}

func (r *jobRepo) GetOverview(context.Context, uuid.UUID, uuid.UUID) (*domain.JobOverview, error) {
	return r.overview, nil
}

func (r *jobRepo) List(_ context.Context, f domain.JobFilter) ([]*domain.JobOverview, int64, error) {
	r.filter = f
	return r.page, r.total, nil
}

type execRepo struct {
	ports.JobExecutionRepository
	exec *domain.JobExecution
}

func (r *execRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.JobExecution, error) {
	return r.exec, nil
}

type taskRepo struct {
	ports.ScheduledTaskRepository
	task *domain.ScheduledTask
}

func (r *taskRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.ScheduledTask, error) {
	return r.task, nil
}

type contract struct {
	srv  http.Handler
	jobs *jobRepo
	job  *domain.JobDefinition
	exec *domain.JobExecution
	task *domain.ScheduledTask
}

// contractServer sirve un trabajo con su calendario (proxima a las 11:00, ultima pasada a
// las 09:00) y su ultima ejecucion, fallida por timeout a las 10:00 UTC.
func contractServer() contract {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	tenant := uuid.New()
	cron, interval, payload, result := "0 * * * *", 15, `{"k":"v"}`, "ok"
	duration := int64(1500)
	job := &domain.JobDefinition{
		ID: uuid.New(), TenantID: &tenant, Name: "Limpieza", Code: "cleanup", JobType: "cron",
		CronExpression: &cron, Timezone: "America/Lima", IntervalMinutes: &interval, Handler: "cleanup", Payload: &payload,
		IsActive: true, MaxRetries: 3, TimeoutSeconds: 60, Version: 3, CreatedAt: now, UpdatedAt: now,
	}
	exec := &domain.JobExecution{
		ID: uuid.New(), JobID: job.ID, TenantID: &tenant, Status: "completed", StartedAt: &now,
		CompletedAt: &now, Duration: &duration, Result: &result, RetryCount: 1, CreatedAt: now,
	}
	task := &domain.ScheduledTask{
		ID: uuid.New(), TenantID: tenant, Name: "Aviso", TriggerAt: now, Handler: "notify",
		Payload: &payload, Status: "scheduled", CreatedAt: now,
	}
	next, last, reason := now.Add(time.Hour), now.Add(-time.Hour), domain.FailureTimeout
	jobs := &jobRepo{job: job, overview: domain.NewJobOverview(*job, &next, &last, &domain.ExecutionSummary{
		ID: exec.ID, Status: domain.StatusFailed, CompletedAt: &now, FailureReason: &reason,
	})}
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{
		Jobs: jobs, Executions: &execRepo{exec: exec}, Tasks: &taskRepo{task: task}, Logger: zap.NewNop(),
	})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(Deps{UC: uc, Perms: authz.NewChecker(unreachable, "")}).Routes())
	return contract{srv: r, jobs: jobs, job: job, exec: exec, task: task}
}

func request(t *testing.T, srv http.Handler, method, path, body, tenant string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-Tenant-ID", tenant)
	req.Header.Set("X-User-Roles", middleware.RoleTenantAdmin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func getData(t *testing.T, srv http.Handler, path string) json.RawMessage {
	t.Helper()
	rec := request(t, srv, http.MethodGet, path, "", uuid.NewString())
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: cuerpo ilegible %q", path, rec.Body.String())
	}
	return env.Data
}

func keysOf(t *testing.T, data json.RawMessage) (map[string]json.RawMessage, string) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("se esperaba un objeto: %s", data)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return obj, strings.Join(keys, ",")
}

func TestContratoJSONEnSnakeCase(t *testing.T) {
	c := contractServer()

	cases := []struct {
		path string
		want string
		id   uuid.UUID
	}{
		{"/api/v1/scheduler/jobs/" + c.job.ID.String(),
			"already_run,code,created_at,cron_expression,description,handler,id,interval_minutes,is_active,job_type,last_execution,last_run_at,max_retries,name,next_run_at,payload,tenant_id,timeout_seconds,timezone,updated_at,version",
			c.job.ID},
		{"/api/v1/scheduler/executions/" + c.exec.ID.String(),
			"completed_at,created_at,deadline_at,duration_ms,error_message,failure_reason,id,job_id,next_attempt_at,result,retry_count,retry_of,started_at,status,tenant_id",
			c.exec.ID},
		{"/api/v1/scheduler/tasks/" + c.task.ID.String(),
			"created_at,description,executed_at,handler,id,name,payload,status,tenant_id,trigger_at",
			c.task.ID},
	}
	for _, tc := range cases {
		obj, got := keysOf(t, getData(t, c.srv, tc.path))
		if got != tc.want {
			t.Errorf("GET %s\n  claves: %s\n  espera: %s", tc.path, got, tc.want)
		}
		if string(obj["id"]) != `"`+tc.id.String()+`"` {
			t.Errorf("GET %s: id %s", tc.path, obj["id"])
		}
	}

	obj, _ := keysOf(t, getData(t, c.srv, "/api/v1/scheduler/executions/"+c.exec.ID.String()))
	if string(obj["duration_ms"]) != "1500" {
		t.Errorf("duration_ms: %s", obj["duration_ms"])
	}
	obj, _ = keysOf(t, getData(t, c.srv, "/api/v1/scheduler/jobs/"+c.job.ID.String()))
	if string(obj["timezone"]) != `"America/Lima"` {
		t.Errorf("timezone: %s", obj["timezone"])
	}
	if string(obj["version"]) != "3" {
		t.Errorf("version: %s", obj["version"])
	}
}

func TestContratoDelCalendarioYLaUltimaEjecucion(t *testing.T) {
	c := contractServer()
	path := "/api/v1/scheduler/jobs/" + c.job.ID.String()
	obj, _ := keysOf(t, getData(t, c.srv, path))
	if string(obj["next_run_at"]) != `"2026-09-13T11:00:00Z"` || string(obj["last_run_at"]) != `"2026-09-13T09:00:00Z"` {
		t.Fatalf("calendario: next_run_at %s, last_run_at %s", obj["next_run_at"], obj["last_run_at"])
	}
	last, got := keysOf(t, obj["last_execution"])
	if got != "completed_at,failure_reason,id,status" {
		t.Fatalf("claves de last_execution: %s", got)
	}
	if string(last["id"]) != `"`+c.exec.ID.String()+`"` || string(last["status"]) != `"failed"` ||
		string(last["completed_at"]) != `"2026-09-13T10:00:00Z"` || string(last["failure_reason"]) != `"timeout"` {
		t.Fatalf("last_execution: %s", obj["last_execution"])
	}

	// Inactivo y sin ejecuciones: las claves siguen ahi, a null; la hora guardada del
	// calendario no se presenta como una ejecucion prevista.
	next := time.Date(2026, 9, 13, 11, 0, 0, 0, time.UTC)
	c.job.IsActive = false
	c.jobs.overview = domain.NewJobOverview(*c.job, &next, nil, nil)
	obj, _ = keysOf(t, getData(t, c.srv, path))
	for _, key := range []string{"next_run_at", "last_run_at", "last_execution"} {
		if string(obj[key]) != "null" {
			t.Errorf("%s: %s, se esperaba null", key, obj[key])
		}
	}
}

type pageEnvelope struct {
	Data []json.RawMessage `json:"data"`
	Meta map[string]int64  `json:"meta"`
}

func listPage(t *testing.T, srv http.Handler, query, tenant string) pageEnvelope {
	t.Helper()
	rec := request(t, srv, http.MethodGet, "/api/v1/scheduler/jobs"+query, "", tenant)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /jobs%s: %d %s", query, rec.Code, rec.Body.String())
	}
	var env pageEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible %q", rec.Body.String())
	}
	return env
}

func TestListadoDeTrabajosPaginado(t *testing.T) {
	c := contractServer()
	c.jobs.page, c.jobs.total = []*domain.JobOverview{c.jobs.overview}, 3
	tenant := uuid.New()

	env := listPage(t, c.srv, "?page=2&per_page=1&is_active=false&tenant_id="+uuid.NewString(), tenant.String())
	if len(env.Data) != 1 {
		t.Fatalf("filas: %d", len(env.Data))
	}
	if env.Meta["page"] != 2 || env.Meta["per_page"] != 1 || env.Meta["total"] != 3 || env.Meta["total_pages"] != 3 {
		t.Fatalf("meta: %v", env.Meta)
	}
	// La empresa es la del token: tenant_id en la query no elige otra.
	f := c.jobs.filter
	if f.TenantID != tenant || f.IsActive == nil || *f.IsActive || f.Page != 2 || f.PerPage != 1 {
		t.Fatalf("filtro: %+v", f)
	}
	if _, got := keysOf(t, env.Data[0]); !strings.Contains(got, "next_run_at") || !strings.Contains(got, "last_execution") {
		t.Fatalf("una fila del listado es un trabajo completo: %s", got)
	}

	env = listPage(t, c.srv, "?per_page=500", tenant.String())
	if f := c.jobs.filter; f.PerPage != 100 || f.Page != 1 || f.IsActive != nil || env.Meta["per_page"] != 100 {
		t.Fatalf("per_page de mas se recorta al maximo: %+v, meta %v", f, env.Meta)
	}

	rec := request(t, c.srv, http.MethodGet, "/api/v1/scheduler/jobs?is_active=quizas", "", tenant.String())
	if code, field := errorOf(t, rec); rec.Code != http.StatusUnprocessableEntity || code != "VALIDATION_ERROR" || field != "is_active" {
		t.Fatalf("is_active ilegible: %d %s", rec.Code, rec.Body.String())
	}
}

func TestListadoVacioEsArray(t *testing.T) {
	c := contractServer()
	for _, path := range []string{"/api/v1/scheduler/jobs", "/api/v1/scheduler/handlers"} {
		if data := getData(t, c.srv, path); string(data) != "[]" {
			t.Fatalf("listado vacio en %s: %s, se esperaba []", path, data)
		}
	}
}

func TestContratoDelCatalogoDeManejadores(t *testing.T) {
	catalog, err := domain.NewHandlerCatalog([]domain.HandlerSpec{{
		Name: "reports.daily", Service: "reports", Description: "Informe diario", MaxTimeoutSeconds: 600,
		Scopes: []domain.HandlerScope{domain.ScopeTenant},
	}})
	if err != nil {
		t.Fatal(err)
	}
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{Catalog: catalog, Logger: zap.NewNop()})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(Deps{UC: uc, Perms: authz.NewChecker(unreachable, "")}).Routes())

	var list []json.RawMessage
	if err := json.Unmarshal(getData(t, r, "/api/v1/scheduler/handlers"), &list); err != nil || len(list) != 1 {
		t.Fatalf("catalogo: %v %d", err, len(list))
	}
	obj, got := keysOf(t, list[0])
	if got != "description,max_timeout_seconds,name,scopes,service" {
		t.Fatalf("claves: %s", got)
	}
	if string(obj["scopes"]) != `["tenant"]` || string(obj["max_timeout_seconds"]) != "600" {
		t.Fatalf("manejador: %v", obj)
	}
}
