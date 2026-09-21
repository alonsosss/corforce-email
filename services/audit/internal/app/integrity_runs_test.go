package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// memRuns es el repositorio de verificaciones en memoria con la semantica que exige el puerto: una en
// curso por empresa, solo su dueno la actualiza o cierra, y el latido decide si esta abandonada.
type memRuns struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*domain.IntegrityRun
}

func newMemRuns() *memRuns { return &memRuns{runs: map[uuid.UUID]*domain.IntegrityRun{}} }

func cloneRun(r *domain.IntegrityRun) *domain.IntegrityRun {
	c := *r
	c.Chains = map[domain.ChainName]*domain.ChainState{}
	for k, v := range r.Chains {
		st := *v
		if v.Checkpoint != nil {
			cp := *v.Checkpoint
			st.Checkpoint = &cp
		}
		c.Chains[k] = &st
	}
	return &c
}

func (m *memRuns) active(tenant uuid.UUID) *domain.IntegrityRun {
	for _, r := range m.runs {
		if r.TenantID == tenant && r.Status == domain.RunRunning {
			return r
		}
	}
	return nil
}

func (m *memRuns) Open(_ context.Context, run *domain.IntegrityRun) (*domain.IntegrityRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.active(run.TenantID); a != nil {
		return cloneRun(a), domain.ErrRunActive
	}
	stored := cloneRun(run)
	stored.Status, stored.StartedAt, stored.HeartbeatAt = domain.RunRunning, time.Now(), time.Now()
	m.runs[stored.ID] = stored
	return cloneRun(stored), nil
}

func (m *memRuns) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.IntegrityRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil || r.TenantID != tenantID {
		return nil, domain.ErrRunNotFound
	}
	return cloneRun(r), nil
}

func (m *memRuns) Active(_ context.Context, tenantID uuid.UUID) (*domain.IntegrityRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.active(tenantID); a != nil {
		return cloneRun(a), nil
	}
	return nil, nil
}

func (m *memRuns) LastCompleted(_ context.Context, tenantID uuid.UUID, onlyOK bool, mode domain.RunMode) (*domain.IntegrityRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var last *domain.IntegrityRun
	for _, r := range m.runs {
		if r.TenantID != tenantID || r.Status != domain.RunCompleted || (onlyOK && !r.OK()) || (mode != "" && r.Mode != mode) {
			continue
		}
		if last == nil || r.FinishedAt.After(*last.FinishedAt) {
			last = r
		}
	}
	if last == nil {
		return nil, nil
	}
	return cloneRun(last), nil
}

func (m *memRuns) List(context.Context, uuid.UUID, int) ([]*domain.IntegrityRun, error) {
	return nil, nil
}

func (m *memRuns) Claim(_ context.Context, tenantID, id uuid.UUID, owner string, staleAfter time.Duration) (*domain.IntegrityRun, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil || r.TenantID != tenantID || r.Status != domain.RunRunning || (r.Owner != owner && time.Since(r.HeartbeatAt) < staleAfter) {
		return nil, false, nil
	}
	r.Owner, r.HeartbeatAt = owner, time.Now()
	return cloneRun(r), true, nil
}

func (m *memRuns) Progress(_ context.Context, tenantID, id uuid.UUID, owner string, p domain.RunProgress) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil || r.TenantID != tenantID || r.Owner != owner || r.Status != domain.RunRunning {
		return domain.ErrRunLost
	}
	r.Phase, r.Checked, r.CurrentSeq, r.HeartbeatAt = p.Phase, p.Checked, p.CurrentSeq, time.Now()
	r.Chains = cloneRun(&domain.IntegrityRun{Chains: p.Chains}).Chains
	if r.CancelRequested {
		return domain.ErrRunCancelled
	}
	return nil
}

