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

// storedJobs guarda un solo trabajo: el que se crea o se edita por el API.
type storedJobs struct {
	ports.JobDefinitionRepository
	job *domain.JobDefinition
}

func (r *storedJobs) GetByCode(context.Context, string) (*domain.JobDefinition, error) {
	return nil, domain.ErrJobNotFound
}

func (r *storedJobs) GetByID(_ context.Context, id, _ uuid.UUID) (*domain.JobDefinition, error) {
	if r.job == nil || r.job.ID != id {
		return nil, domain.ErrJobNotFound
	}
	c := *r.job
	return &c, nil
}

func (r *storedJobs) Create(_ context.Context, j *domain.JobDefinition) error {
	c := *j
	r.job = &c
	return nil
}

func (r *storedJobs) Update(ctx context.Context, j *domain.JobDefinition) error {
	return r.Create(ctx, j)
}

type plannedSchedules struct {
	ports.JobScheduleRepository
	next time.Time
}

func (s *plannedSchedules) SetNextRun(_ context.Context, _ uuid.UUID, next time.Time) error {
	s.next = next
	return nil
}

type inlineTx struct{}

func (inlineTx) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func timezoneServer(t *testing.T) (http.Handler, *storedJobs, *plannedSchedules) {
	t.Helper()
	catalog, err := domain.NewHandlerCatalog([]domain.HandlerSpec{{
		Name: "reports.daily", Service: "reports", MaxTimeoutSeconds: 600, Scopes: []domain.HandlerScope{domain.ScopeTenant},
	}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, schedules := &storedJobs{}, &plannedSchedules{}
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{
		Jobs: jobs, Schedules: schedules, Tx: inlineTx{}, Catalog: catalog,
		Now:    func() time.Time { return time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC) },
		Logger: zap.NewNop(),
	})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(Deps{UC: uc, Perms: authz.NewChecker(unreachable, "")}).Routes())
	return r, jobs, schedules
}

type envelope struct {
	Data  map[string]json.RawMessage `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func send(t *testing.T, srv http.Handler, method, path, body string) (int, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", uuid.NewString())
	req.Header.Set("X-Tenant-ID", uuid.NewString())
	req.Header.Set("X-User-Roles", middleware.RoleTenantAdmin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s %s: cuerpo ilegible %q", method, path, rec.Body.String())
	}
	return rec.Code, env
}

const jobFields = `"name":"Informe","job_type":"cron","cron_expression":"0 8 * * *","handler":"reports.daily"`

// createBody y updateBody son el cuerpo de un cron diario a las 08:00 con timezone, que es
// el trozo JSON que se anade (vacio para omitir el campo).
func createBody(timezone string) string { return `{"code":"informe",` + jobFields + timezone + `}` }

func updateBody(timezone string) string { return `{` + jobFields + timezone + `}` }

func TestCrearUnTrabajoConZona(t *testing.T) {
	srv, jobs, schedules := timezoneServer(t)
	code, env := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", createBody(`,"timezone":"America/Lima"`))
	if code != http.StatusCreated || string(env.Data["timezone"]) != `"America/Lima"` {
		t.Fatalf("crear en Lima: %d %s", code, env.Data["timezone"])
	}
	if jobs.job.Timezone != "America/Lima" || !schedules.next.Equal(time.Date(2026, 9, 13, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("guardado %q, planificado %v", jobs.job.Timezone, schedules.next)
	}

	for _, body := range []string{createBody(""), createBody(`,"timezone":null`)} {
		code, env = send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", body)
		if code != http.StatusCreated || string(env.Data["timezone"]) != `"UTC"` {
			t.Fatalf("sin zona es UTC (%s): %d %s", body, code, env.Data["timezone"])
		}
	}
}

func TestZonaInvalidaResponde422ConSuCodigo(t *testing.T) {
	srv, jobs, _ := timezoneServer(t)
	for _, tz := range []string{`""`, `"+05:00"`, `"UTC-5"`, `"Local"`, `"America/Nowhere"`, `" America/Lima"`} {
		code, env := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", createBody(`,"timezone":`+tz))
		if code != http.StatusUnprocessableEntity || env.Error == nil || env.Error.Code != "INVALID_TIMEZONE" {
			t.Errorf("crear con %s: %d %+v", tz, code, env.Error)
		}
	}
	if jobs.job != nil {
		t.Fatal("un trabajo con una zona invalida no se guarda")
	}
}

func TestEditarConservaOCambiaLaZona(t *testing.T) {
	srv, jobs, schedules := timezoneServer(t)
	if code, _ := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs", createBody(`,"timezone":"America/Lima"`)); code != http.StatusCreated {
		t.Fatalf("crear: %d", code)
	}
	path := "/api/v1/scheduler/jobs/" + jobs.job.ID.String()

	code, env := send(t, srv, http.MethodPut, path, updateBody(""))
	if code != http.StatusOK || string(env.Data["timezone"]) != `"America/Lima"` {
		t.Fatalf("editar sin zona la conserva: %d %s", code, env.Data["timezone"])
	}

	code, _ = send(t, srv, http.MethodPut, path, updateBody(`,"timezone":"Europe/Berlin"`))
	if code != http.StatusOK || jobs.job.Timezone != "Europe/Berlin" || !schedules.next.Equal(time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("cambiar la zona replanifica: %d %q %v", code, jobs.job.Timezone, schedules.next)
	}

	code, env = send(t, srv, http.MethodPut, path, updateBody(`,"timezone":""`))
	if code != http.StatusUnprocessableEntity || env.Error == nil || env.Error.Code != "INVALID_TIMEZONE" || jobs.job.Timezone != "Europe/Berlin" {
		t.Fatalf("editar con zona vacia: %d %+v, guardada %q", code, env.Error, jobs.job.Timezone)
	}
}

func TestContratoDeLaMeta(t *testing.T) {
	srv, _, _ := timezoneServer(t)
	obj, got := keysOf(t, getData(t, srv, "/api/v1/scheduler/meta"))
	if got != "cron,timezone" {
		t.Fatalf("claves: %s", got)
	}
	tz, got := keysOf(t, obj["timezone"])
	if got != "default,format,max_length,pattern" {
		t.Fatalf("claves de timezone: %s", got)
	}
	if string(tz["default"]) != `"UTC"` || string(tz["format"]) != `"iana"` || string(tz["max_length"]) != "64" {
		t.Fatalf("timezone: %v", tz)
	}
	cron, got := keysOf(t, obj["cron"])
	if got != "descriptors,max_length,min_every_seconds" {
		t.Fatalf("claves de cron: %s", got)
	}
	if string(cron["descriptors"]) != `["@daily","@hourly","@monthly","@weekly"]` || string(cron["min_every_seconds"]) != "60" {
		t.Fatalf("cron: %v", cron)
	}
}
