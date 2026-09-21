package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// runLease es lo que puede pasar sin que una verificacion en curso deje un latido antes de darla
	// por abandonada (el proceso murio o se reinicio). El latido se escribe con cada punto de
	// reanudacion, cada CheckpointEvery: la holgura cubre un lote lento sin retomarla por error.
	runLease = 90 * time.Second

	DefaultCheckpointEvery = 2 * time.Second
	DefaultRunTimeout      = 12 * time.Hour
	// DefaultInlineMaxRows es el tamano de cadena (suma de las posiciones de cabeza de las dos) hasta
	// el que GET /integrity contesta dentro de la peticion: a unos 200000 filas/s de medicion, cabe de
	// sobra en su plazo aun en una maquina cinco veces mas lenta.
	DefaultInlineMaxRows = 200_000
	DefaultFullEvery     = 7 * 24 * time.Hour

	runListLimit = 20
	// finishTimeout es lo que se da a cerrar una verificacion cuyo contexto ya termino.
	finishTimeout = 15 * time.Second
)

// IntegrityRunConfig fija cuando y como corren las verificaciones de cadena en segundo plano.
type IntegrityRunConfig struct {
	Runs    ports.IntegrityRunRepository
	Metrics ports.IntegrityMetrics
	// Background gobierna las verificaciones: al cancelarse (el servicio se apaga) se detienen dejando
	// su punto de reanudacion, y otro proceso, o este al volver, las retoma. Nil no las cancela nunca.
	Background      context.Context
	RunTimeout      time.Duration
	CheckpointEvery time.Duration
	InlineMaxRows   int64
	// FullEvery es cada cuanto el barrido periodico hace una verificacion completa en vez de
	// incremental: la incremental confia en lo ya verificado, y solo la completa relee las filas
	// antiguas.
	FullEvery time.Duration
}

type integrityRuns struct {
	IntegrityRunConfig
	// owner identifica a este proceso ante las demas replicas: es quien puede actualizar y cerrar
	// las verificaciones que abrio o retomo.
	owner string
}

func newIntegrityRuns(c IntegrityRunConfig) integrityRuns {
	if c.Background == nil {
		c.Background = context.Background()
	}
	if c.RunTimeout <= 0 {
		c.RunTimeout = DefaultRunTimeout
	}
	if c.CheckpointEvery <= 0 {
		c.CheckpointEvery = DefaultCheckpointEvery
	}
	if c.InlineMaxRows <= 0 {
		c.InlineMaxRows = DefaultInlineMaxRows
	}
	if c.FullEvery <= 0 {
		c.FullEvery = DefaultFullEvery
	}
	return integrityRuns{IntegrityRunConfig: c, owner: uuid.NewString()}
}

// IntegrityCheck es la respuesta de GET /integrity: el veredicto si la cadena cabe en una peticion,
// o la verificacion en segundo plano que lo dara (con la ultima terminada, para no dejar al
// llamador sin nada que mirar mientras tanto).
type IntegrityCheck struct {
	Result        *domain.ChainIntegrity
	Run           *domain.IntegrityRun
	LastCompleted *domain.IntegrityRun
}