func (m *memRuns) Finish(_ context.Context, tenantID, id uuid.UUID, owner string, f domain.RunFinish) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil || r.TenantID != tenantID || r.Owner != owner || r.Status != domain.RunRunning {
		return domain.ErrRunLost
	}
	now := time.Now()
	r.Status, r.Result, r.ErrorCode, r.FinishedAt, r.HeartbeatAt = f.Status, f.Result, f.ErrorCode, &now, now
	r.Chains = cloneRun(&domain.IntegrityRun{Chains: f.Chains}).Chains
	if f.Status == domain.RunCompleted {
		r.Phase = domain.RunPhaseDone
	}
	return nil
}

func (m *memRuns) RequestCancel(_ context.Context, tenantID, id uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil || r.TenantID != tenantID || r.Status != domain.RunRunning {
		return false, nil
	}
	r.CancelRequested = true
	return true, nil
}

func (m *memRuns) status(id uuid.UUID) domain.RunStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[id].Status
}

// age envejece el latido de una verificacion, como el de un proceso que murio hace tiempo.
func (m *memRuns) age(id uuid.UUID) {
	m.mu.Lock()
	m.runs[id].HeartbeatAt = time.Now().Add(-time.Hour)
	m.mu.Unlock()
}

// scriptedChain simula el recorrido de una cadena de total filas en lotes de perBatch: llama a
// OnBatch tras cada uno, continua desde opts.From y se detiene si el contexto se cancela.
type scriptedChain struct {
	chain    domain.ChainName
	total    int
	perBatch int
	broken   bool
	failWith error
	// afterBatch se llama tras cada lote (n empieza en 1) antes de seguir; permite a la prueba
	// sincronizarse con el recorrido o detenerlo.
	afterBatch func(n int)

	mu    sync.Mutex
	froms []*domain.ChainCheckpoint
}

func (c *scriptedChain) VerifyChain(ctx context.Context, _ uuid.UUID, opts domain.VerifyOptions) (*domain.ChainIntegrity, error) {
	c.mu.Lock()
	c.froms = append(c.froms, opts.From)
	c.mu.Unlock()
	if c.failWith != nil {
		return nil, c.failWith
	}
	done := 0
	if opts.From != nil {
		done = opts.From.Checked
	}
	n := 0
	for done < c.total {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		done = min(done+c.perBatch, c.total)
		n++
		if done < c.total && opts.OnBatch != nil {
			if err := opts.OnBatch(domain.ChainCheckpoint{Seq: int64(done), Hash: fmt.Sprintf("h%d", done), HashVersion: 2, Checked: done}); err != nil {
				return nil, err
			}
		}
		if c.afterBatch != nil {
			c.afterBatch(n)
		}
	}
	res := &domain.ChainIntegrity{OK: !c.broken, Chain: c.chain, Checked: done, Head: &domain.ChainHead{Seq: int64(done), Hash: fmt.Sprintf("h%d", done), HashVersion: 2}}
	if c.broken {
		res.Reason = domain.ReasonChainBroken
		return res, nil
	}
	res.Checkpoint = &domain.ChainCheckpoint{Seq: int64(done), Hash: fmt.Sprintf("h%d", done), HashVersion: 2, Checked: done, Complete: true}
	return res, nil
}

func (c *scriptedChain) lastFrom() *domain.ChainCheckpoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.froms[len(c.froms)-1]
}

func (g *verificationGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.running)
}

type logsWithChain struct {
	fakeLogs
	chain *scriptedChain
}

func (l *logsWithChain) VerifyChain(ctx context.Context, t uuid.UUID, o domain.VerifyOptions) (*domain.ChainIntegrity, error) {
	return l.chain.VerifyChain(ctx, t, o)
}

type securityWithChain struct {
	fakeSecurity
	chain *scriptedChain
}

func (s *securityWithChain) VerifyChain(ctx context.Context, t uuid.UUID, o domain.VerifyOptions) (*domain.ChainIntegrity, error) {
	return s.chain.VerifyChain(ctx, t, o)
}

type runMetrics struct {
	mu       sync.Mutex
	finished []string
	broken   int
}

