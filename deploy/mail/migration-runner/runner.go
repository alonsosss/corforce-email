package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	maxClaimBackoff = time.Minute
	minBeatInterval = time.Second
	beatTimeout     = 20 * time.Second
	scanWatchPeriod = time.Second
)

// Runner reclama un trabajo cada vez, valida su origen, corre imapsync en dos pasadas y cierra el
// trabajo. No toca base de datos, Redis ni NATS: solo habla con la API del ejecutor de
// mail-migration, con Dovecot (por imapsync) y con clamd (por el filtro).
type Runner struct {
	cfg   Config
	api   *API
	guard *SourceGuard
	log   *slog.Logger

	// completeBase y completeBudget acotan los reintentos del cierre; las pruebas los acortan.
	completeBase   time.Duration
	completeBudget time.Duration
}

func NewRunner(cfg Config, api *API, guard *SourceGuard, log *slog.Logger) *Runner {
	return &Runner{cfg: cfg, api: api, guard: guard, log: log, completeBase: 2 * time.Second, completeBudget: 10 * time.Minute}
}

// Loop sondea POST /v1/claim hasta que ctx termina. Un trabajo a la vez.
func (r *Runner) Loop(ctx context.Context) {
	backoff := r.cfg.PollInterval
	failures := 0
	for ctx.Err() == nil {
		job, err := r.api.Claim(ctx)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			failures++
			if failures == 1 || failures%20 == 0 {
				r.log.Warn("no se pudo reclamar trabajo", "error", err, "fallos_seguidos", failures)
			}
			backoff = min(max(backoff, r.cfg.PollInterval)*2, maxClaimBackoff)
			sleep(ctx, backoff)
		case job == nil:
			failures, backoff = 0, r.cfg.PollInterval
			sleep(ctx, r.cfg.PollInterval)
		default:
			failures, backoff = 0, r.cfg.PollInterval
			r.Execute(ctx, job)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Execute corre un trabajo reclamado hasta cerrarlo o abandonarlo. Se abandona (sin cerrar) cuando
// el arrendamiento se perdio o el proceso se esta apagando: el servicio lo recupera al vencer.
func (r *Runner) Execute(ctx context.Context, job *ClaimedJob) {
	log := r.log.With("job_id", job.JobID, "tenant_id", job.TenantID, "attempt", job.Attempt)
	log.Info("trabajo reclamado")

	jobCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	track := &tracker{}
	beats := &heartbeater{api: r.api, job: job, track: track, cancel: cancel, log: log, lease: time.Duration(job.LeaseSeconds) * time.Second}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); beats.loop(jobCtx) }()

	result := r.process(jobCtx, job, track, beats)

	cause := context.Cause(jobCtx)
	cancel(nil)
	wg.Wait()

	switch {
	case ctx.Err() != nil:
		log.Warn("trabajo abandonado: el ejecutor se apaga; el servicio lo recuperara al vencer el arrendamiento")
		return
	case errors.Is(cause, errLeaseLost):
		log.Warn("trabajo abandonado: el arrendamiento ya no es de este ejecutor")
		return
	}
	r.complete(ctx, log, job, result)
}

// jobResult es lo que se informa al cerrar.
type jobResult struct {
	Outcome  string
	Progress Progress
	Err      *JobError
}

func (r *Runner) process(ctx context.Context, job *ClaimedJob, track *tracker, beats *heartbeater) jobResult {
	if err := job.validate(); err != nil {
		r.log.Error("trabajo con datos no validos", "job_id", job.JobID, "motivo", err.Error())
		return jobResult{Outcome: outcomeFailed, Err: &JobError{Code: codeImapsyncFailed, Message: "El ejecutor recibio un trabajo con datos no validos."}}
	}
	if !r.cfg.sourcePortAllowed(job.Source.Port) {
		r.log.Warn("origen rechazado", "job_id", job.JobID, "codigo", codeSourceBlockedAddress, "motivo", "puerto")
		return jobResult{Outcome: outcomeFailed, Err: &JobError{Code: codeSourceBlockedAddress, Message: "El servidor de origen usa un puerto no permitido."}}
	}
	target, err := r.guard.Resolve(ctx, job.Source.Host)
	if err != nil {
		code := codeSourceUnreachable
		msg := "El servidor de origen no se pudo resolver."
		switch {
		case errors.Is(err, errBlockedAddress):
			code, msg = codeSourceBlockedAddress, "El servidor de origen apunta a una direccion no permitida."
		case errors.Is(err, errInvalidHost):
			msg = "El nombre del servidor de origen no es valido."
		}
		r.log.Warn("origen rechazado", "job_id", job.JobID, "codigo", code)
		return jobResult{Outcome: outcomeFailed, Err: &JobError{Code: code, Message: msg}}
	}

	secrets, err := newSecretFiles(r.cfg.WorkDir, job.Source.Password, r.cfg.MasterPass)
	if err != nil {
		r.log.Error("no se pudieron preparar los ficheros del trabajo", "job_id", job.JobID, "error", err)
		return jobResult{Outcome: outcomeFailed, Err: &JobError{Code: codeImapsyncFailed, Message: "No se pudo preparar el trabajo en el ejecutor."}}
	}
	defer secrets.remove()

	spec := passSpec{job: job, target: target, secrets: secrets}
	var base Progress
	var last verdict
	for _, phase := range []string{phaseInitial, phaseCatchup} {
		pass := r.runPass(ctx, spec, phase, base, track, beats)
		base = pass.progress
		last = pass.verdict
		if pass.stop != nil {
			return jobResult{Outcome: pass.stop.outcome, Progress: base, Err: pass.stop.err}
		}
		if !last.ok() && !last.Partial {
			return jobResult{Outcome: outcomeFailed, Progress: base, Err: last.Err}
		}
	}
	if !last.ok() {
		return jobResult{Outcome: outcomeFailed, Progress: base, Err: last.Err}
	}
	return jobResult{Outcome: outcomeSucceeded, Progress: base}
}