// CheckIntegrity verifica la cadena dentro de la peticion si es pequena y, si no, lanza (o
// reutiliza) una verificacion completa en segundo plano. Asi GET /integrity conserva su contrato
// para las cadenas que ya cabian y no se rompe por la cola en las que ya no caben.
func (uc *AuditUseCase) CheckIntegrity(ctx context.Context, tenantID uuid.UUID, requestedBy *uuid.UUID) (*IntegrityCheck, error) {
	if uc.integrity.Runs != nil {
		rows, err := uc.chainRows(ctx)
		if err != nil {
			return nil, err
		}
		if rows > uc.integrity.InlineMaxRows {
			run, err := uc.StartIntegrityRun(ctx, tenantID, requestedBy, domain.RunModeFull, domain.RunTriggerRequest)
			if err != nil && !errors.Is(err, domain.ErrRunActive) {
				return nil, err
			}
			last, err := uc.integrity.Runs.LastCompleted(ctx, tenantID, false, "")
			if err != nil {
				return nil, err
			}
			return &IntegrityCheck{Run: run, LastCompleted: last}, nil
		}
	}
	res, err := uc.VerifyChainIntegrity(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return &IntegrityCheck{Result: res}, nil
}

// chainRows es el tamano de las cadenas: la suma de sus posiciones de cabeza (una cota de sus filas,
// que solo la sobrestima por los huecos de la secuencia).
func (uc *AuditUseCase) chainRows(ctx context.Context) (int64, error) {
	var rows int64
	for _, chain := range anchoredChains {
		head, err := uc.anchors.Head(ctx, chain)
		if err != nil {
			return 0, err
		}
		if head != nil {
			rows += head.Seq
		}
	}
	return rows, nil
}

// StartIntegrityRun lanza una verificacion en segundo plano y la devuelve ya en curso. Si la empresa
// ya tiene una viva devuelve esa junto a domain.ErrRunActive; si tiene una abandonada (su proceso
// murio) la retoma en lugar de abrir otra. domain.ErrVerificationBusy si este proceso esta en su
// tope de verificaciones.
func (uc *AuditUseCase) StartIntegrityRun(ctx context.Context, tenantID uuid.UUID, requestedBy *uuid.UUID, mode domain.RunMode, origin domain.RunTrigger) (*domain.IntegrityRun, error) {
	run, release, err := uc.openRun(ctx, tenantID, requestedBy, mode, origin)
	if err != nil {
		return run, err
	}
	return uc.spawn(ctx, run, release), nil
}

// openRun registra la verificacion y toma su cupo en este proceso. Devuelve el cupo (release) para
// que quien ejecute la verificacion lo suelte al terminar.
func (uc *AuditUseCase) openRun(ctx context.Context, tenantID uuid.UUID, requestedBy *uuid.UUID, mode domain.RunMode, origin domain.RunTrigger) (*domain.IntegrityRun, func(), error) {
	if !mode.Valid() {
		return nil, nil, fmt.Errorf("modo de verificacion %q desconocido", mode)
	}
	runs := uc.integrity.Runs
	active, err := runs.Active(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	if active != nil {
		if adopted, release, ok, err := uc.adopt(ctx, active); err != nil {
			return nil, nil, err
		} else if ok {
			return adopted, release, nil
		}
		return active, nil, domain.ErrRunActive
	}

	release, ok := uc.verifying.acquire(tenantID)
	if !ok {
		// Otro lanzamiento de esta misma empresa acaba de tomar el cupo: si ya la abrio, es esa.
		if active, err := runs.Active(ctx, tenantID); err == nil && active != nil {
			return active, nil, domain.ErrRunActive
		}
		return nil, nil, domain.ErrVerificationBusy
	}
	mode, chains, err := uc.startingPoint(ctx, tenantID, mode)
	if err != nil {
		release()
		return nil, nil, err
	}
	head, err := uc.anchors.Head(ctx, domain.ChainAuditLogs)
	if err != nil {
		release()
		return nil, nil, err
	}
	run := &domain.IntegrityRun{
		ID: uuid.New(), TenantID: tenantID, Mode: mode, Trigger: origin, RequestedBy: requestedBy,
		Phase: domain.RunPhaseAuditLogs, Owner: uc.integrity.owner, Chains: chains,
	}
	if head != nil {
		run.TargetSeq = head.Seq
	}
	stored, err := runs.Open(ctx, run)
	if err != nil {
		release()
		return stored, nil, err
	}
	return stored, release, nil
}

// startingPoint da el modo real y el estado de partida de cada cadena. La incremental parte del
// punto final de la ULTIMA verificacion terminada, y solo si esa dio la cadena por buena: si la ultima
// la dio por rota, aunque una anterior fuera buena, la cadena esta rota hasta que una completa diga
// lo contrario, y partir del punto de la buena la haria parecer sana. Sin punto de partida, es completa.
func (uc *AuditUseCase) startingPoint(ctx context.Context, tenantID uuid.UUID, mode domain.RunMode) (domain.RunMode, map[domain.ChainName]*domain.ChainState, error) {
	chains := map[domain.ChainName]*domain.ChainState{}
	if mode == domain.RunModeFull {
		return mode, chains, nil
	}
	last, err := uc.integrity.Runs.LastCompleted(ctx, tenantID, false, "")
	if err != nil {
		return mode, nil, err
	}
	if last != nil && last.OK() {
		for _, chain := range anchoredChains {
			if st := last.Chains[chain]; st != nil && st.Checkpoint != nil {
				cp := *st.Checkpoint
				chains[chain] = &domain.ChainState{Checkpoint: &cp}
			}
		}
	}
	if len(chains) == 0 {
		return domain.RunModeFull, chains, nil
	}
	return mode, chains, nil
}

// adopt toma una verificacion abandonada: su latido vencio, asi que su proceso murio. ok es false si
// la verificacion sigue viva, si ya es de este proceso o si este no tiene cupo.
func (uc *AuditUseCase) adopt(ctx context.Context, run *domain.IntegrityRun) (*domain.IntegrityRun, func(), bool, error) {
	if run.Finished() || time.Since(run.HeartbeatAt) <= runLease {
		return nil, nil, false, nil
	}
	release, ok := uc.verifying.acquire(run.TenantID)
	if !ok {
		return nil, nil, false, nil
	}
	claimed, ok, err := uc.integrity.Runs.Claim(ctx, run.TenantID, run.ID, uc.integrity.owner, runLease)
	if err != nil || !ok {
		release()
		return nil, nil, false, err
	}
	uc.logger.Warn("audit: se retoma una verificacion de la cadena abandonada",
		zap.String("tenant_id", run.TenantID.String()), zap.String("run_id", run.ID.String()), zap.Int64("current_seq", run.CurrentSeq))
	return claimed, release, true, nil
}

// spawn ejecuta la verificacion en segundo plano, fuera de la vida de la peticion que la lanzo y
// atada a la del servicio. La ejecucion es la duena de run y lo va modificando: quien llama recibe
// una copia del estado con el que arranco.
func (uc *AuditUseCase) spawn(ctx context.Context, run *domain.IntegrityRun, release func()) *domain.IntegrityRun {
	snapshot := *run
	bg, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(uc.integrity.Background, cancel)
	go func() {
		defer func() { stop(); cancel() }()
		uc.execute(bg, run, release)
	}()
	return &snapshot
}

// GetIntegrityRun devuelve una verificacion de la empresa. Si esta abandonada (su proceso murio)
// la retoma aqui mismo: quien la mira ve como sigue.
func (uc *AuditUseCase) GetIntegrityRun(ctx context.Context, tenantID, id uuid.UUID) (*domain.IntegrityRun, error) {
	run, err := uc.integrity.Runs.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	adopted, release, ok, err := uc.adopt(ctx, run)
	if err != nil {
		return nil, err
	}
	if ok {
		return uc.spawn(ctx, adopted, release), nil
	}
	return run, nil
}

func (uc *AuditUseCase) ListIntegrityRuns(ctx context.Context, tenantID uuid.UUID) ([]*domain.IntegrityRun, error) {
	return uc.integrity.Runs.List(ctx, tenantID, runListLimit)
}

// CancelIntegrityRun pide detener una verificacion en curso: su proceso lo ve en el siguiente punto
// de reanudacion y la cierra como cancelada. Cancelar una ya terminada no hace nada.
func (uc *AuditUseCase) CancelIntegrityRun(ctx context.Context, tenantID, id uuid.UUID) (*domain.IntegrityRun, error) {
	run, err := uc.integrity.Runs.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if run.Finished() {
		return run, nil
	}
	if _, err := uc.integrity.Runs.RequestCancel(ctx, tenantID, id); err != nil {
		return nil, err
	}
	// Una verificacion abandonada no tiene quien la detenga: se retoma para que lo vea y se cierre.
	if adopted, release, ok, err := uc.adopt(ctx, run); err != nil {
		return nil, err
	} else if ok {
		uc.spawn(ctx, adopted, release)
	}
	return uc.integrity.Runs.Get(ctx, tenantID, id)
}

// SweepOutcome es lo que dejo el barrido periodico en una empresa.
type SweepOutcome struct {
	// Run es la verificacion que corrio; nil si no hizo falta o no se pudo lanzar.
	Run *domain.IntegrityRun
	// Broken es verdadero si la verificacion termino y la cadena esta rota.
	Broken bool
}

// SweepIntegrity es un paso del barrido periodico sobre una empresa: retoma una verificacion
// abandonada o lanza una nueva, incremental salvo que la ultima completa correcta sea mas vieja que
// FullEvery, y la espera. Va en el hilo del barrido a proposito: asi el barrido verifica una empresa
// a la vez y su ritmo lo marca el propio recorrido. Si el contexto vence a medias, la verificacion
// queda en curso con su punto y la siguiente pasada la retoma.
func (uc *AuditUseCase) SweepIntegrity(ctx context.Context, tenantID uuid.UUID) (SweepOutcome, error) {
	runs := uc.integrity.Runs
	active, err := runs.Active(ctx, tenantID)
	if err != nil {
		return SweepOutcome{}, err
	}
	var (
		run     *domain.IntegrityRun
		release func()
	)
	if active != nil {
		adopted, rel, ok, err := uc.adopt(ctx, active)
		if err != nil || !ok {
			return SweepOutcome{}, err
		}
		run, release = adopted, rel
	} else {
		mode := domain.RunModeIncremental
		lastFull, err := runs.LastCompleted(ctx, tenantID, true, domain.RunModeFull)
		if err != nil {
			return SweepOutcome{}, err
		}
		if lastFull == nil || lastFull.FinishedAt == nil || time.Since(*lastFull.FinishedAt) >= uc.integrity.FullEvery {
			mode = domain.RunModeFull
		}
		run, release, err = uc.openRun(ctx, tenantID, nil, mode, domain.RunTriggerSweep)
		if errors.Is(err, domain.ErrRunActive) || errors.Is(err, domain.ErrVerificationBusy) {
			return SweepOutcome{}, nil
		}
		if err != nil {
			return SweepOutcome{}, err
		}
	}
	res := uc.execute(ctx, run, release)
	return SweepOutcome{Run: run, Broken: res != nil && !res.OK}, nil
}

// execute verifica hasta el final y cierra la verificacion. Devuelve el veredicto si la verificacion
// termino (acierte o no); nil si se cancelo, fallo o quedo pendiente de retomarse.
func (uc *AuditUseCase) execute(ctx context.Context, run *domain.IntegrityRun, release func()) (verdict *domain.ChainIntegrity) {
	defer release()
	runCtx, cancel := context.WithTimeout(ctx, uc.integrity.RunTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			uc.logger.Error("audit: la verificacion de la cadena hizo panic",
				zap.String("run_id", run.ID.String()), zap.Any("panic", r), zap.Stack("stack"))
			uc.closeRun(ctx, run, domain.RunFinish{Status: domain.RunFailed, ErrorCode: domain.RunErrorInternal, Chains: run.Chains}, domain.RunOutcomeFailed)
			verdict = nil
		}
	}()

	var last time.Time
	// El primer punto es inmediato: refresca el latido de una verificacion retomada y ve una
	// cancelacion pedida mientras estuvo abandonada.
	err := uc.reportRun(runCtx, run, run.Phase, true, &last)
	if err == nil {
		verdict, err = uc.verifyRun(runCtx, run, &last)
	}
	switch {
	case err == nil:
		uc.closeRun(ctx, run, domain.RunFinish{Status: domain.RunCompleted, Result: verdict, Chains: run.Chains}, outcomeOf(verdict))
		if !verdict.OK {
			uc.logger.Error("audit: la verificacion de la cadena encontro una rotura",
				zap.String("tenant_id", run.TenantID.String()), zap.String("run_id", run.ID.String()),
				zap.String("chain", string(verdict.Chain)), zap.String("reason", verdict.Reason), zap.Any("broken_seq", verdict.BrokenSeq))
		}
		return verdict
	case errors.Is(err, domain.ErrRunCancelled):
		uc.closeRun(ctx, run, domain.RunFinish{Status: domain.RunCancelled, Chains: run.Chains}, domain.RunOutcomeCancelled)
	case errors.Is(err, domain.ErrRunLost):
		uc.logger.Warn("audit: otro proceso retomo la verificacion de la cadena; este la deja",
			zap.String("run_id", run.ID.String()))
	case runCtx.Err() != nil && ctx.Err() == nil:
		uc.logger.Error("audit: la verificacion de la cadena excedio su plazo", zap.String("run_id", run.ID.String()))
		uc.closeRun(ctx, run, domain.RunFinish{Status: domain.RunFailed, ErrorCode: domain.RunErrorTimeout, Chains: run.Chains}, domain.RunOutcomeFailed)
	case ctx.Err() != nil:
		uc.logger.Info("audit: verificacion de la cadena interrumpida; queda en curso y se retoma desde su punto",
			zap.String("run_id", run.ID.String()), zap.Int64("current_seq", run.CurrentSeq))
	default:
		uc.logger.Error("audit: la verificacion de la cadena fallo", zap.String("run_id", run.ID.String()), zap.Error(err))
		uc.closeRun(ctx, run, domain.RunFinish{Status: domain.RunFailed, ErrorCode: domain.RunErrorInternal, Chains: run.Chains}, domain.RunOutcomeFailed)
	}
	return nil
}

func outcomeOf(v *domain.ChainIntegrity) string {
	if v.OK {
		return domain.RunOutcomeOK
	}
	return domain.RunOutcomeBroken
}

// closeRun cierra la verificacion con un contexto propio: el de la verificacion puede ser el que
// acaba de vencer.
func (uc *AuditUseCase) closeRun(ctx context.Context, run *domain.IntegrityRun, f domain.RunFinish, outcome string) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := uc.integrity.Runs.Finish(fctx, run.TenantID, run.ID, uc.integrity.owner, f); err != nil {
		uc.logger.Error("audit: no se pudo cerrar la verificacion de la cadena; queda en curso y otro proceso la retomara",
			zap.String("run_id", run.ID.String()), zap.Error(err))
		return
	}
	if uc.integrity.Metrics != nil {
		uc.integrity.Metrics.RunFinished(run.Trigger, outcome)
	}
}

