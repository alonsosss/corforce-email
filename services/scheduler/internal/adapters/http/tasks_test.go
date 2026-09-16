package http

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/response"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/app"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/domain"
	"github.com/alonsosss/corforce-email/services/scheduler/internal/ports"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// clockNow es el reloj de estas pruebas: 2026-09-13 10:00 UTC.
var clockNow = time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)

// taskStore guarda tareas de varias empresas con las condiciones del SQL: toda lectura y
// escritura por id lleva la empresa. fail simula una base caida.
type taskStore struct {
	ports.ScheduledTaskRepository
	tasks  map[uuid.UUID]domain.ScheduledTask
	fail   error
	filter domain.TaskFilter
	writes int
}

func (s *taskStore) add(tenant uuid.UUID, status string, trigger time.Time) domain.ScheduledTask {
	task := domain.ScheduledTask{ID: uuid.New(), TenantID: tenant, Name: "Aviso " + trigger.Format(time.Kitchen),
		TriggerAt: trigger, Handler: "reports.daily", Status: status, CreatedAt: clockNow}
	s.tasks[task.ID] = task
	return task
}

func (s *taskStore) GetByID(_ context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	t, ok := s.tasks[id]
	if !ok || t.TenantID != tenantID {
		return nil, domain.ErrTaskNotFound
	}
	return &t, nil
}

func (s *taskStore) GetForUpdate(ctx context.Context, id, tenantID uuid.UUID) (*domain.ScheduledTask, error) {
	return s.GetByID(ctx, id, tenantID)
}

func (s *taskStore) Cancel(_ context.Context, id, tenantID uuid.UUID) error {
	t, ok := s.tasks[id]
	if !ok || t.TenantID != tenantID || t.Status != domain.TaskStatusScheduled {
		return domain.ErrTaskNotFound
	}
	t.Status = domain.TaskStatusCancelled
	s.tasks[id] = t
	s.writes++
	return nil
}

func (s *taskStore) ListPending(_ context.Context, f domain.TaskFilter) ([]*domain.ScheduledTask, int64, error) {
	s.filter = f
	var all []*domain.ScheduledTask
	for _, t := range s.tasks {
		if t.TenantID == f.TenantID && t.Status == domain.TaskStatusScheduled && !t.TriggerAt.After(f.Before) {
			c := t
			all = append(all, &c)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].TriggerAt.Before(all[j].TriggerAt) })
	offset := f.Offset()
	if offset >= int64(len(all)) {
		return nil, int64(len(all)), nil
	}
	return all[offset:min(int(offset)+f.PerPage, len(all))], int64(len(all)), nil
}

func taskServer(t *testing.T) (http.Handler, *taskStore) {
	t.Helper()
	store := &taskStore{tasks: map[uuid.UUID]domain.ScheduledTask{}}
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{
		Tasks: store, Tx: inlineTx{}, Now: func() time.Time { return clockNow }, Logger: zap.NewNop(),
	})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(Deps{UC: uc, Perms: authz.NewChecker(unreachable, "")}).Routes())
	return r, store
}

