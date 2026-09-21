//go:build integration

// Verificaciones de la cadena en segundo plano (docs/adr/0006) contra un Postgres real: cerrojo entre
// procesos, cancelacion, reanudacion tras la muerte del proceso, manipulacion detectada tambien al
// reanudar, verificacion incremental y permisos del rol del servicio.
package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	outboxadapter "github.com/alonsosss/corforce-email/services/audit/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/audit/internal/app"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// hookedLogs deja a la prueba enterarse de cada lote de la verificacion (despues de que el servicio
// anoto su punto) y ver desde donde continuo.
type hookedLogs struct {
	*AuditLogRepo
	hook func(batch int, cp domain.ChainCheckpoint)

	mu    sync.Mutex
	froms []*domain.ChainCheckpoint
}

func (h *hookedLogs) VerifyChain(ctx context.Context, tenantID uuid.UUID, opts domain.VerifyOptions) (*domain.ChainIntegrity, error) {
	h.mu.Lock()
	h.froms = append(h.froms, opts.From)
	h.mu.Unlock()
	inner, batch := opts.OnBatch, 0
	opts.OnBatch = func(cp domain.ChainCheckpoint) error {
		batch++
		if inner != nil {
			if err := inner(cp); err != nil {
				return err
			}
		}
		if h.hook != nil {
			h.hook(batch, cp)
		}
		return nil
	}
	return h.AuditLogRepo.VerifyChain(ctx, tenantID, opts)
}

func (h *hookedLogs) lastFrom() *domain.ChainCheckpoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.froms[len(h.froms)-1]
}

// process es un proceso de audit: su caso de uso, su identidad ante las demas replicas y su contexto de
// fondo, que se cancela al matarlo.
type process struct {
	uc   *app.AuditUseCase
	logs *hookedLogs
	kill context.CancelFunc
}

func (e *env) process(t *testing.T) *process {
	t.Helper()
	bg, kill := context.WithCancel(context.Background())
	t.Cleanup(kill)
	cp := &db.ContextPool{}
	logs := &hookedLogs{AuditLogRepo: e.logs}
	uc := app.NewAuditUseCase(app.AuditDeps{
		Logs: logs, Security: e.security, Changes: e.changes, Summary: e.summary, Logger: zap.NewNop(),
		Anchors: e.anchors, AnchorEvents: outboxadapter.NewPublisher(cp), Tx: cp,
		Integrity: app.IntegrityRunConfig{
			Runs: NewIntegrityRunRepo(cp), Background: bg, CheckpointEvery: time.Nanosecond, InlineMaxRows: 1000,
		},
	})
	return &process{uc: uc, logs: logs, kill: kill}
}

func (e *env) runRow(t *testing.T, id uuid.UUID) (status string, result []byte) {
	t.Helper()
	if err := e.admin.QueryRow(context.Background(), `SELECT status, result FROM audit.integrity_runs WHERE id = $1`, id).Scan(&status, &result); err != nil {
		t.Fatal(err)
	}
	return status, result
}

