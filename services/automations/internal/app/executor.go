package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// El ejecutor recorre las ejecuciones debidas y hace UN paso de cada una sin repetir ni
// perder ningun paso aunque el proceso muera en cualquier punto o haya varias replicas:
//
//  1. Reservar: una sentencia con FOR UPDATE SKIP LOCKED toma las debidas de flujos
//     activos, las pasa a running con un lease_token propio y un lease_until, y se confirma
//     ANTES de llamar a nadie. Otra replica ve la reserva viva y no la toca; si el
//     trabajador muere, la reserva vence y otro retoma el MISMO paso.
//  2. Ejecutar el paso fuera de toda transaccion. Un envio lleva la clave
//     automation:<run_id>:step:<indice>, que depende solo de la ejecucion y del paso: si
//     el proceso muere entre la respuesta de transactional y nuestro commit, el reintento
//     manda la misma clave y transactional devuelve lo ya creado. No hay duplicados.
//  3. Registrar (otra transaccion): avanzar step_index al paso siguiente del grafo (el de
//     next, o el de then o else en una rama) y fijar next_run_at, aplazar, o
//     terminar la ejecucion con su evento. Toda escritura exige lease_token y status
//     running, y el avance exige ademas el mismo step_index: un trabajador cuya reserva
//     vencio no puede pisar a quien la retomo. No hay perdidas: ningun paso avanza sin
//     haberse hecho, y una ejecucion reservada que nadie cierra vuelve a estar debida.

// Outcome resume que hizo ProcessRun con una ejecucion.
type Outcome string

const (
	OutcomeAdvanced    Outcome = "advanced"
	OutcomeCompleted   Outcome = "completed"
	OutcomeSkipped     Outcome = "skipped"
	OutcomeFailed      Outcome = "failed"
	OutcomeRateLimited Outcome = "rate_limited"
	OutcomeRetrying    Outcome = "retrying"
	// OutcomeReleased: el flujo dejo de estar activo entre la reserva y el paso; la
	// ejecucion vuelve a esperar sin cambios.
	OutcomeReleased Outcome = "released"
	// OutcomeLost: otro trabajador tomo la reserva o la ejecucion se cancelo; lo hecho
	// no se registra y el paso lo cierra quien la tenga.
	OutcomeLost Outcome = "lost"
	// OutcomeAborted: se abandono sin registrar (apagado o base caida); la reserva vence y
	// el paso se reintenta con la misma clave.
	OutcomeAborted Outcome = "aborted"
)

// Tick es una pasada del ejecutor por una empresa.
func (uc *UseCase) Tick(ctx context.Context, tenantID uuid.UUID) error {
	for round := 0; round < maxClaimRounds; round++ {
		if !enoughTime(ctx) {
			return nil
		}
		now := uc.now()
		runs, err := uc.runs.ClaimDue(ctx, tenantID, now, now.Add(domain.RunLease), claimBatch)
		if err != nil {
			return fmt.Errorf("reservar ejecuciones: %w", err)
		}
		for i := range runs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			outcome, err := uc.ProcessRun(ctx, &runs[i])
			if err != nil {
				uc.logger.Warn("automations: paso no registrado; se reintentara al vencer la reserva",
					zap.String("tenant_id", tenantID.String()), zap.String("run_id", runs[i].ID.String()), zap.Error(err))
				continue
			}
			if outcome == OutcomeFailed {
				uc.logger.Info("automations: ejecucion fallida",
					zap.String("tenant_id", tenantID.String()), zap.String("run_id", runs[i].ID.String()))
			}
		}
		if len(runs) < claimBatch {
			return nil
		}
	}
	return nil
}

// enoughTime: no se reserva un lote que no cabe en lo que queda del presupuesto de la
// empresa; abandonarlo a mitad solo retrasa esas ejecuciones hasta que venza su reserva.
func enoughTime(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) > 2*domain.CallTimeout+recordTimeout
}