func (m *runMetrics) RunFinished(origin domain.RunTrigger, outcome string) {
	m.mu.Lock()
	m.finished = append(m.finished, string(origin)+"/"+outcome)
	m.mu.Unlock()
}
func (m *runMetrics) SweepBroken(n int) { m.broken = n }

func (m *runMetrics) list() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.finished...)
}

var _ ports.IntegrityMetrics = (*runMetrics)(nil)

type runRig struct {
	uc      *AuditUseCase
	runs    *memRuns
	logs    *scriptedChain
	events  *scriptedChain
	anchors *fakeAnchors
	metrics *runMetrics
	bg      context.Context
	stop    context.CancelFunc
}

// newRunRig monta un caso de uso con dos cadenas de 1000 filas en lotes de 100 y un punto de
// reanudacion por lote.
func newRunRig(t *testing.T, runs *memRuns) *runRig {
	t.Helper()
	bg, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	r := &runRig{
		runs:    runs,
		logs:    &scriptedChain{chain: domain.ChainAuditLogs, total: 1000, perBatch: 100},
		events:  &scriptedChain{chain: domain.ChainSecurityEvents, total: 1000, perBatch: 100},
		anchors: &fakeAnchors{heads: map[domain.ChainName]*domain.ChainHead{}, found: map[domain.ChainName]domain.AnchorFindings{}},
		metrics: &runMetrics{},
		bg:      bg,
		stop:    stop,
	}
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 1000, Hash: "h1000", HashVersion: 2}
	r.anchors.heads[domain.ChainSecurityEvents] = &domain.ChainHead{Seq: 1000, Hash: "h1000", HashVersion: 2}
	r.uc = NewAuditUseCase(AuditDeps{
		Logs: &logsWithChain{chain: r.logs}, Security: &securityWithChain{chain: r.events},
		Events: &fakePublisher{}, Logger: zap.NewNop(), Anchors: r.anchors, Tx: &passthroughTx{},
		Integrity: IntegrityRunConfig{Runs: runs, Metrics: r.metrics, Background: bg, CheckpointEvery: time.Nanosecond, InlineMaxRows: 5000},
	})
	return r
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("no ocurrio: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func startRun(t *testing.T, r *runRig, tenant uuid.UUID, mode domain.RunMode) *domain.IntegrityRun {
	t.Helper()
	run, err := r.uc.StartIntegrityRun(context.Background(), tenant, nil, mode, domain.RunTriggerManual)
	if err != nil {
		t.Fatalf("lanzar: %v", err)
	}
	return run
}

func TestUnaVerificacionEnSegundoPlanTerminaYDejaSuResultado(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	tenant := uuid.New()
	run := startRun(t, r, tenant, domain.RunModeFull)
	if run.Status != domain.RunRunning || run.TargetSeq != 1000 || run.Mode != domain.RunModeFull {
		t.Fatalf("recien lanzada: %+v", run)
	}
	waitUntil(t, "termina", func() bool { return r.runs.status(run.ID) == domain.RunCompleted })

	done, _ := r.uc.GetIntegrityRun(context.Background(), tenant, run.ID)
	if !done.OK() || done.Phase != domain.RunPhaseDone || done.FinishedAt == nil {
		t.Fatalf("terminada: %+v", done)
	}
	if done.Result.Checked != 1000 || done.Result.SecurityEvents == nil || done.Result.SecurityEvents.Checked != 1000 {
		t.Fatalf("resultado: %+v", done.Result)
	}
	if done.Checked != 2000 || done.CurrentSeq != 1000 {
		t.Fatalf("avance: checked %d, seq %d", done.Checked, done.CurrentSeq)
	}
	if got := r.metrics.list(); len(got) != 1 || got[0] != "manual/ok" {
		t.Fatalf("metricas: %v", got)
	}
}