// chainReader es lo que la verificacion necesita de cada cadena.
type chainReader interface {
	VerifyChain(ctx context.Context, tenantID uuid.UUID, opts domain.VerifyOptions) (*domain.ChainIntegrity, error)
}

func (uc *AuditUseCase) readerFor(chain domain.ChainName) chainReader {
	if chain == domain.ChainSecurityEvents {
		return uc.security
	}
	return uc.logs
}

// verifyRun recorre las cadenas que faltan (las ya terminadas de una verificacion retomada no se
// repiten), anota un punto de reanudacion cada CheckpointEvery y concluye contrastando con las anclas.
func (uc *AuditUseCase) verifyRun(ctx context.Context, run *domain.IntegrityRun, last *time.Time) (*domain.ChainIntegrity, error) {
	if run.Chains == nil {
		run.Chains = map[domain.ChainName]*domain.ChainState{}
	}
	for _, chain := range anchoredChains {
		st := run.Chains[chain]
		if st == nil {
			st = &domain.ChainState{}
			run.Chains[chain] = st
		}
		if st.Result != nil {
			continue
		}
		phase := string(chain)
		res, err := uc.readerFor(chain).VerifyChain(ctx, run.TenantID, domain.VerifyOptions{
			From: st.Checkpoint,
			OnBatch: func(cp domain.ChainCheckpoint) error {
				st.Checkpoint = &cp
				return uc.reportRun(ctx, run, phase, false, last)
			},
		})
		if err != nil {
			return nil, err
		}
		st.Result = res
		if res.Checkpoint != nil {
			st.Checkpoint = res.Checkpoint
		}
		next := domain.RunPhaseAnchors
		if chain == domain.ChainAuditLogs {
			next = domain.RunPhaseSecurityEvents
		}
		if err := uc.reportRun(ctx, run, next, true, last); err != nil {
			return nil, err
		}
	}
	return uc.conclude(ctx, run.Chains[domain.ChainAuditLogs].Result, run.Chains[domain.ChainSecurityEvents].Result)
}

// reportRun escribe el avance y el latido, como mucho una vez por CheckpointEvery salvo force. Es
// tambien donde la verificacion se entera de que se le pidio cancelar o de que otro proceso la tomo.
func (uc *AuditUseCase) reportRun(ctx context.Context, run *domain.IntegrityRun, phase string, force bool, last *time.Time) error {
	if !force && time.Since(*last) < uc.integrity.CheckpointEvery {
		return nil
	}
	*last = time.Now()
	run.Phase = phase
	run.Checked, run.CurrentSeq = 0, 0
	for chain, st := range run.Chains {
		var cp *domain.ChainCheckpoint
		switch {
		case st.Result != nil:
			run.Checked += int64(st.Result.Checked)
			cp = st.Checkpoint
		case st.Checkpoint != nil:
			run.Checked += int64(st.Checkpoint.Checked)
			cp = st.Checkpoint
		}
		if chain == domain.ChainAuditLogs && cp != nil {
			run.CurrentSeq = cp.Seq
		}
	}
	return uc.integrity.Runs.Progress(ctx, run.TenantID, run.ID, uc.integrity.owner, domain.RunProgress{
		Phase: phase, Checked: run.Checked, CurrentSeq: run.CurrentSeq, Chains: run.Chains,
	})
}