func (e *env) waitRun(t *testing.T, id uuid.UUID, want string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if status, _ := e.runRow(t, id); status == want {
			return
		}
		if time.Now().After(deadline) {
			status, _ := e.runRow(t, id)
			t.Fatalf("la verificacion esta en %q, se esperaba %q", status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) finished(t *testing.T, p *process, id uuid.UUID) *domain.IntegrityRun {
	t.Helper()
	e.waitRun(t, id, string(domain.RunCompleted))
	run, err := NewIntegrityRunRepo(&db.ContextPool{}).Get(e.ctx, e.tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// seedBoth siembra n filas en audit_logs y m eventos de seguridad, los dos encadenados.
func (e *env) seedBoth(t *testing.T, n, m int) {
	t.Helper()
	e.useKeys(testRing(t, testKeyA, ""))
	e.seedVolume(t, n)
	e.seedEvents(t, m)
}

func TestUnaVerificacionEnSegundoPlanRecorreLasDosCadenasYGuardaSuResultado(t *testing.T) {
	e := setup(t)
	e.seedBoth(t, 4500, 12)
	p := e.process(t)

	run, err := p.uc.StartIntegrityRun(e.ctx, e.tenant, &e.user, domain.RunModeFull, domain.RunTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunRunning || run.TargetSeq != 4500 || run.RequestedBy == nil || *run.RequestedBy != e.user {
		t.Fatalf("recien lanzada: %+v", run)
	}
	done := e.finished(t, p, run.ID)
	if !done.OK() || done.Phase != domain.RunPhaseDone || done.Checked != 4512 || done.CurrentSeq != 4500 || done.FinishedAt == nil {
		t.Fatalf("terminada: %+v", done)
	}
	if done.Result.Checked != 4500 || done.Result.SecurityEvents == nil || done.Result.SecurityEvents.Checked != 12 {
		t.Fatalf("resultado: %+v", done.Result)
	}
	if cp := done.Chains[domain.ChainAuditLogs].Checkpoint; cp == nil || !cp.Complete || cp.Seq != 4500 {
		t.Fatalf("el punto final de la cadena quedo guardado: %+v", cp)
	}
	// El veredicto de la verificacion asincrona es el mismo que da la de la peticion.
	inline := e.verifyAll(t)
	if inline.OK != done.Result.OK || inline.Checked != done.Result.Checked || inline.Head.Hash != done.Result.Head.Hash {
		t.Fatalf("asincrona %+v, sincrona %+v", done.Result, inline)
	}
}

// Dos procesos (dos replicas) lanzan a la vez: la base deja pasar a uno solo.
func TestDosProcesosQueLanzanAlaVezSoloDejanUnaVerificacion(t *testing.T) {
	e := setup(t)
	e.seedBoth(t, 3000, 5)
	procs := []*process{e.process(t), e.process(t), e.process(t)}
	release := make(chan struct{})
	for _, p := range procs {
		p.logs.hook = func(batch int, _ domain.ChainCheckpoint) {
			if batch == 1 {
				<-release
			}
		}
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		started []uuid.UUID
		active  int
		busy    int
	)
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(p *process) {
			defer wg.Done()
			<-start
			run, err := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeFull, domain.RunTriggerManual)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				started = append(started, run.ID)
			case errors.Is(err, domain.ErrRunActive):
				active++
			case errors.Is(err, domain.ErrVerificationBusy):
				busy++
			default:
				t.Errorf("lanzar: %v", err)
			}
		}(procs[i%len(procs)])
	}
	close(start)
	wg.Wait()
	if len(started) != 1 || active+busy != 11 {
		t.Fatalf("arrancaron %d verificaciones, %d recibieron la que estaba en curso y %d encontraron el cupo tomado; se esperaba 1 y 11 entre las dos",
			len(started), active, busy)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.integrity_runs`); n != 1 {
		t.Fatalf("%d filas en integrity_runs", n)
	}
	close(release)
	e.waitRun(t, started[0], string(domain.RunCompleted))
}

func TestSePuedeCancelarUnaVerificacionYLaSiguienteCorre(t *testing.T) {
	e := setup(t)
	e.seedBoth(t, 6500, 3)
	p := e.process(t)
	reached, release := make(chan struct{}), make(chan struct{})
	p.logs.hook = func(batch int, _ domain.ChainCheckpoint) {
		if batch == 2 {
			close(reached)
			<-release
		}
	}
	run, err := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeFull, domain.RunTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	got, err := p.uc.CancelIntegrityRun(e.ctx, e.tenant, run.ID)
	if err != nil || !got.CancelRequested {
		t.Fatalf("cancelar: %v %+v", err, got)
	}
	close(release)
	e.waitRun(t, run.ID, string(domain.RunCancelled))
	cancelled, _ := p.uc.GetIntegrityRun(e.ctx, e.tenant, run.ID)
	if cancelled.Result != nil || cancelled.FinishedAt == nil || cancelled.OK() {
		t.Fatalf("cancelada: %+v", cancelled)
	}

	p.logs.hook = nil
	next, err := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeFull, domain.RunTriggerManual)
	if err != nil {
		t.Fatalf("tras cancelar, la empresa puede verificar otra vez: %v", err)
	}
	if !e.finished(t, p, next.ID).OK() {
		t.Fatal("la siguiente debia dar la cadena por buena")
	}
}

// killedRun deja una verificacion a medias: el proceso muere en el segundo lote de audit_logs (con su
// punto ya guardado) y su latido se envejece, como el de un proceso que lleva mucho sin dar senales.
func killedRun(t *testing.T, e *env) (id uuid.UUID, checkpointSeq int64) {
	t.Helper()
	dying := e.process(t)
	dying.logs.hook = func(batch int, _ domain.ChainCheckpoint) {
		if batch == 2 {
			dying.kill()
			time.Sleep(100 * time.Millisecond)
		}
	}
	run, err := dying.uc.StartIntegrityRun(e.ctx, e.tenant, &e.user, domain.RunModeFull, domain.RunTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		stored, err := NewIntegrityRunRepo(&db.ContextPool{}).Get(e.ctx, e.tenant, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cp := stored.Chains[domain.ChainAuditLogs]; cp != nil && cp.Checkpoint != nil && cp.Checkpoint.Seq >= 4000 {
			// El proceso deja de verificar en cuanto ve su contexto cancelado.
			time.Sleep(400 * time.Millisecond)
			stored, err = NewIntegrityRunRepo(&db.ContextPool{}).Get(e.ctx, e.tenant, run.ID)
			if err != nil || stored.Status != domain.RunRunning || stored.Chains[domain.ChainAuditLogs].Checkpoint.Seq != cp.Checkpoint.Seq {
				t.Fatalf("un proceso que muere no cierra su verificacion ni avanza: %v %+v", err, stored)
			}
			e.tamper(t, `UPDATE audit.integrity_runs SET heartbeat_at = now() - interval '1 hour' WHERE id = $1`, run.ID)
			return run.ID, cp.Checkpoint.Seq
		}
		if time.Now().After(deadline) {
			t.Fatalf("la verificacion no llego al segundo lote: %+v", stored)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// El proceso muere a mitad: otro retoma la verificacion desde su punto y no desde el principio.
func TestUnaVerificacionInterrumpidaSeRetomaDesdeSuPuntoYTerminaIgual(t *testing.T) {
	e := setup(t)
	e.seedBoth(t, 6500, 8)
	id, cpSeq := killedRun(t, e)

	survivor := e.process(t)
	if got, err := survivor.uc.GetIntegrityRun(e.ctx, e.tenant, id); err != nil || got.Status != domain.RunRunning {
		t.Fatalf("retomar: %v %+v", err, got)
	}
	done := e.finished(t, survivor, id)
	if !done.OK() || done.Result.Checked != 6500 || done.Result.SecurityEvents.Checked != 8 || done.Checked != 6508 {
		t.Fatalf("resultado tras retomar: %+v checked %d", done.Result, done.Checked)
	}
	from := survivor.logs.lastFrom()
	if from == nil || from.Seq != cpSeq || from.Seq < 4000 {
		t.Fatalf("la retomada debia continuar en el punto %d, no desde cero: %+v", cpSeq, from)
	}
	if n := e.count(t, `SELECT count(*) FROM audit.integrity_runs`); n != 1 {
		t.Fatalf("retomar no abre otra verificacion: %d filas", n)
	}
}

// La reanudacion no es un atajo para esconder una manipulacion: lo que se edita o se borra en el punto
// o despues de el se detecta.
func TestLaManipulacionSeDetectaTambienAlReanudar(t *testing.T) {
	casos := []struct {
		nombre string
		tamper func(t *testing.T, e *env, cpSeq int64)
		reason string
		seq    func(cpSeq int64) int64
	}{
		{
			nombre: "fila posterior al punto editada",
			tamper: func(t *testing.T, e *env, cpSeq int64) {
				e.tamper(t, `UPDATE audit.audit_logs SET action = 'editada' WHERE seq = $1`, cpSeq+500)
			},
			reason: domain.ReasonChainBroken, seq: func(cp int64) int64 { return cp + 500 },
		},
		{
			nombre: "la fila del punto editada",
			tamper: func(t *testing.T, e *env, cpSeq int64) {
				e.tamper(t, `UPDATE audit.audit_logs SET action = 'editada' WHERE seq = $1`, cpSeq)
			},
			reason: domain.ReasonChainBroken, seq: func(cp int64) int64 { return cp },
		},
		{
			nombre: "el hash de la fila del punto reescrito",
			tamper: func(t *testing.T, e *env, cpSeq int64) {
				e.tamper(t, `UPDATE audit.audit_logs SET entry_hash = repeat('a', 64) WHERE seq = $1`, cpSeq)
			},
			reason: domain.ReasonChainBroken, seq: func(cp int64) int64 { return cp },
		},
		{
			nombre: "la fila del punto borrada",
			tamper: func(t *testing.T, e *env, cpSeq int64) {
				e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq = $1`, cpSeq)
			},
			reason: domain.ReasonCheckpointMismatch, seq: func(cp int64) int64 { return cp },
		},
		{
			nombre: "las filas posteriores al punto borradas",
			tamper: func(t *testing.T, e *env, cpSeq int64) {
				e.tamper(t, `DELETE FROM audit.audit_logs WHERE seq > $1 AND seq <= $2`, cpSeq, cpSeq+100)
			},
			reason: domain.ReasonChainBroken, seq: func(cp int64) int64 { return cp + 101 },
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := setup(t)
			e.seedBoth(t, 6500, 4)
			id, cpSeq := killedRun(t, e)
			c.tamper(t, e, cpSeq)

			survivor := e.process(t)
			if _, err := survivor.uc.GetIntegrityRun(e.ctx, e.tenant, id); err != nil {
				t.Fatal(err)
			}
			done := e.finished(t, survivor, id)
			if done.OK() || done.Result.Chain != domain.ChainAuditLogs || done.Result.Reason != c.reason {
				t.Fatalf("la manipulacion no se detecto al reanudar: %+v", done.Result)
			}
			if done.Result.BrokenSeq == nil || *done.Result.BrokenSeq != c.seq(cpSeq) {
				t.Fatalf("rota en %v, se esperaba %d", done.Result.BrokenSeq, c.seq(cpSeq))
			}
		})
	}
}