// ProcessRun ejecuta el paso actual de una ejecucion ya reservada.
func (uc *UseCase) ProcessRun(ctx context.Context, run *domain.Run) (Outcome, error) {
	w, err := uc.workflows.Get(ctx, run.TenantID, run.WorkflowID)
	if errors.Is(err, domain.ErrWorkflowNotFound) {
		// El flujo se borro y sus ejecuciones con el (ON DELETE CASCADE).
		return OutcomeLost, nil
	}
	if err != nil {
		return OutcomeAborted, err
	}
	if w.Status != domain.StatusActive {
		return uc.release(ctx, run)
	}
	if run.StepIndex >= len(w.Steps) {
		return uc.finish(ctx, w, run, domain.RunCompleted, "", "")
	}
	step := w.Steps[run.StepIndex]
	switch step.Type {
	case domain.StepWait:
		return uc.advance(ctx, w, run, domain.NextIndex(w.Steps, run.StepIndex, false), uc.now().Add(step.WaitDuration()))
	case domain.StepSendEmail:
		return uc.stepSend(ctx, w, run, step)
	case domain.StepAddToList, domain.StepRemoveFromList:
		return uc.stepList(ctx, w, run, step)
	case domain.StepBranch:
		return uc.stepBranch(ctx, w, run, step)
	}
	return uc.finish(ctx, w, run, domain.RunFailed, "INVALID_STEP", "tipo de paso desconocido: "+string(step.Type))
}

// stepSend comprueba que el contacto sigue siendo enviable (activo y con consentimiento
// de marketing vigente) y entrega el correo a la via de marketing de transactional como un
// lote de un destinatario, con campaign_id = id del flujo.
func (uc *UseCase) stepSend(ctx context.Context, w *domain.Workflow, run *domain.Run, step domain.Step) (Outcome, error) {
	if step.TemplateID == nil || step.TemplateVersion == nil {
		return uc.finish(ctx, w, run, domain.RunFailed, "TEMPLATE_VERSION_MISSING", "el paso de envío no tiene la versión de plantilla fijada")
	}
	callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
	defer cancel()
	sendable, err := uc.contacts.Sendable(callCtx, run.TenantID, []uuid.UUID{run.ContactID})
	if err != nil {
		return uc.onFailure(ctx, w, run, fmt.Errorf("contacts: %w", err))
	}
	var contact *domain.Contact
	for i := range sendable {
		if sendable[i].ID == run.ContactID {
			contact = &sendable[i]
		}
	}
	if contact == nil {
		return uc.finish(ctx, w, run, domain.RunSkipped, domain.CodeContactNotSendable,
			"el contacto no está activo o no tiene consentimiento de marketing vigente")
	}
	res, err := uc.sender.SendMarketing(callCtx, run.TenantID, ports.MarketingMessage{
		WorkflowID:      w.ID,
		IdempotencyKey:  run.IdempotencyKey(),
		FromEmail:       step.FromEmail,
		FromName:        step.FromName,
		ReplyTo:         step.ReplyTo,
		TemplateID:      *step.TemplateID,
		TemplateVersion: *step.TemplateVersion,
		Contact:         *contact,
		Tags:            map[string]string{"source": "automation", "workflow_id": w.ID.String()},
	})
	if err != nil {
		return uc.onFailure(ctx, w, run, fmt.Errorf("transactional: %w", err))
	}
	if err := uc.recordMessage(ctx, w, run, step, res); err != nil {
		return OutcomeAborted, err
	}
	return uc.advance(ctx, w, run, domain.NextIndex(w.Steps, run.StepIndex, false), uc.now())
}

// recordMessage guarda el correo del paso para las ramas que lo miran. Va antes de avanzar
// y es idempotente: si el proceso cae entre las dos escrituras, el reintento manda la misma
// clave, transactional devuelve el mismo mensaje y el registro no se duplica. Un envio que
// la supresion retiro no deja mensaje: la rama lo trata como no abierto.
func (uc *UseCase) recordMessage(ctx context.Context, w *domain.Workflow, run *domain.Run, step domain.Step, res *ports.BatchResult) error {
	if uc.runMessages == nil || res == nil || len(res.MessageIDs) == 0 {
		return nil
	}
	rctx, cancel := recordContext(ctx)
	defer cancel()
	return uc.runMessages.Record(rctx, domain.RunMessage{
		TenantID: run.TenantID, RunID: run.ID, WorkflowID: w.ID, ContactID: run.ContactID,
		StepID: step.ID, MessageID: res.MessageIDs[0],
	})
}