func TestUnaCadenaRotaTerminaComoVerificacionCompletaConElVeredictoEnRojo(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	r.logs.broken = true
	tenant := uuid.New()
	run := startRun(t, r, tenant, domain.RunModeFull)
	waitUntil(t, "termina", func() bool { return r.runs.status(run.ID) == domain.RunCompleted })

	done, _ := r.uc.GetIntegrityRun(context.Background(), tenant, run.ID)
	if done.OK() || done.Result.Reason != domain.ReasonChainBroken || done.Result.Chain != domain.ChainAuditLogs {
		t.Fatalf("veredicto: %+v", done.Result)
	}
	if done.Result.SecurityEvents == nil || !done.Result.SecurityEvents.OK {
		t.Fatal("la otra cadena tambien se verifica")
	}
	if got := r.metrics.list(); len(got) != 1 || got[0] != "manual/broken" {
		t.Fatalf("metricas: %v", got)
	}
}

func TestUnFalloTecnicoNoDiceNadaDeLaCadena(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	r.logs.failWith = errors.New("conexion perdida")
	tenant := uuid.New()
	run := startRun(t, r, tenant, domain.RunModeFull)
	waitUntil(t, "falla", func() bool { return r.runs.status(run.ID) == domain.RunFailed })

	done, _ := r.uc.GetIntegrityRun(context.Background(), tenant, run.ID)
	if done.Result != nil || done.ErrorCode != domain.RunErrorInternal || done.OK() {
		t.Fatalf("fallo tecnico: %+v", done)
	}
	if got := r.metrics.list(); len(got) != 1 || got[0] != "manual/failed" {
		t.Fatalf("metricas: %v", got)
	}
}

// Dos lanzamientos a la vez: corre uno; el otro recibe la verificacion que ya esta en curso.
func TestDosLanzamientosSimultaneosSoloCorreUno(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	tenant := uuid.New()
	release := make(chan struct{})
	r.logs.afterBatch = func(n int) {
		if n == 1 {
			<-release
		}
	}
	first := startRun(t, r, tenant, domain.RunModeFull)
	second, err := r.uc.StartIntegrityRun(context.Background(), tenant, nil, domain.RunModeFull, domain.RunTriggerManual)
	if !errors.Is(err, domain.ErrRunActive) || second == nil || second.ID != first.ID {
		t.Fatalf("segundo lanzamiento: %v %+v", err, second)
	}
	other, err := r.uc.StartIntegrityRun(context.Background(), uuid.New(), nil, domain.RunModeFull, domain.RunTriggerManual)
	if err != nil || other.ID == first.ID {
		t.Fatalf("otra empresa: %v", err)
	}
	close(release)
	waitUntil(t, "terminan", func() bool {
		return r.runs.status(first.ID) == domain.RunCompleted && r.runs.status(other.ID) == domain.RunCompleted
	})
	if n := len(r.runs.runs); n != 2 {
		t.Fatalf("%d verificaciones creadas", n)
	}
}

func TestUnaEmpresaNoVeLasVerificacionesDeOtra(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	run := startRun(t, r, uuid.New(), domain.RunModeFull)
	if _, err := r.uc.GetIntegrityRun(context.Background(), uuid.New(), run.ID); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("leer la de otra empresa: %v", err)
	}
	if _, err := r.uc.CancelIntegrityRun(context.Background(), uuid.New(), run.ID); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("cancelar la de otra empresa: %v", err)
	}
}

func TestSePuedeCancelarUnaVerificacionEnCurso(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	tenant := uuid.New()
	reached := make(chan struct{})
	release := make(chan struct{})
	r.logs.afterBatch = func(n int) {
		if n == 3 {
			close(reached)
			<-release
		}
	}
	run := startRun(t, r, tenant, domain.RunModeFull)
	<-reached
	got, err := r.uc.CancelIntegrityRun(context.Background(), tenant, run.ID)
	if err != nil || !got.CancelRequested || got.Status != domain.RunRunning {
		t.Fatalf("cancelar: %v %+v", err, got)
	}
	close(release)
	waitUntil(t, "se cancela", func() bool { return r.runs.status(run.ID) == domain.RunCancelled })

	done, _ := r.uc.GetIntegrityRun(context.Background(), tenant, run.ID)
	if done.Result != nil || done.FinishedAt == nil {
		t.Fatalf("cancelada: %+v", done)
	}
	if again, err := r.uc.CancelIntegrityRun(context.Background(), tenant, run.ID); err != nil || again.Status != domain.RunCancelled {
		t.Fatalf("cancelar una terminada no hace nada: %v %+v", err, again)
	}
	if got := r.metrics.list(); len(got) != 1 || got[0] != "manual/cancelled" {
		t.Fatalf("metricas: %v", got)
	}
	// Cancelada, la empresa puede lanzar otra.
	r.logs.afterBatch = nil
	next := startRun(t, r, tenant, domain.RunModeFull)
	waitUntil(t, "la siguiente termina", func() bool { return r.runs.status(next.ID) == domain.RunCompleted })
}