// El limite que hay que conocer: reanudar (o verificar de forma incremental) confia en lo que ya se
// verifico. Una fila ANTERIOR al punto editada despues solo la ve una verificacion completa, que por
// eso el barrido periodico repite cada FullEvery.
func TestLaIncrementalConfiaEnLoAnteriorAlPuntoYLaCompletaLoRelee(t *testing.T) {
	e := setup(t)
	e.seedBoth(t, 3000, 3)
	p := e.process(t)
	first, err := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeFull, domain.RunTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !e.finished(t, p, first.ID).OK() {
		t.Fatal("la primera debia dar la cadena por buena")
	}

	e.seedVolume(t, 200)
	e.tamper(t, `UPDATE audit.audit_logs SET action = 'editada antes del punto' WHERE seq = 100`)

	inc, err := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeIncremental, domain.RunTriggerSweep)
	if err != nil || inc.Mode != domain.RunModeIncremental {
		t.Fatalf("incremental: %v %+v", err, inc)
	}
	done := e.finished(t, p, inc.ID)
	from := p.logs.lastFrom()
	if from == nil || from.Seq != 3000 {
		t.Fatalf("la incremental parte del final de la anterior: %+v", from)
	}
	if !done.OK() || done.Result.Checked != 3200 {
		t.Fatalf("la incremental no relee lo anterior al punto: %+v", done.Result)
	}

	full, err := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeFull, domain.RunTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	fullDone := e.finished(t, p, full.ID)
	if fullDone.OK() || fullDone.Result.BrokenSeq == nil || *fullDone.Result.BrokenSeq != 100 {
		t.Fatalf("la completa debia detectar la fila 100: %+v", fullDone.Result)
	}

	// La ultima terminada esta rota: la siguiente incremental es completa (no parte del punto de la
	// anterior, buena pero anterior a la rotura) y sigue viendo la rotura.
	again, err := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeIncremental, domain.RunTriggerSweep)
	if err != nil || again.Mode != domain.RunModeFull {
		t.Fatalf("tras una cadena rota: %v %+v", err, again)
	}
	if e.finished(t, p, again.ID).OK() {
		t.Fatal("la rotura debia seguir viendose")
	}
}

