package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// stubRuns es un repositorio de verificaciones minimo: una por empresa, sin latido que venza.
type stubRuns struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*domain.IntegrityRun
}

func (s *stubRuns) find(tenant uuid.UUID, running bool) *domain.IntegrityRun {
	for _, r := range s.runs {
		if r.TenantID == tenant && (r.Status == domain.RunRunning) == running {
			c := *r
			return &c
		}
	}
	return nil
}

func (s *stubRuns) Open(_ context.Context, run *domain.IntegrityRun) (*domain.IntegrityRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.find(run.TenantID, true); a != nil {
		return a, domain.ErrRunActive
	}
	stored := *run
	stored.Status, stored.StartedAt, stored.HeartbeatAt = domain.RunRunning, time.Now(), time.Now()
	s.runs[stored.ID] = &stored
	c := stored
	return &c, nil
}

func (s *stubRuns) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.IntegrityRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil || r.TenantID != tenantID {
		return nil, domain.ErrRunNotFound
	}
	c := *r
	return &c, nil
}

func (s *stubRuns) Active(_ context.Context, tenantID uuid.UUID) (*domain.IntegrityRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(tenantID, true), nil
}

func (s *stubRuns) LastCompleted(_ context.Context, tenantID uuid.UUID, _ bool, _ domain.RunMode) (*domain.IntegrityRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(tenantID, false), nil
}

func (s *stubRuns) List(_ context.Context, tenantID uuid.UUID, _ int) ([]*domain.IntegrityRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*domain.IntegrityRun
	for _, r := range s.runs {
		if r.TenantID == tenantID {
			c := *r
			out = append(out, &c)
		}
	}
	return out, nil
}

func (s *stubRuns) Claim(context.Context, uuid.UUID, uuid.UUID, string, time.Duration) (*domain.IntegrityRun, bool, error) {
	return nil, false, nil
}

func (s *stubRuns) Progress(_ context.Context, _, id uuid.UUID, _ string, _ domain.RunProgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[id].CancelRequested {
		return domain.ErrRunCancelled
	}
	return nil
}

func (s *stubRuns) Finish(_ context.Context, _, id uuid.UUID, _ string, f domain.RunFinish) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.runs[id].Status, s.runs[id].Result, s.runs[id].FinishedAt = f.Status, f.Result, &now
	return nil
}

func (s *stubRuns) RequestCancel(_ context.Context, tenantID, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.runs[id]; r != nil && r.TenantID == tenantID && r.Status == domain.RunRunning {
		r.CancelRequested = true
		return true, nil
	}
	return false, nil
}

func (s *stubRuns) statusOf(id uuid.UUID) domain.RunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id].Status
}

// newRunsFlow monta las rutas sobre el caso de uso real con verificaciones en segundo plano. headSeq
// es la posicion de cabeza de las dos cadenas: decide si GET /integrity contesta o lanza una.
func newRunsFlow(t *testing.T, headSeq int64) (*flow, *stubRuns) {
	t.Helper()
	f := &flow{logs: &stubLogs{verdict: &domain.ChainIntegrity{OK: true, Checked: 3, Chain: domain.ChainAuditLogs}}, security: &stubSecurity{},
		changes: &stubChanges{}, summary: &stubSummary{}, tenant: uuid.NewString(), user: uuid.NewString()}
	runs := &stubRuns{runs: map[uuid.UUID]*domain.IntegrityRun{}}
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	uc := app.NewAuditUseCase(app.AuditDeps{Logs: f.logs, Security: f.security, Changes: f.changes, Summary: f.summary,
		Events: stubPublisher{}, Logger: zap.NewNop(), Anchors: stubAnchors{headSeq: headSeq},
		Integrity: app.IntegrityRunConfig{Runs: runs, Background: ctx, InlineMaxRows: 1000}})
	r := chi.NewRouter()
	r.Use(middleware.InjectFromGateway)
	r.Mount(base, NewHandler(uc, authz.NewChecker(unreachable, "")).Routes())
	f.srv = r
	return f, runs
}