// El proceso muere a mitad: la verificacion queda en curso con su punto. Otro proceso, cuando su
// latido vence, la retoma DESDE ese punto y no desde el principio.
func TestUnaVerificacionAbandonadaSeRetomaDesdeSuPunto(t *testing.T) {
	runs := newMemRuns()
	tenant := uuid.New()

	dying := newRunRig(t, runs)
	reached := make(chan struct{})
	dying.logs.afterBatch = func(n int) {
		if n == 4 {
			close(reached)
			dying.stop()
			time.Sleep(50 * time.Millisecond)
		}
	}
	run := startRun(t, dying, tenant, domain.RunModeFull)
	<-reached
	waitUntil(t, "el proceso suelta su cupo", func() bool { return dying.uc.verifying.count() == 0 })
	if runs.status(run.ID) != domain.RunRunning {
		t.Fatal("un proceso interrumpido no cierra su verificacion: la deja para retomarla")
	}
	stored, _ := runs.Get(context.Background(), tenant, run.ID)
	if cp := stored.Chains[domain.ChainAuditLogs].Checkpoint; cp == nil || cp.Checked < 300 {
		t.Fatalf("punto guardado: %+v", cp)
	}

	survivor := newRunRig(t, runs)
	if got, _ := survivor.uc.GetIntegrityRun(context.Background(), tenant, run.ID); got.Status != domain.RunRunning {
		t.Fatalf("con el latido reciente no se retoma: %+v", got)
	}
	if survivor.logs.froms != nil {
		t.Fatal("no debio empezar a verificar")
	}
	runs.age(run.ID)
	got, err := survivor.uc.GetIntegrityRun(context.Background(), tenant, run.ID)
	if err != nil || got.Owner == "" {
		t.Fatalf("retomar: %v %+v", err, got)
	}
	waitUntil(t, "termina", func() bool { return runs.status(run.ID) == domain.RunCompleted })

	from := survivor.logs.lastFrom()
	if from == nil || from.Checked < 300 {
		t.Fatalf("la retomada empezo en %+v: debe continuar desde el punto, no desde cero", from)
	}
	done, _ := survivor.uc.GetIntegrityRun(context.Background(), tenant, run.ID)
	if !done.OK() || done.Result.Checked != 1000 || done.Result.SecurityEvents.Checked != 1000 {
		t.Fatalf("resultado tras retomar: %+v", done.Result)
	}
}

func TestUnLanzamientoRetomaLaVerificacionAbandonadaEnVezDeAbrirOtra(t *testing.T) {
	runs := newMemRuns()
	tenant := uuid.New()
	dying := newRunRig(t, runs)
	reached := make(chan struct{})
	dying.logs.afterBatch = func(n int) {
		if n == 2 {
			close(reached)
			dying.stop()
			time.Sleep(50 * time.Millisecond)
		}
	}
	run := startRun(t, dying, tenant, domain.RunModeFull)
	<-reached
	waitUntil(t, "se interrumpe", func() bool { return dying.uc.verifying.count() == 0 })
	runs.age(run.ID)

	survivor := newRunRig(t, runs)
	again, err := survivor.uc.StartIntegrityRun(context.Background(), tenant, nil, domain.RunModeFull, domain.RunTriggerManual)
	if err != nil || again.ID != run.ID {
		t.Fatalf("debia retomar %s: %v %+v", run.ID, err, again)
	}
	waitUntil(t, "termina", func() bool { return runs.status(run.ID) == domain.RunCompleted })
	if len(runs.runs) != 1 {
		t.Fatalf("%d verificaciones", len(runs.runs))
	}
}