func TestLaIncrementalDetectaLoEditadoDespuesDelPunto(t *testing.T) {
	e := setup(t)
	e.seedBoth(t, 2500, 3)
	p := e.process(t)
	first, _ := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeFull, domain.RunTriggerManual)
	e.finished(t, p, first.ID)

	e.seedVolume(t, 300)
	e.tamper(t, `UPDATE audit.audit_logs SET action = 'editada' WHERE seq = 2600`)
	inc, _ := p.uc.StartIntegrityRun(e.ctx, e.tenant, nil, domain.RunModeIncremental, domain.RunTriggerSweep)
	done := e.finished(t, p, inc.ID)
	if done.OK() || done.Result.BrokenSeq == nil || *done.Result.BrokenSeq != 2600 {
		t.Fatalf("%+v", done.Result)
	}
}

// El barrido periodico verifica las empresas que le tocan y avisa de la rota.
func TestElBarridoVerificaYAvisaDeLaCadenaRota(t *testing.T) {
	e := setup(t)
	e.seedBoth(t, 2500, 3)
	p := e.process(t)
	out, err := p.uc.SweepIntegrity(e.ctx, e.tenant)
	if err != nil || out.Run == nil || out.Broken || out.Run.Mode != domain.RunModeFull {
		t.Fatalf("primera pasada: %v %+v", err, out)
	}
	e.seedVolume(t, 100)
	out, err = p.uc.SweepIntegrity(e.ctx, e.tenant)
	if err != nil || out.Run == nil || out.Broken || out.Run.Mode != domain.RunModeIncremental {
		t.Fatalf("segunda pasada: %v %+v", err, out)
	}
	e.seedVolume(t, 50)
	e.tamper(t, `UPDATE audit.audit_logs SET module = 'x' WHERE seq = 2620`)
	out, err = p.uc.SweepIntegrity(e.ctx, e.tenant)
	if err != nil || !out.Broken {
		t.Fatalf("tercera pasada: %v %+v", err, out)
	}
}