func TestUnaTareaDeOtraEmpresaEs404SinEfectos(t *testing.T) {
	srv, store := taskServer(t)
	me, other := uuid.New(), uuid.New()
	foreign := store.add(other, domain.TaskStatusScheduled, clockNow.Add(time.Hour))

	for name, id := range map[string]string{"de otra empresa": foreign.ID.String(), "inexistente": uuid.NewString()} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			path := "/api/v1/scheduler/tasks/" + id
			if method == http.MethodPost {
				path += "/cancel"
			}
			rec := request(t, srv, method, path, "", me.String())
			if code, _ := errorOf(t, rec); rec.Code != http.StatusNotFound || code != "NOT_FOUND" {
				t.Errorf("%s %s (%s): %d %s, se esperaba 404", method, path, name, rec.Code, rec.Body.String())
			}
		}
	}
	if store.writes != 0 || store.tasks[foreign.ID].Status != domain.TaskStatusScheduled {
		t.Fatalf("la tarea ajena no cambia: %s, escrituras %d", store.tasks[foreign.ID].Status, store.writes)
	}
	// La empresa del token manda: la duena si la cancela.
	if rec := request(t, srv, http.MethodPost, "/api/v1/scheduler/tasks/"+foreign.ID.String()+"/cancel", "", other.String()); rec.Code != http.StatusNoContent {
		t.Fatalf("la duena cancela su tarea: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCancelarUnaTareaResponde204O409(t *testing.T) {
	srv, store := taskServer(t)
	me := uuid.New()
	own := store.add(me, domain.TaskStatusScheduled, clockNow.Add(time.Hour))
	done := store.add(me, domain.TaskStatusExecuted, clockNow.Add(-time.Hour))
	path := func(task domain.ScheduledTask) string {
		return "/api/v1/scheduler/tasks/" + task.ID.String() + "/cancel"
	}

	for i := 0; i < 2; i++ {
		if rec := request(t, srv, http.MethodPost, path(own), "", me.String()); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("cancelar (vez %d): %d %q", i+1, rec.Code, rec.Body.String())
		}
	}
	if store.writes != 1 || store.tasks[own.ID].Status != domain.TaskStatusCancelled {
		t.Fatalf("cancelada una vez: %s, escrituras %d", store.tasks[own.ID].Status, store.writes)
	}
	rec := request(t, srv, http.MethodPost, path(done), "", me.String())
	if code, _ := errorOf(t, rec); rec.Code != http.StatusConflict || code != "CONFLICT" {
		t.Fatalf("cancelar una ejecutada: %d %s", rec.Code, rec.Body.String())
	}
	if store.tasks[done.ID].Status != domain.TaskStatusExecuted || store.writes != 1 {
		t.Fatal("la ejecutada no cambia")
	}
}

func TestLeerUnaTareaDistingueNoEncontradaDeUnFallo(t *testing.T) {
	srv, store := taskServer(t)
	core, logs := observer.New(zap.ErrorLevel)
	response.SetUnexpectedLogger(zap.New(core))
	t.Cleanup(func() { response.SetUnexpectedLogger(nil) })

	store.fail = errors.New("conexion con la base perdida")
	rec := request(t, srv, http.MethodGet, "/api/v1/scheduler/tasks/"+uuid.NewString(), "", uuid.NewString())
	if code, _ := errorOf(t, rec); rec.Code != http.StatusInternalServerError || code != "INTERNAL_ERROR" {
		t.Fatalf("fallo de la base: %d %s, se esperaba 500", rec.Code, rec.Body.String())
	}
	entries := logs.All()
	if len(entries) != 1 || entries[0].ContextMap()["error"] != store.fail.Error() {
		t.Fatalf("el 500 deja escrito el motivo: %+v", entries)
	}
	if rec.Body.String() == "" || json.Valid(rec.Body.Bytes()) && string(rec.Body.Bytes()) == store.fail.Error() {
		t.Fatal("el motivo no llega al cliente")
	}
}

type tasksEnvelope struct {
	Data []json.RawMessage          `json:"data"`
	Meta map[string]json.RawMessage `json:"meta"`
}

func listTasks(t *testing.T, srv http.Handler, query, tenant string) tasksEnvelope {
	t.Helper()
	rec := request(t, srv, http.MethodGet, "/api/v1/scheduler/tasks"+query, "", tenant)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tasks%s: %d %s", query, rec.Code, rec.Body.String())
	}
	var env tasksEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("cuerpo ilegible %q", rec.Body.String())
	}
	return env
}