type passStop struct {
	outcome string
	err     *JobError
}

type passResult struct {
	verdict  verdict
	progress Progress
	stop     *passStop
}

// runPass ejecuta una pasada de imapsync con su plazo y devuelve lo que paso: el veredicto, el
// progreso acumulado y, si la pasada no puede seguir por causas ajenas a imapsync (cancelada,
// plazo vencido, arrendamiento perdido), la razon.
func (r *Runner) runPass(ctx context.Context, spec passSpec, phase string, base Progress, track *tracker, beats *heartbeater) passResult {
	parser := newOutputParser()
	track.begin(phase, base, parser)
	if beats.beat(ctx) {
		return passResult{progress: base, stop: r.stopFor(ctx)}
	}

	passCtx, cancel := context.WithTimeoutCause(ctx, r.cfg.JobTimeout, context.DeadlineExceeded)
	defer cancel()
	go watchScanner(passCtx, parser, func() { cancel() })

	exit, runErr := execImapsync(passCtx, r.cfg, spec, parser)
	progress := combine(base, parser.Progress())
	track.finish(progress)

	if ctx.Err() != nil {
		return passResult{progress: progress, stop: r.stopFor(ctx)}
	}
	if passCtx.Err() != nil {
		if errors.Is(context.Cause(passCtx), context.DeadlineExceeded) {
			r.log.Warn("pasada detenida por plazo", "job_id", spec.job.JobID, "fase", phase)
			return passResult{progress: progress, stop: &passStop{outcome: outcomeFailed, err: &JobError{Code: codeTimeout, Message: "La pasada de migracion supero el tiempo maximo."}}}
		}
		return passResult{progress: progress, verdict: fail(codeImapsyncFailed, "El analisis antivirus no esta disponible.", false)}
	}
	if runErr != nil {
		r.log.Error("no se pudo ejecutar imapsync", "job_id", spec.job.JobID, "error", runErr)
		return passResult{progress: progress, verdict: fail(codeImapsyncFailed, "No se pudo ejecutar imapsync.", false)}
	}
	findings := parser.Findings()
	v := classify(exit, findings)
	r.log.Info("pasada terminada", "job_id", spec.job.JobID, "fase", phase, "salida", exit,
		"copiados", progress.MessagesCopied, "omitidos", progress.MessagesSkipped, "fallidos", progress.MessagesFailed,
		"virus", findings.ScanInfected, "sin_analizar", findings.ScanUnavailable,
		"codigo", codeOf(v))
	return passResult{progress: progress, verdict: v}
}

func codeOf(v verdict) string {
	if v.Err == nil {
		return "ok"
	}
	return v.Err.Code
}

// stopFor traduce la causa de la cancelacion del trabajo en como se cierra.
func (r *Runner) stopFor(ctx context.Context) *passStop {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errCancelRequested):
		return &passStop{outcome: outcomeCancelled}
	default:
		return &passStop{outcome: outcomeFailed, err: &JobError{Code: codeImapsyncFailed, Message: "El ejecutor se detuvo antes de terminar."}}
	}
}

// watchScanner corta la pasada cuando el antivirus cae a mitad: cada mensaje se rechazaria y
// imapsync recorreria el buzon entero sin copiar nada.
func watchScanner(ctx context.Context, parser *outputParser, stop func()) {
	t := time.NewTicker(scanWatchPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if parser.ScanUnavailableCount() >= maxScanUnavailable {
				stop()
				return
			}
		}
	}
}

