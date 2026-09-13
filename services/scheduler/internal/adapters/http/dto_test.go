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
	job *domain.JobDefinition
}

func (r *jobRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.JobDefinition, error) {
	return r.job, nil
}

func (r *jobRepo) List(context.Context, *uuid.UUID, *bool) ([]*domain.JobDefinition, error) {
	return nil, nil
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

func contractServer() (http.Handler, *domain.JobDefinition, *domain.JobExecution, *domain.ScheduledTask) {
	now := time.Now().UTC()
	tenant := uuid.New()
	cron, interval, payload, result := "0 * * * *", 15, `{"k":"v"}`, "ok"
	duration := int64(1500)
	job := &domain.JobDefinition{
		ID: uuid.New(), TenantID: &tenant, Name: "Limpieza", Code: "cleanup", JobType: "cron",
		CronExpression: &cron, IntervalMinutes: &interval, Handler: "cleanup", Payload: &payload,
		IsActive: true, MaxRetries: 3, TimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now,
	}
	exec := &domain.JobExecution{
		ID: uuid.New(), JobID: job.ID, TenantID: &tenant, Status: "completed", StartedAt: &now,
		CompletedAt: &now, Duration: &duration, Result: &result, RetryCount: 1, CreatedAt: now,
	}
	task := &domain.ScheduledTask{
		ID: uuid.New(), TenantID: tenant, Name: "Aviso", TriggerAt: now, Handler: "notify",
		Payload: &payload, Status: "scheduled", CreatedAt: now,
	}
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{
		Jobs: &jobRepo{job: job}, Executions: &execRepo{exec: exec}, Tasks: &taskRepo{task: task}, Logger: zap.NewNop(),
	})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(uc, authz.NewChecker(unreachable, "")).Routes())
	return r, job, exec, task
}

func getData(t *testing.T, srv http.Handler, path string) json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-Tenant-ID", uuid.NewString())
	req.Header.Set("X-User-Roles", middleware.RoleTenantAdmin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
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
	srv, job, exec, task := contractServer()

	cases := []struct {
		path string
		want string
		id   uuid.UUID
	}{
		{"/api/v1/scheduler/jobs/" + job.ID.String(),
			"code,created_at,cron_expression,description,handler,id,interval_minutes,is_active,job_type,max_retries,name,payload,tenant_id,timeout_seconds,updated_at",
			job.ID},
		{"/api/v1/scheduler/executions/" + exec.ID.String(),
			"completed_at,created_at,duration_ms,error_message,id,job_id,result,retry_count,started_at,status,tenant_id",
			exec.ID},
		{"/api/v1/scheduler/tasks/" + task.ID.String(),
			"created_at,description,executed_at,handler,id,name,payload,status,tenant_id,trigger_at",
			task.ID},
	}
	for _, tc := range cases {
		obj, got := keysOf(t, getData(t, srv, tc.path))
		if got != tc.want {
			t.Errorf("GET %s\n  claves: %s\n  espera: %s", tc.path, got, tc.want)
		}
		if string(obj["id"]) != `"`+tc.id.String()+`"` {
			t.Errorf("GET %s: id %s", tc.path, obj["id"])
		}
	}

	obj, _ := keysOf(t, getData(t, srv, "/api/v1/scheduler/executions/"+exec.ID.String()))
	if string(obj["duration_ms"]) != "1500" {
		t.Errorf("duration_ms: %s", obj["duration_ms"])
	}
}

func TestListadoVacioEsArray(t *testing.T) {
	srv, _, _, _ := contractServer()
	if data := getData(t, srv, "/api/v1/scheduler/jobs"); string(data) != "[]" {
		t.Fatalf("listado vacio: %s, se esperaba []", data)
	}
}
