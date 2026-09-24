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

const (
	// dateScanPage y maxDateScanPages acotan un recorrido de aniversarios por flujo: una
	// tanda de contacts por llamada, hasta 50 000 contactos prefiltrados.
	dateScanPage     = 1000
	maxDateScanPages = 50
	// CodeDateTriggerInvalid pausa un flujo por fecha cuyo atributo o lista ya no valen.
	CodeDateTriggerInvalid = "DATE_TRIGGER_INVALID"
)

// ScanDateTriggers hace entrar en cada flujo activo por fecha a los contactos cuyo
// aniversario cae hoy, ya pasada la hora local del disparador. Cada flujo se recorre como
// mucho una vez por DateScanInterval entre todas las replicas (date_scans). La entrada es
// unica por (flujo, contacto, clave de entrada) con la clave del ano del aniversario: un
// recorrido repetido, un reinicio o dos replicas no duplican a nadie. Devuelve cuantos
// entraron.
func (uc *UseCase) ScanDateTriggers(ctx context.Context, tenantID uuid.UUID) (int, error) {
	if uc.rules == nil || uc.dateScans == nil {
		return 0, nil
	}
	active, err := uc.workflows.ListActiveByTrigger(ctx, tenantID, domain.TriggerContactDate)
	if err != nil {
		return 0, err
	}
	entered := 0
	for i := range active {
		if ctx.Err() != nil {
			return entered, ctx.Err()
		}
		w := &active[i]
		now := uc.now()
		claimed, err := uc.dateScans.Claim(ctx, tenantID, w.ID, now, now.Add(-uc.cfg.DateScanInterval))
		if err != nil {
			return entered, err
		}
		if !claimed {
			continue
		}
		n, err := uc.scanWorkflow(ctx, w)
		entered += n
		if err != nil {
			uc.logger.Warn("automations: recorrido de aniversarios incompleto; se retomara en el siguiente",
				zap.String("tenant_id", tenantID.String()), zap.String("workflow_id", w.ID.String()), zap.Error(err))
		}
	}
	return entered, nil
}

func (uc *UseCase) scanWorkflow(ctx context.Context, w *domain.Workflow) (int, error) {
	if w.Trigger.Hour == nil {
		return 0, nil
	}
	entered, cursor := 0, ""
	for page := 0; page < maxDateScanPages; page++ {
		callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
		res, err := uc.rules.Anniversaries(callCtx, w.TenantID, ports.AnniversaryQuery{
			Attribute: w.Trigger.Attribute, Hour: *w.Trigger.Hour, Timezone: w.Trigger.Timezone,
			ListID: w.ListID, Cursor: cursor, Limit: dateScanPage,
		})
		cancel()
		var rejected *ports.RejectedError
		if errors.As(err, &rejected) {
			return entered, uc.pauseDateWorkflow(ctx, w, rejected)
		}
		if err != nil {
			return entered, err
		}
		for _, m := range res.Matches {
			run, err := domain.NewDateRun(w, m.ContactID, m.Occurrence, uc.now())
			if err != nil {
				uc.logger.Warn("automations: aniversario ilegible de contacts; se ignora", zap.String("workflow_id", w.ID.String()), zap.Error(err))
				continue
			}
			ok, err := uc.runs.Enroll(ctx, run, w.ReEntry)
			if err != nil {
				return entered, err
			}
			if ok {
				entered++
			}
		}
		if res.NextCursor == "" {
			return entered, nil
		}
		cursor = res.NextCursor
	}
	return entered, fmt.Errorf("más de %d tandas de aniversarios", maxDateScanPages)
}

// pauseDateWorkflow pausa el flujo cuyo atributo dejo de existir o de ser una fecha, o cuya
// lista se borro: seguir recorriendo no haria entrar a nadie y el motivo queda a la vista.
func (uc *UseCase) pauseDateWorkflow(ctx context.Context, w *domain.Workflow, cause *ports.RejectedError) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		cur, err := uc.workflows.GetForUpdate(ctx, w.TenantID, w.ID)
		if err != nil {
			return err
		}
		if cur.Status != domain.StatusActive {
			return nil
		}
		if err := cur.Pause(CodeDateTriggerInvalid + ": " + cause.Error()); err != nil {
			return err
		}
		if err := uc.workflows.Update(ctx, cur); err != nil {
			return err
		}
		uc.logger.Warn("automations: flujo por fecha pausado", zap.String("workflow_id", w.ID.String()), zap.Error(cause))
		return uc.publishPaused(ctx, cur)
	})
}

// RecordMessageEngagement anota la apertura o el clic de un correo de un paso de envio.
// Un clic cuenta tambien como apertura. false si el mensaje no es de ningun flujo.
func (uc *UseCase) RecordMessageEngagement(ctx context.Context, tenantID, messageID uuid.UUID, clicked bool, at time.Time) (bool, error) {
	if uc.runMessages == nil || messageID == uuid.Nil {
		return false, nil
	}
	if at.IsZero() || at.After(uc.now()) {
		at = uc.now()
	}
	opened := at
	var clickedAt *time.Time
	if clicked {
		clickedAt = &at
	}
	return uc.runMessages.MarkEngagement(ctx, tenantID, messageID, &opened, clickedAt)
}

// checkRules comprueba en contacts lo que el flujo usa de alli antes de activarlo: los
// segmentos y las condiciones de atributo de sus ramas y el atributo de un disparador por
// fecha. Un rechazo es un error de validacion que nombra el paso.
func (uc *UseCase) checkRules(ctx context.Context, tenantID uuid.UUID, w *domain.Workflow) error {
	if uc.rules == nil {
		return nil
	}
	reject := func(field string, err error) error {
		var rejected *ports.RejectedError
		if !errors.As(err, &rejected) {
			return err
		}
		if rejected.NotFound() {
			return domain.NewValidationError("%s: no existe en contacts (%s)", field, rejected.Message)
		}
		return domain.NewValidationError("%s: %s", field, rejected.Message)
	}
	for i, s := range w.Steps {
		if s.Type != domain.StepBranch || s.Condition == nil {
			continue
		}
		q := ports.MatchQuery{}
		switch s.Condition.Kind {
		case domain.ConditionSegment:
			q.SegmentID = s.Condition.SegmentID
		case domain.ConditionAttribute:
			q.Definition = s.Condition.AttributeDefinition()
		default:
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
		_, err := uc.rules.Match(callCtx, tenantID, q)
		cancel()
		if err != nil {
			return reject(fmt.Sprintf("steps[%d].condition", i), err)
		}
	}
	if w.Trigger.Type.IsDate() && w.Trigger.Hour != nil {
		callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
		_, err := uc.rules.Anniversaries(callCtx, tenantID, ports.AnniversaryQuery{
			Attribute: w.Trigger.Attribute, Hour: *w.Trigger.Hour, Timezone: w.Trigger.Timezone, ListID: w.ListID, Limit: 1,
		})
		cancel()
		if err != nil {
			return reject("trigger", err)
		}
	}
	return nil
}