func waitStatus(t *testing.T, runs *stubRuns, id uuid.UUID, want domain.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for runs.statusOf(id) != want {
		if time.Now().After(deadline) {
			t.Fatalf("la verificacion no llego a %s", want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func runOf(t *testing.T, rec *httptest.ResponseRecorder) domain.IntegrityRun {
	t.Helper()
	var run domain.IntegrityRun
	if err := json.Unmarshal(decode(t, rec).Data, &run); err != nil {
		t.Fatalf("verificacion ilegible: %v %s", err, rec.Body)
	}
	return run
}

func TestLanzarUnaVerificacionResponde202ConSuUbicacion(t *testing.T) {
	f, runs := newRunsFlow(t, 10)
	rec := f.do(http.MethodPost, base+"/integrity/runs", "")
	if rec.Code != http.StatusAccepted || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d, Retry-After %q: %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	run := runOf(t, rec)
	if rec.Header().Get("Location") != base+"/integrity/runs/"+run.ID.String() {
		t.Fatalf("Location %q", rec.Header().Get("Location"))
	}
	if run.Mode != domain.RunModeFull || run.Trigger != domain.RunTriggerManual || run.RequestedBy == nil || run.RequestedBy.String() != f.user {
		t.Fatalf("por defecto es completa, manual y a nombre de quien la lanza: %+v", run)
	}
	waitStatus(t, runs, run.ID, domain.RunCompleted)

	got := runOf(t, f.do(http.MethodGet, base+"/integrity/runs/"+run.ID.String(), ""))
	if got.Status != domain.RunCompleted || got.Result == nil || !got.Result.OK {
		t.Fatalf("consultada: %+v", got)
	}
	if strings.Contains(f.do(http.MethodGet, base+"/integrity/runs/"+run.ID.String(), "").Body.String(), "owner") {
		t.Fatal("el proceso dueno no forma parte de la respuesta")
	}
}

func TestLanzarConModoIncrementalYRechazarLoDesconocido(t *testing.T) {
	f, _ := newRunsFlow(t, 10)
	rec := f.do(http.MethodPost, base+"/integrity/runs", `{"mode":"incremental"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if run := runOf(t, rec); run.Mode != domain.RunModeFull {
		t.Fatalf("sin punto previo la incremental es completa: %s", run.Mode)
	}
	f, _ = newRunsFlow(t, 10)
	if rec := f.do(http.MethodPost, base+"/integrity/runs", `{"mode":"rapido"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("modo desconocido: %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, base+"/integrity/runs", `{"modo":"full"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("campo desconocido: %d", rec.Code)
	}
}

func TestUnSegundoLanzamientoRecibe409ConLaVerificacionEnCurso(t *testing.T) {
	f, runs := newRunsFlow(t, 10)
	gate := make(chan struct{})
	f.logs.gate = gate
	first := runOf(t, f.do(http.MethodPost, base+"/integrity/runs", ""))
	rec := f.do(http.MethodPost, base+"/integrity/runs", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var env struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Code != "VERIFICATION_RUNNING" || env.Error.Details["run_id"] != first.ID.String() {
		t.Fatalf("%v %s", err, rec.Body)
	}
	close(gate)
	waitStatus(t, runs, first.ID, domain.RunCompleted)
}

func TestCancelarUnaVerificacion(t *testing.T) {
	f, runs := newRunsFlow(t, 10)
	gate := make(chan struct{})
	f.logs.gate = gate
	run := runOf(t, f.do(http.MethodPost, base+"/integrity/runs", ""))
	rec := f.do(http.MethodPost, base+"/integrity/runs/"+run.ID.String()+"/cancel", "")
	if rec.Code != http.StatusOK || !runOf(t, rec).CancelRequested {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	close(gate)
	waitStatus(t, runs, run.ID, domain.RunCancelled)
}

func TestConsultarUnaVerificacionAusenteOMalFormada(t *testing.T) {
	f, _ := newRunsFlow(t, 10)
	if rec := f.do(http.MethodGet, base+"/integrity/runs/"+uuid.NewString(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("ausente: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, base+"/integrity/runs/no-es-uuid", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("mal formada: %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, base+"/integrity/runs/"+uuid.NewString()+"/cancel", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cancelar una ausente: %d", rec.Code)
	}
}

func TestListarLasVerificacionesDeLaEmpresa(t *testing.T) {
	f, runs := newRunsFlow(t, 10)
	run := runOf(t, f.do(http.MethodPost, base+"/integrity/runs", ""))
	waitStatus(t, runs, run.ID, domain.RunCompleted)
	rec := f.do(http.MethodGet, base+"/integrity/runs", "")
	var list []domain.IntegrityRun
	if err := json.Unmarshal(decode(t, rec).Data, &list); err != nil || len(list) != 1 || list[0].ID != run.ID {
		t.Fatalf("%v %s", err, rec.Body)
	}
}

// GET /integrity conserva su contrato para las cadenas que caben en una peticion y, para las que no,
// responde 202 con la verificacion en curso en lugar de agotar el plazo.
func TestGetIntegrityDeUnaCadenaGrandeResponde202ConLaVerificacion(t *testing.T) {
	f, runs := newRunsFlow(t, 5000)
	rec := f.do(http.MethodGet, base+"/integrity", "")
	if rec.Code != http.StatusAccepted || rec.Header().Get("Location") == "" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var body struct {
		Run           domain.IntegrityRun  `json:"run"`
		LastCompleted *domain.IntegrityRun `json:"last_completed"`
	}
	if err := json.Unmarshal(decode(t, rec).Data, &body); err != nil || body.Run.ID == uuid.Nil || body.Run.Trigger != domain.RunTriggerRequest {
		t.Fatalf("%v %s", err, rec.Body)
	}
	waitStatus(t, runs, body.Run.ID, domain.RunCompleted)

	small, _ := newRunsFlow(t, 10)
	rec = small.do(http.MethodGet, base+"/integrity", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"run"`) {
		t.Fatalf("una cadena pequena se verifica dentro de la peticion: %d %s", rec.Code, rec.Body)
	}
}

func TestLasVerificacionesExigenElPermisoDeVerificar(t *testing.T) {
	for _, rt := range []ruta{
		{http.MethodPost, base + "/integrity/runs", "", "audit/integrity/verify"},
		{http.MethodGet, base + "/integrity/runs", "", "audit/integrity/verify"},
		{http.MethodGet, base + "/integrity/runs/" + uuid.NewString(), "", "audit/integrity/verify"},
		{http.MethodPost, base + "/integrity/runs/" + uuid.NewString() + "/cancel", "", "audit/integrity/verify"},
	} {
		if code := call(t, newServer(policyStub(t, menos(catalogo, rt.perm)...)), rt, "auditor"); code != http.StatusForbidden {
			t.Errorf("%s %s sin integrity/verify: %d", rt.method, rt.path, code)
		}
	}
}