// combine suma a lo ya copiado en pasadas anteriores lo de la pasada actual. El resto de los
// contadores describe el estado actual del origen y es el de la pasada en curso.
func combine(base, cur Progress) Progress {
	if base.MessagesCopied == 0 && base.BytesCopied == 0 && len(base.Folders) == 0 {
		return cur
	}
	cur.MessagesCopied += base.MessagesCopied
	cur.BytesCopied += base.BytesCopied
	prev := make(map[string]int, len(base.Folders))
	for _, f := range base.Folders {
		prev[f.Name] += f.MessagesCopied
	}
	for i := range cur.Folders {
		cur.Folders[i].MessagesCopied += prev[cur.Folders[i].Name]
	}
	return cur
}

// complete cierra el trabajo con reintentos y espera acotada si el servicio no responde. No
// reintenta lo que no puede cambiar (el arrendamiento perdido o un rechazo definitivo).
func (r *Runner) complete(ctx context.Context, log *slog.Logger, job *ClaimedJob, res jobResult) {
	deadline := time.Now().Add(r.completeBudget)
	delay := r.completeBase
	for attempt := 1; ; attempt++ {
		err := r.api.Complete(ctx, job, res.Outcome, res.Progress, res.Err)
		if err == nil {
			log.Info("trabajo cerrado", "resultado", res.Outcome, "codigo", jobErrCode(res.Err))
			return
		}
		if ctx.Err() != nil {
			log.Warn("cierre interrumpido por el apagado; el servicio recuperara el trabajo al vencer el arrendamiento")
			return
		}
		if isLeaseLost(err) {
			log.Warn("el trabajo ya no es de este ejecutor; no se cierra")
			return
		}
		if !retryable(err) || time.Now().Add(delay).After(deadline) {
			log.Error("no se pudo cerrar el trabajo", "error", err, "intentos", attempt)
			return
		}
		log.Warn("cierre del trabajo fallido, se reintenta", "error", err, "intento", attempt, "espera", delay.String())
		sleep(ctx, delay)
		delay = min(delay*2, maxClaimBackoff)
	}
}

func jobErrCode(e *JobError) string {
	if e == nil {
		return ""
	}
	return e.Code
}

// tracker guarda la fase y el progreso que el latido informa.
type tracker struct {
	mu     sync.Mutex
	phase  string
	base   Progress
	parser *outputParser
	final  *Progress
}

func (t *tracker) begin(phase string, base Progress, parser *outputParser) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase, t.base, t.parser, t.final = phase, base, parser, nil
}

func (t *tracker) finish(p Progress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.final = &p
}

func (t *tracker) snapshot() (string, Progress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.final != nil {
		return t.phase, *t.final
	}
	if t.parser == nil {
		return t.phase, t.base
	}
	return t.phase, combine(t.base, t.parser.Progress())
}

// heartbeater extiende el arrendamiento a la mitad de su duracion y recoge la orden de cancelar.
// Si el servicio no responde durante todo el arrendamiento, el trabajo se da por perdido: otro
// ejecutor podria haberlo tomado.
type heartbeater struct {
	api    *API
	job    *ClaimedJob
	track  *tracker
	cancel context.CancelCauseFunc
	log    *slog.Logger

	mu     sync.Mutex
	lease  time.Duration
	lastOK time.Time
}

func (h *heartbeater) interval() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return max(h.lease/2, minBeatInterval)
}

func (h *heartbeater) loop(ctx context.Context) {
	h.mu.Lock()
	h.lastOK = time.Now()
	h.mu.Unlock()
	for {
		t := time.NewTimer(h.interval())
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			h.beat(ctx)
		}
	}
}

// beat envia un latido. Devuelve true cuando el trabajo debe detenerse (cancelacion o arrendamiento
// perdido), y en ese caso ya cancelo el contexto con su causa.
func (h *heartbeater) beat(ctx context.Context) bool {
	phase, progress := h.track.snapshot()
	beatCtx, cancel := context.WithTimeout(ctx, beatTimeout)
	defer cancel()
	reply, err := h.api.Heartbeat(beatCtx, h.job, phase, progress)
	switch {
	case err == nil:
		h.mu.Lock()
		h.lastOK = time.Now()
		if reply.LeaseSeconds >= 10 {
			h.lease = time.Duration(reply.LeaseSeconds) * time.Second
		}
		h.mu.Unlock()
		if reply.Cancel {
			h.log.Info("cancelacion pedida por el servicio")
			h.cancel(errCancelRequested)
			return true
		}
		return false
	case isLeaseLost(err):
		h.cancel(errLeaseLost)
		return true
	case ctx.Err() != nil:
		return false
	}
	h.mu.Lock()
	expired := time.Since(h.lastOK) > h.lease
	h.mu.Unlock()
	h.log.Warn("latido fallido", "error", err)
	if expired {
		h.log.Error("sin latido aceptado durante todo el arrendamiento; se abandona el trabajo")
		h.cancel(errLeaseLost)
		return true
	}
	return false
}