// Las verificaciones son de su empresa y una empresa no ve ni detiene las de otra; cada una tiene la
// suya en curso a la vez.
func TestLasVerificacionesSonDeSuEmpresa(t *testing.T) {
	e := setup(t)
	repo := NewIntegrityRunRepo(&db.ContextPool{})
	other := uuid.New()
	mine := &domain.IntegrityRun{ID: uuid.New(), TenantID: e.tenant, Mode: domain.RunModeFull, Trigger: domain.RunTriggerManual, Phase: domain.RunPhaseAuditLogs, Owner: "a"}
	theirs := &domain.IntegrityRun{ID: uuid.New(), TenantID: other, Mode: domain.RunModeFull, Trigger: domain.RunTriggerManual, Phase: domain.RunPhaseAuditLogs, Owner: "b"}
	for _, r := range []*domain.IntegrityRun{mine, theirs} {
		if _, err := repo.Open(e.ctx, r); err != nil {
			t.Fatalf("cada empresa abre la suya: %v", err)
		}
	}
	if _, err := repo.Get(e.ctx, other, mine.ID); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("leer la de otra: %v", err)
	}
	if ok, err := repo.RequestCancel(e.ctx, other, mine.ID); err != nil || ok {
		t.Fatalf("cancelar la de otra: %v %v", ok, err)
	}
	if _, claimed, err := repo.Claim(e.ctx, other, mine.ID, "b", time.Nanosecond); err != nil || claimed {
		t.Fatalf("retomar la de otra: %v %v", claimed, err)
	}
	if err := repo.Finish(e.ctx, other, mine.ID, "a", domain.RunFinish{Status: domain.RunCancelled}); !errors.Is(err, domain.ErrRunLost) {
		t.Fatalf("cerrar la de otra: %v", err)
	}
	if active, _ := repo.Active(e.ctx, e.tenant); active == nil || active.ID != mine.ID {
		t.Fatalf("activa de la empresa: %+v", active)
	}
	if runs, _ := repo.List(e.ctx, e.tenant, 10); len(runs) != 1 {
		t.Fatalf("listado de la empresa: %d", len(runs))
	}
}