func TestLaIncrementalParteDelPuntoDeLaUltimaVerificacionBuena(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	tenant := uuid.New()

	// Sin verificacion previa, la incremental es completa.
	first := startRun(t, r, tenant, domain.RunModeIncremental)
	if first.Mode != domain.RunModeFull {
		t.Fatalf("sin punto previo debe ser completa: %s", first.Mode)
	}
	waitUntil(t, "termina", func() bool { return r.runs.status(first.ID) == domain.RunCompleted })
	if r.logs.lastFrom() != nil {
		t.Fatal("la completa parte del principio")
	}

	// Con ella hecha, crece la cadena y la incremental solo relee lo nuevo.
	r.logs.total, r.events.total = 1300, 1300
	second := startRun(t, r, tenant, domain.RunModeIncremental)
	if second.Mode != domain.RunModeIncremental {
		t.Fatalf("modo %s", second.Mode)
	}
	waitUntil(t, "termina", func() bool { return r.runs.status(second.ID) == domain.RunCompleted })
	from := r.logs.lastFrom()
	if from == nil || from.Seq != 1000 || from.Hash != "h1000" {
		t.Fatalf("la incremental debia partir del punto final: %+v", from)
	}
	done, _ := r.uc.GetIntegrityRun(context.Background(), tenant, second.ID)
	if done.Result.Checked != 1300 {
		t.Fatalf("el total incluye lo ya verificado: %d", done.Result.Checked)
	}
}

func TestUnaVerificacionRotaNoEsPuntoDePartida(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	r.logs.broken = true
	tenant := uuid.New()
	first := startRun(t, r, tenant, domain.RunModeFull)
	waitUntil(t, "termina", func() bool { return r.runs.status(first.ID) == domain.RunCompleted })

	r.logs.broken = false
	second := startRun(t, r, tenant, domain.RunModeIncremental)
	if second.Mode != domain.RunModeFull {
		t.Fatalf("tras una cadena rota, la siguiente es completa: %s", second.Mode)
	}
}

func TestUnModeloDeVerificacionDesconocidoSeRechaza(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	if _, err := r.uc.StartIntegrityRun(context.Background(), uuid.New(), nil, "rapido", domain.RunTriggerManual); err == nil {
		t.Fatal("un modo desconocido se acepto")
	}
}

func TestElTopeDeVerificacionesPorProcesoTambienVaConLasAsincronas(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	release := make(chan struct{})
	r.logs.afterBatch = func(n int) {
		if n == 1 {
			<-release
		}
	}
	defer close(release)
	for i := 0; i < maxConcurrentVerifications; i++ {
		startRun(t, r, uuid.New(), domain.RunModeFull)
	}
	if _, err := r.uc.StartIntegrityRun(context.Background(), uuid.New(), nil, domain.RunModeFull, domain.RunTriggerManual); !errors.Is(err, domain.ErrVerificationBusy) {
		t.Fatalf("quinta verificacion: %v", err)
	}
}

func TestGetIntegrityContestaDentroDeLaPeticionSiLaCadenaEsPequena(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 2000}
	r.anchors.heads[domain.ChainSecurityEvents] = &domain.ChainHead{Seq: 3000}
	check, err := r.uc.CheckIntegrity(context.Background(), uuid.New(), nil)
	if err != nil || check.Result == nil || check.Run != nil || !check.Result.OK {
		t.Fatalf("cadena pequena: %v %+v", err, check)
	}
	if len(r.runs.runs) != 0 {
		t.Fatal("una cadena pequena no abre una verificacion")
	}
}

