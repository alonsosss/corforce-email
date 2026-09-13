package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

type oneJob struct {
	ports.JobDefinitionRepository
	job *domain.JobDefinition
}

func (r *oneJob) GetByID(_ context.Context, id, _ uuid.UUID) (*domain.JobDefinition, error) {
	if id != r.job.ID {
		return nil, domain.ErrJobNotFound
	}
	c := *r.job
	return &c, nil
}

func (r *oneJob) GetByCode(context.Context, string) (*domain.JobDefinition, error) {
	return nil, domain.ErrJobNotFound
}

type oneExec struct {
	ports.JobExecutionRepository
	exec    *domain.JobExecution
	created int
}

func (r *oneExec) GetForUpdate(_ context.Context, id, _ uuid.UUID) (*domain.JobExecution, error) {
	if id != r.exec.ID {
		return nil, domain.ErrExecutionNotFound
	}
	c := *r.exec
	return &c, nil
}

func (r *oneExec) Update(_ context.Context, e *domain.JobExecution) error {
	c := *e
	r.exec = &c
	return nil
}

func (r *oneExec) Create(context.Context, *domain.JobExecution) error {
	r.created++
	return nil
}

type passTx struct{}

func (passTx) Transact(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type countEvents struct{ completed, failed int }

func (c *countEvents) JobStarted(context.Context, *domain.JobDefinition, *domain.JobExecution, int) error {
	return nil
}

func (c *countEvents) JobCompleted(context.Context, *domain.JobDefinition, *domain.JobExecution) error {
	c.completed++
	return nil
}

func (c *countEvents) JobFailed(context.Context, *domain.JobDefinition, *domain.JobExecution, *domain.JobExecution) error {
	c.failed++
	return nil
}

type internalFixture struct {
	srv    http.Handler
	execs  *oneExec
	events *countEvents
	id     uuid.UUID
	jobID  uuid.UUID
	tenant string
}

func newInternalFixture(t *testing.T, status string) internalFixture {
	t.Helper()
	tenant := uuid.New()
	now := time.Now().UTC()
	job := &domain.JobDefinition{ID: uuid.New(), TenantID: &tenant, Handler: "reports.daily", IsActive: true, MaxRetries: 1,
		Timezone: domain.DefaultTimezone}
	exec := &domain.JobExecution{ID: uuid.New(), JobID: job.ID, TenantID: &tenant, Status: status, StartedAt: &now, CreatedAt: now}
	catalog, err := domain.NewHandlerCatalog([]domain.HandlerSpec{{
		Name: "reports.daily", Service: "reports", MaxTimeoutSeconds: 600, Scopes: []domain.HandlerScope{domain.ScopeTenant},
	}})
	if err != nil {
		t.Fatal(err)
	}
	execs, events := &oneExec{exec: exec}, &countEvents{}
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{
		Jobs: &oneJob{job: job}, Executions: execs, Events: events, Tx: passTx{}, Catalog: catalog,
		Retry: domain.RetryPolicy{BaseDelay: time.Minute, MaxDelay: time.Hour}, Logger: zap.NewNop(),
	})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(Deps{UC: uc, Perms: authz.NewChecker(unreachable, "")}).Routes())
	return internalFixture{srv: r, execs: execs, events: events, id: exec.ID, jobID: job.ID, tenant: tenant.String()}
}

func (f internalFixture) post(t *testing.T, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.postTo(t, "/internal/scheduler/executions/"+f.id.String()+"/"+action, f.tenant, body)
}

func (f internalFixture) postTo(t *testing.T, path, tenant, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func dataOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible: %s", rec.Body.String())
	}
	return env.Data
}

func TestCompletarDosVecesRespondeLoMismo(t *testing.T) {
	f := newInternalFixture(t, domain.StatusRunning)
	first := f.post(t, "complete", `{"result":{"rows":3}}`)
	if first.Code != http.StatusOK {
		t.Fatalf("primer cierre: %d %s", first.Code, first.Body.String())
	}
	second := f.post(t, "complete", ``)
	if second.Code != http.StatusOK {
		t.Fatalf("segundo cierre: %d %s", second.Code, second.Body.String())
	}
	a, b := dataOf(t, first), dataOf(t, second)
	if string(a["status"]) != `"completed"` || string(a["completed_at"]) != string(b["completed_at"]) || string(b["result"]) != `"{\"rows\":3}"` {
		t.Fatalf("primer %v, segundo %v", a, b)
	}
	if f.events.completed != 1 {
		t.Fatalf("un solo evento de exito, hubo %d", f.events.completed)
	}
}