func TestSoloElDuenoActualizaYCierraYLaRetomaExigeLatidoVencido(t *testing.T) {
	e := setup(t)
	repo := NewIntegrityRunRepo(&db.ContextPool{})
	run := &domain.IntegrityRun{ID: uuid.New(), TenantID: e.tenant, Mode: domain.RunModeFull, Trigger: domain.RunTriggerManual, Phase: domain.RunPhaseAuditLogs, Owner: "a"}
	if _, err := repo.Open(e.ctx, run); err != nil {
		t.Fatal(err)
	}
	progress := domain.RunProgress{Phase: domain.RunPhaseSecurityEvents, Checked: 10, CurrentSeq: 10, Chains: map[domain.ChainName]*domain.ChainState{
		domain.ChainAuditLogs: {Checkpoint: &domain.ChainCheckpoint{Seq: 10, Hash: "h", HashVersion: 2, Checked: 10}},
	}}
	if err := repo.Progress(e.ctx, e.tenant, run.ID, "intruso", progress); !errors.Is(err, domain.ErrRunLost) {
		t.Fatalf("progreso de quien no es el dueno: %v", err)
	}
	if err := repo.Progress(e.ctx, e.tenant, run.ID, "a", progress); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := repo.Claim(e.ctx, e.tenant, run.ID, "b", time.Hour); ok {
		t.Fatal("con el latido reciente nadie la retoma")
	}
	e.tamper(t, `UPDATE audit.integrity_runs SET heartbeat_at = now() - interval '2 hours' WHERE id = $1`, run.ID)
	taken, ok, err := repo.Claim(e.ctx, e.tenant, run.ID, "b", time.Hour)
	if err != nil || !ok || taken.Owner != "b" || taken.Chains[domain.ChainAuditLogs].Checkpoint.Seq != 10 {
		t.Fatalf("retomar: %v %v %+v", ok, err, taken)
	}
	if err := repo.Progress(e.ctx, e.tenant, run.ID, "a", progress); !errors.Is(err, domain.ErrRunLost) {
		t.Fatalf("el dueno anterior ya no actualiza: %v", err)
	}
	if err := repo.Finish(e.ctx, e.tenant, run.ID, "a", domain.RunFinish{Status: domain.RunCompleted}); !errors.Is(err, domain.ErrRunLost) {
		t.Fatalf("el dueno anterior ya no cierra: %v", err)
	}
	if ok, _ := repo.RequestCancel(e.ctx, e.tenant, run.ID); !ok {
		t.Fatal("cancelar una en curso")
	}
	if err := repo.Progress(e.ctx, e.tenant, run.ID, "b", progress); !errors.Is(err, domain.ErrRunCancelled) {
		t.Fatalf("el dueno se entera de la cancelacion: %v", err)
	}
	if err := repo.Finish(e.ctx, e.tenant, run.ID, "b", domain.RunFinish{Status: domain.RunCancelled}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := repo.RequestCancel(e.ctx, e.tenant, run.ID); ok {
		t.Fatal("no se cancela una terminada")
	}
}

// El registro de quien verifico es de solo anadir para el rol del servicio, como el rastro: no borra
// filas ni edita quien la lanzo, cuando ni con que modo.
func TestElRolDelServicioNoBorraNiReescribeElRegistroDeVerificaciones(t *testing.T) {
	e := setup(t)
	repo := NewIntegrityRunRepo(&db.ContextPool{})
	run := &domain.IntegrityRun{ID: uuid.New(), TenantID: e.tenant, Mode: domain.RunModeFull, Trigger: domain.RunTriggerManual, RequestedBy: &e.user, Phase: domain.RunPhaseAuditLogs, Owner: "a"}
	if _, err := repo.Open(e.ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`DELETE FROM audit.integrity_runs`,
		`TRUNCATE audit.integrity_runs`,
		`UPDATE audit.integrity_runs SET requested_by = gen_random_uuid()`,
		`UPDATE audit.integrity_runs SET mode = 'incremental'`,
		`UPDATE audit.integrity_runs SET started_at = now()`,
		`UPDATE audit.integrity_runs SET tenant_id = gen_random_uuid()`,
	} {
		_, err := e.svc.Exec(context.Background(), sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: %v; se esperaba permission denied", sql, err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit.integrity_runs WHERE requested_by = $1`, e.user); n != 1 {
		t.Fatalf("el registro cambio: %d", n)
	}
}