func TestListadoDeTareasPaginadoConSuVentana(t *testing.T) {
	srv, store := taskServer(t)
	me := uuid.New()
	for i := 1; i <= 3; i++ {
		store.add(me, domain.TaskStatusScheduled, clockNow.Add(time.Duration(i)*time.Hour))
	}
	store.add(me, domain.TaskStatusScheduled, clockNow.Add(domain.PendingTasksWindow+time.Minute))
	store.add(uuid.New(), domain.TaskStatusScheduled, clockNow.Add(time.Hour))

	env := listTasks(t, srv, "?page=2&per_page=1&tenant_id="+uuid.NewString(), me.String())
	if len(env.Data) != 1 {
		t.Fatalf("filas: %d", len(env.Data))
	}
	_, keys := keysOf(t, mustJSON(t, env.Meta))
	if keys != "page,pending_window_seconds,per_page,total,total_pages" {
		t.Fatalf("claves de la meta: %s", keys)
	}
	for key, want := range map[string]string{"page": "2", "per_page": "1", "total": "3", "total_pages": "3", "pending_window_seconds": "86400"} {
		if string(env.Meta[key]) != want {
			t.Errorf("meta.%s: %s, se esperaba %s", key, env.Meta[key], want)
		}
	}
	if _, got := keysOf(t, env.Data[0]); got != "created_at,description,executed_at,failure_reason,handler,id,name,payload,status,tenant_id,trigger_at" {
		t.Fatalf("una fila es una tarea completa: %s", got)
	}
	// La empresa es la del token y la ventana sale del reloj del caso de uso.
	if f := store.filter; f.TenantID != me || !f.Before.Equal(clockNow.Add(domain.PendingTasksWindow)) || f.Page != 2 || f.PerPage != 1 {
		t.Fatalf("filtro: %+v", f)
	}

	env = listTasks(t, srv, "?per_page=500&page=-3", me.String())
	if f := store.filter; f.PerPage != maxPerPage || f.Page != 1 || string(env.Meta["per_page"]) != strconv.Itoa(maxPerPage) {
		t.Fatalf("per_page de mas se recorta al maximo: %+v, meta %v", f, env.Meta)
	}

	env = listTasks(t, srv, "?page="+strconv.Itoa(math.MaxInt), me.String())
	if len(env.Data) != 0 || string(env.Meta["total"]) != "3" {
		t.Fatalf("pagina enorme: %d filas, meta %v", len(env.Data), env.Meta)
	}

	rec := request(t, srv, http.MethodGet, "/api/v1/scheduler/tasks", "", uuid.NewString())
	var empty tasksEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil || string(mustJSON(t, empty.Data)) != "[]" ||
		string(empty.Meta["pending_window_seconds"]) != "86400" || string(empty.Meta["per_page"]) != strconv.Itoa(defaultPerPage) {
		t.Fatalf("sin tareas: %s", rec.Body.String())
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// historyData es un trabajo de una empresa con sus ejecuciones; historyJobs e historyExecs lo
// sirven como el SQL, con el desplazamiento del dominio.
type historyData struct {
	job     domain.JobDefinition
	execs   []*domain.JobExecution
	perPage int
}

type historyJobs struct {
	ports.JobDefinitionRepository
	d *historyData
}

func (h historyJobs) GetByID(_ context.Context, id, tenantID uuid.UUID) (*domain.JobDefinition, error) {
	if id != h.d.job.ID || (h.d.job.TenantID != nil && *h.d.job.TenantID != tenantID) {
		return nil, domain.ErrJobNotFound
	}
	j := h.d.job
	return &j, nil
}

type historyExecs struct {
	ports.JobExecutionRepository
	d *historyData
}

func (h historyExecs) GetByJob(_ context.Context, _, _ uuid.UUID, page, perPage int) ([]*domain.JobExecution, int64, error) {
	h.d.perPage = perPage
	all := h.d.execs
	offset := domain.PageOffset(page, perPage)
	if offset >= int64(len(all)) {
		return nil, int64(len(all)), nil
	}
	return all[offset:min(int(offset)+perPage, len(all))], int64(len(all)), nil
}

func TestHistorialConPaginaEnormeYDeOtraEmpresa(t *testing.T) {
	owner := uuid.New()
	store := &historyData{job: domain.JobDefinition{ID: uuid.New(), TenantID: &owner}}
	for i := 0; i < 3; i++ {
		store.execs = append(store.execs, &domain.JobExecution{ID: uuid.New(), JobID: store.job.ID, TenantID: &owner, Status: domain.StatusCompleted, CreatedAt: clockNow})
	}
	uc := app.NewSchedulerUseCase(app.SchedulerDeps{Jobs: historyJobs{d: store}, Executions: historyExecs{d: store}, Logger: zap.NewNop()})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount("/", NewHandler(Deps{UC: uc, Perms: authz.NewChecker(unreachable, "")}).Routes())
	path := "/api/v1/scheduler/jobs/" + store.job.ID.String() + "/history"

	for _, page := range []string{strconv.Itoa(math.MaxInt), "99999999999999999999999", "-1", "x"} {
		rec := request(t, r, http.MethodGet, path+"?per_page=1000&page="+page, "", owner.String())
		var env pageEnvelope
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &env) != nil {
			t.Fatalf("page=%s: %d %s", page, rec.Code, rec.Body.String())
		}
		if store.perPage != maxPerPage || env.Meta["per_page"] != maxPerPage || env.Meta["total"] != 3 {
			t.Fatalf("page=%s: per_page %d, meta %v", page, store.perPage, env.Meta)
		}
		// Una pagina ilegible es la primera; una que desborda int satura (strconv.Atoi da el
		// maximo) y, como MaxInt, cae despues de la ultima: sin filas y sin 500.
		wantRows := 3
		if page == strconv.Itoa(math.MaxInt) || page == "99999999999999999999999" {
			wantRows = 0
		}
		if len(env.Data) != wantRows {
			t.Fatalf("page=%s: %d filas, se esperaban %d", page, len(env.Data), wantRows)
		}
	}

	rec := request(t, r, http.MethodGet, path, "", uuid.NewString())
	if code, _ := errorOf(t, rec); rec.Code != http.StatusNotFound || code != "NOT_FOUND" {
		t.Fatalf("historial de un trabajo de otra empresa: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReactivarUnOneTimeYaLanzadoEs409(t *testing.T) {
	srv, jobs, schedules := timezoneServer(t)
	if code, _ := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs",
		`{"name":"Una vez","code":"una-vez","job_type":"one_time","handler":"reports.daily"}`); code != http.StatusCreated {
		t.Fatalf("crear: %d", code)
	}
	ran := clockNow.Add(-time.Hour)
	jobs.job.IsActive, schedules.last = false, &ran
	code, env := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs/"+jobs.job.ID.String()+"/enable", "")
	if code != http.StatusConflict || env.Error == nil || env.Error.Code != codeJobAlreadyRun {
		t.Fatalf("reactivar un one_time ya despachado: %d %+v", code, env.Error)
	}
	if jobs.job.IsActive {
		t.Fatal("sigue inactivo")
	}
}

func TestReactivarUnIntervaloLoDejaEnSuRejilla(t *testing.T) {
	srv, jobs, schedules := timezoneServer(t)
	if code, _ := send(t, srv, http.MethodPost, "/api/v1/scheduler/jobs",
		`{"name":"Cada cuarto","code":"cuarto","job_type":"interval","interval_minutes":15,"handler":"reports.daily"}`); code != http.StatusCreated {
		t.Fatalf("crear: %d", code)
	}
	jobs.job.IsActive, schedules.next = false, clockNow.Add(-50*time.Minute)
	req := httpRequest(t, srv, http.MethodPost, "/api/v1/scheduler/jobs/"+jobs.job.ID.String()+"/enable")
	if req != http.StatusNoContent || !jobs.job.IsActive || !schedules.next.Equal(clockNow.Add(10*time.Minute)) {
		t.Fatalf("reactivar: %d, activo %v, proxima %v (se esperaba 10:10)", req, jobs.job.IsActive, schedules.next)
	}
}

func httpRequest(t *testing.T, srv http.Handler, method, path string) int {
	t.Helper()
	return request(t, srv, method, path, "", uuid.NewString()).Code
}