func TestCompletarUnaFallidaEsConflicto(t *testing.T) {
	f := newInternalFixture(t, domain.StatusFailed)
	if rec := f.post(t, "complete", `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("completar una fallida: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.post(t, "fail", `{"error":"otra vez","retryable":true}`); rec.Code != http.StatusOK {
		t.Fatalf("fallar una fallida responde lo mismo: %d", rec.Code)
	}
	if f.events.failed != 0 || f.execs.created != 0 {
		t.Fatal("repetir un fallo no publica ni reintenta")
	}
}

func TestFallarReintentableProgramaElSiguienteIntento(t *testing.T) {
	f := newInternalFixture(t, domain.StatusRunning)
	rec := f.post(t, "fail", `{"error":"proveedor caido","retryable":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallar: %d %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, rec)
	if string(data["status"]) != `"failed"` || string(data["failure_reason"]) != `"executor"` || string(data["error_message"]) != `"proveedor caido"` {
		t.Fatalf("ejecucion: %v", data)
	}
	if f.events.failed != 1 || f.execs.created != 1 {
		t.Fatalf("evento %d, reintentos %d", f.events.failed, f.execs.created)
	}
}

func TestLasRutasInternasValidanLaPeticion(t *testing.T) {
	f := newInternalFixture(t, domain.StatusRunning)
	base := "/internal/scheduler/executions/"
	nul := `{"result":{"a":"` + string([]byte{'\\', 'u', '0', '0', '0', '0'}) + `"}}`
	cases := []struct {
		name, path, tenant, body string
		want                     int
	}{
		{"fallo sin retryable", base + f.id.String() + "/fail", f.tenant, `{"error":"x"}`, http.StatusUnprocessableEntity},
		{"fallo sin motivo", base + f.id.String() + "/fail", f.tenant, `{"error":"  ","retryable":false}`, http.StatusUnprocessableEntity},
		{"campo desconocido", base + f.id.String() + "/fail", f.tenant, `{"error":"x","retryable":true,"extra":1}`, http.StatusBadRequest},
		{"resultado con NUL", base + f.id.String() + "/complete", f.tenant, nul, http.StatusUnprocessableEntity},
		{"id invalido", base + "no-es-uuid/complete", f.tenant, `{}`, http.StatusBadRequest},
		{"sin empresa", base + f.id.String() + "/complete", "", `{}`, http.StatusUnauthorized},
		{"ejecucion desconocida", base + uuid.NewString() + "/complete", f.tenant, `{}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		if rec := f.postTo(t, tc.path, tc.tenant, tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d, se esperaba %d (%s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
	if f.events.completed+f.events.failed != 0 {
		t.Fatal("una peticion rechazada no cierra nada")
	}
}

func (f internalFixture) api(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-Tenant-ID", f.tenant)
	req.Header.Set("X-User-Roles", middleware.RoleTenantAdmin)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func TestCrearUnTrabajoConManejadorDesconocidoEs422(t *testing.T) {
	f := newInternalFixture(t, domain.StatusRunning)
	rec := f.api(t, http.MethodPost, "/api/v1/scheduler/jobs",
		`{"name":"n","code":"c","job_type":"cron","cron_expression":"0 3 * * *","handler":"no.existe"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "handler not allowed") {
		t.Fatalf("manejador desconocido: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnCronConExpresionInvalidaEs422(t *testing.T) {
	f := newInternalFixture(t, domain.StatusRunning)
	for name, expr := range map[string]string{
		"sin expresion":  ``,
		"vacia":          `,"cron_expression":"  "`,
		"hora 25":        `,"cron_expression":"0 25 * * *"`,
		"@every corto":   `,"cron_expression":"@every 10s"`,
		"con zona":       `,"cron_expression":"CRON_TZ=America/Lima 0 9 * * *"`,
		"nunca ocurre":   `,"cron_expression":"0 0 30 2 *"`,
		"cuatro campos":  `,"cron_expression":"0 3 * *"`,
		"no admitido":    `,"cron_expression":"@yearly"`,
		"seis campos":    `,"cron_expression":"0 0 3 * * *"`,
		"texto cualquie": `,"cron_expression":"todos los dias"`,
	} {
		body := `{"name":"n","job_type":"cron","handler":"reports.daily"` + expr + `}`
		create := f.api(t, http.MethodPost, "/api/v1/scheduler/jobs", strings.Replace(body, `{`, `{"code":"c",`, 1))
		update := f.api(t, http.MethodPut, "/api/v1/scheduler/jobs/"+f.jobID.String(), body)
		for op, rec := range map[string]*httptest.ResponseRecorder{"crear": create, "editar": update} {
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.ErrInvalidCron.Error()) {
				t.Errorf("%s con %s: %d %s", op, name, rec.Code, rec.Body.String())
			}
		}
	}
}