// stepBranch evalua la condicion y sigue por then o por else. Un segmento o un atributo que
// ya no existen son un rechazo de contacts y terminan la ejecucion como fallida con su
// codigo; un fallo transitorio se reintenta como cualquier paso.
func (uc *UseCase) stepBranch(ctx context.Context, w *domain.Workflow, run *domain.Run, step domain.Step) (Outcome, error) {
	if step.Condition == nil {
		return uc.finish(ctx, w, run, domain.RunFailed, "INVALID_STEP", "rama sin condición")
	}
	taken, err := uc.evaluate(ctx, run, *step.Condition)
	if err != nil {
		return uc.onFailure(ctx, w, run, err)
	}
	return uc.advance(ctx, w, run, domain.NextIndex(w.Steps, run.StepIndex, taken), uc.now())
}

func (uc *UseCase) evaluate(ctx context.Context, run *domain.Run, c domain.Condition) (bool, error) {
	switch c.Kind {
	case domain.ConditionEmailOpened, domain.ConditionEmailClicked:
		m, err := uc.runMessages.Get(ctx, run.TenantID, run.ID, c.Step)
		if err != nil {
			return false, err
		}
		return m.Satisfies(c.Kind), nil
	case domain.ConditionSegment, domain.ConditionAttribute:
		q := ports.MatchQuery{ContactIDs: []uuid.UUID{run.ContactID}}
		if c.Kind == domain.ConditionSegment {
			q.SegmentID = c.SegmentID
		} else {
			q.Definition = c.AttributeDefinition()
		}
		callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
		defer cancel()
		ids, err := uc.rules.Match(callCtx, run.TenantID, q)
		if err != nil {
			return false, fmt.Errorf("contacts: %w", err)
		}
		for _, id := range ids {
			if id == run.ContactID {
				return true, nil
			}
		}
		return false, nil
	}
	return false, &ports.RejectedError{Code: "INVALID_STEP", Message: "condición desconocida: " + string(c.Kind)}
}

// stepList anade o quita al contacto de una lista. Las dos operaciones son idempotentes
// en contacts (un miembro que ya estaba, o que ya no esta, se ignora).
func (uc *UseCase) stepList(ctx context.Context, w *domain.Workflow, run *domain.Run, step domain.Step) (Outcome, error) {
	callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
	defer cancel()
	ids := []uuid.UUID{run.ContactID}
	var err error
	if step.Type == domain.StepAddToList {
		err = uc.contacts.AddToList(callCtx, run.TenantID, *step.ListID, ids)
	} else {
		err = uc.contacts.RemoveFromList(callCtx, run.TenantID, *step.ListID, ids)
	}
	if err != nil {
		return uc.onFailure(ctx, w, run, fmt.Errorf("contacts: %w", err))
	}
	return uc.advance(ctx, w, run, domain.NextIndex(w.Steps, run.StepIndex, false), uc.now())
}

// advance cierra el paso: pasa al de la posicion next con next_run_at. Si next es el fin
// (len(steps)) y no hay espera, la ejecucion termina aqui mismo.
func (uc *UseCase) advance(ctx context.Context, w *domain.Workflow, run *domain.Run, next int, nextRunAt time.Time) (Outcome, error) {
	if next >= len(w.Steps) && !nextRunAt.After(uc.now()) {
		return uc.finish(ctx, w, run, domain.RunCompleted, "", "")
	}
	rctx, cancel := recordContext(ctx)
	defer cancel()
	ok, err := uc.runs.Advance(rctx, run, next, nextRunAt)
	if err != nil {
		return OutcomeAborted, err
	}
	if !ok {
		return OutcomeLost, nil
	}
	return OutcomeAdvanced, nil
}

// finish termina la ejecucion con su evento. Un fallo por bloqueo de transactional puede
// pausar el flujo en la misma transaccion.
func (uc *UseCase) finish(ctx context.Context, w *domain.Workflow, run *domain.Run, status domain.RunStatus, code, reason string) (Outcome, error) {
	rctx, cancel := recordContext(ctx)
	defer cancel()
	reason = domain.TruncateReason(reason)
	outcome := map[domain.RunStatus]Outcome{
		domain.RunCompleted: OutcomeCompleted, domain.RunSkipped: OutcomeSkipped, domain.RunFailed: OutcomeFailed,
	}[status]
	err := uc.tx.Transact(rctx, func(ctx context.Context) error {
		ok, err := uc.runs.Finish(ctx, run, status, code, reason, uc.now())
		if err != nil {
			return err
		}
		if !ok {
			outcome = OutcomeLost
			return nil
		}
		switch status {
		case domain.RunCompleted:
			return uc.publishRunCompleted(ctx, run)
		case domain.RunFailed:
			if err := uc.publishRunFailed(ctx, run, reason); err != nil {
				return err
			}
			return uc.pauseIfBlocked(ctx, w, code)
		}
		return nil
	})
	if err != nil {
		return OutcomeAborted, err
	}
	return outcome, nil
}