func TestGetIntegrityDeUnaCadenaGrandeDaLaVerificacionEnSegundoPlano(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	tenant := uuid.New()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 4000}
	r.anchors.heads[domain.ChainSecurityEvents] = &domain.ChainHead{Seq: 1500}
	release := make(chan struct{})
	r.logs.afterBatch = func(n int) {
		if n == 1 {
			<-release
		}
	}

	check, err := r.uc.CheckIntegrity(context.Background(), tenant, nil)
	if err != nil || check.Result != nil || check.Run == nil || check.Run.Trigger != domain.RunTriggerRequest || check.Run.Mode != domain.RunModeFull {
		t.Fatalf("cadena grande: %v %+v", err, check)
	}
	if check.LastCompleted != nil {
		t.Fatalf("aun no hay ninguna terminada: %+v", check.LastCompleted)
	}
	again, err := r.uc.CheckIntegrity(context.Background(), tenant, nil)
	if err != nil || again.Run == nil || again.Run.ID != check.Run.ID {
		t.Fatalf("una segunda consulta sigue la misma verificacion: %v %+v", err, again)
	}
	close(release)
	waitUntil(t, "termina", func() bool { return r.runs.status(check.Run.ID) == domain.RunCompleted })

	after, err := r.uc.CheckIntegrity(context.Background(), tenant, nil)
	if err != nil || after.Run == nil || after.Run.ID == check.Run.ID || after.LastCompleted == nil || after.LastCompleted.ID != check.Run.ID {
		t.Fatalf("la siguiente lanza otra y ofrece la ultima terminada: %v %+v", err, after)
	}
}

func TestElBarridoHaceCompletaSiLaUltimaEsAntiguaEIncrementalSiNo(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	tenant := uuid.New()

	out, err := r.uc.SweepIntegrity(context.Background(), tenant)
	if err != nil || out.Run == nil || out.Run.Mode != domain.RunModeFull || out.Broken {
		t.Fatalf("primera pasada: %v %+v", err, out)
	}
	if r.runs.status(out.Run.ID) != domain.RunCompleted {
		t.Fatal("el barrido espera a que termine")
	}
	out, err = r.uc.SweepIntegrity(context.Background(), tenant)
	if err != nil || out.Run == nil || out.Run.Mode != domain.RunModeIncremental || out.Run.Trigger != domain.RunTriggerSweep {
		t.Fatalf("segunda pasada: %v %+v", err, out)
	}

	// La ultima completa correcta pasa de FullEvery: vuelve a ser completa.
	r.runs.mu.Lock()
	for _, run := range r.runs.runs {
		if run.Mode == domain.RunModeFull {
			old := time.Now().Add(-DefaultFullEvery - time.Hour)
			run.FinishedAt = &old
		}
	}
	r.runs.mu.Unlock()
	out, _ = r.uc.SweepIntegrity(context.Background(), tenant)
	if out.Run == nil || out.Run.Mode != domain.RunModeFull {
		t.Fatalf("tras FullEvery: %+v", out)
	}
}

func TestElBarridoAvisaDeUnaCadenaRota(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	r.events.broken = true
	out, err := r.uc.SweepIntegrity(context.Background(), uuid.New())
	if err != nil || !out.Broken {
		t.Fatalf("barrido: %v %+v", err, out)
	}
	if got := r.metrics.list(); len(got) != 1 || got[0] != "sweep/broken" {
		t.Fatalf("metricas: %v", got)
	}
}

func TestElBarridoNoPisaUnaVerificacionViva(t *testing.T) {
	r := newRunRig(t, newMemRuns())
	tenant := uuid.New()
	release := make(chan struct{})
	r.logs.afterBatch = func(n int) {
		if n == 1 {
			<-release
		}
	}
	defer close(release)
	live := startRun(t, r, tenant, domain.RunModeFull)
	out, err := r.uc.SweepIntegrity(context.Background(), tenant)
	if err != nil || out.Run != nil {
		t.Fatalf("el barrido debia dejarla: %v %+v", err, out)
	}
	if r.runs.status(live.ID) != domain.RunRunning {
		t.Fatal("la verificacion viva no debe tocarse")
	}
}