// pauseIfBlocked pausa el flujo cuando acumula PauseAfterFailures ejecuciones bloqueadas
// por transactional en la ultima hora: la empresa no puede enviar y seguir solo quema
// contactos que ya no volveran a entrar.
func (uc *UseCase) pauseIfBlocked(ctx context.Context, w *domain.Workflow, code string) error {
	blocking := false
	for _, c := range domain.BlockingCodes() {
		blocking = blocking || c == code
	}
	if !blocking {
		return nil
	}
	n, err := uc.runs.CountRecentFailures(ctx, w.TenantID, w.ID, domain.BlockingCodes(), uc.now().Add(-blockedWindow))
	if err != nil || n < uc.cfg.PauseAfterFailures {
		return err
	}
	cur, err := uc.workflows.GetForUpdate(ctx, w.TenantID, w.ID)
	if err != nil {
		return err
	}
	if cur.Status != domain.StatusActive {
		return nil
	}
	if err := cur.Pause(fmt.Sprintf("%s: %d ejecuciones bloqueadas por transactional en la ultima hora", code, n)); err != nil {
		return err
	}
	if err := uc.workflows.Update(ctx, cur); err != nil {
		return err
	}
	uc.logger.Warn("automations: flujo pausado por bloqueos repetidos",
		zap.String("tenant_id", w.TenantID.String()), zap.String("workflow_id", w.ID.String()), zap.String("code", code))
	return uc.publishPaused(ctx, cur)
}

// onFailure decide que pasa con la ejecucion segun el fallo del vecino: un 429 la aplaza
// lo que pida; un 403 de reputacion o de plan y un rechazo definitivo la terminan como
// fallida; cualquier otro fallo cuenta un intento con espera creciente y, al agotarlos, la
// da por fallida.
func (uc *UseCase) onFailure(ctx context.Context, w *domain.Workflow, run *domain.Run, cause error) (Outcome, error) {
	if ctx.Err() != nil {
		return OutcomeAborted, nil
	}
	var (
		limited  *ports.RateLimitedError
		blocked  *ports.BlockedError
		rejected *ports.RejectedError
	)
	now := uc.now()
	switch {
	case errors.As(cause, &limited):
		return uc.reschedule(ctx, run, now.Add(domain.ClampRetryAfter(limited.RetryAfter)), run.Attempts,
			domain.CodeRateLimited, cause.Error(), OutcomeRateLimited)
	case errors.As(cause, &blocked):
		return uc.finish(ctx, w, run, domain.RunFailed, blocked.Code, cause.Error())
	case errors.As(cause, &rejected):
		code := rejected.Code
		if code == "" {
			code = "REJECTED"
		}
		return uc.finish(ctx, w, run, domain.RunFailed, code, cause.Error())
	}
	attempts := run.Attempts + 1
	if attempts >= domain.MaxStepAttempts {
		return uc.finish(ctx, w, run, domain.RunFailed, domain.CodeUnavailable,
			fmt.Sprintf("paso %d sin completar tras %d intentos: %v", run.StepIndex, attempts, cause))
	}
	return uc.reschedule(ctx, run, now.Add(domain.RetryBackoff(attempts)), attempts, domain.CodeUnavailable, cause.Error(), OutcomeRetrying)
}

func (uc *UseCase) reschedule(ctx context.Context, run *domain.Run, at time.Time, attempts int, code, reason string, outcome Outcome) (Outcome, error) {
	rctx, cancel := recordContext(ctx)
	defer cancel()
	ok, err := uc.runs.Reschedule(rctx, run, at, attempts, code, domain.TruncateReason(reason))
	if err != nil {
		return OutcomeAborted, err
	}
	if !ok {
		return OutcomeLost, nil
	}
	return outcome, nil
}

func (uc *UseCase) release(ctx context.Context, run *domain.Run) (Outcome, error) {
	rctx, cancel := recordContext(ctx)
	defer cancel()
	ok, err := uc.runs.Release(rctx, run)
	if err != nil {
		return OutcomeAborted, err
	}
	if !ok {
		return OutcomeLost, nil
	}
	return OutcomeReleased, nil
}
