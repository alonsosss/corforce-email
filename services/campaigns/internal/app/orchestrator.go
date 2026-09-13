package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// El orquestador entrega la audiencia de una campana por lotes a la via de marketing de
// transactional sin enviar dos veces a nadie ni saltarse a nadie, aunque el proceso
// muera en cualquier punto o haya varias replicas trabajando a la vez.
//
// Un lote es una fila de campaigns.batches con numero (seq) y cursor de entrada. Cada
// paso deja escrito en la base lo necesario para que el siguiente intento, de este
// proceso o de otro, haga exactamente lo mismo:
//
//  1. Reclamar (transaccion con la campana bloqueada con SKIP LOCKED): se retoma el lote
//     pendiente si lo hay o se crea el siguiente con cursor_in = cursor_out del anterior,
//     y se RESERVA (leased_until + lease_token) antes de confirmar. Nadie llama a contacts
//     ni a transactional sin un lote confirmado: claim-before-send. Mientras la reserva
//     vive, otra replica ve el lote reservado y lo deja; al vencer (el trabajador murio o
//     su llamada caduco, CallTimeout < BatchLease) otra lo reintenta.
//  2. Fijar la pagina: la primera vez se pide a contacts con cursor_in y se guardan los
//     destinatarios y cursor_out en el lote ANTES de enviar. Los reintentos no vuelven a
//     pedirla: envian la misma pagina aunque la audiencia haya cambiado, asi que un
//     contacto nuevo que desplace el orden no hace que el siguiente lote repita a nadie
//     ni que se pierda el ultimo de esta pagina.
//  3. Enviar a transactional con idempotency_key = campaign:<id>:batch:<seq>. La clave
//     depende solo del lote: si el proceso muere entre el 202 de transactional y nuestro
//     commit, el reintento lleva la misma clave y transactional responde lo mismo sin
//     crear mensajes nuevos. No hay duplicados.
//  4. Cerrar (otra transaccion): el lote pasa de pending a delivered y se suman sus
//     totales. El UPDATE exige status = 'pending', asi que aunque dos trabajadores
//     lleguen aqui con el mismo lote, solo uno suma. Solo entonces existe el lote
//     siguiente, que parte del cursor_out guardado en el paso 2. No hay perdidas: ningun
//     cursor avanza sin que su pagina haya sido aceptada.
//
// Un 429 aplaza la campana (resume_after) sin tocar el lote; un 403 de reputacion o de
// plan la pausa; un 422 la da por fallida; cualquier otro fallo cuenta un intento con
// espera creciente y, al decimo, la pausa para que la mire una persona.

// Outcome resume que hizo ProcessBatch con una campana.
type Outcome string

const (
	// OutcomeSkipped: otra replica la tiene, no esta en envio o no le tocaba.
	OutcomeSkipped     Outcome = "skipped"
	OutcomeDelivered   Outcome = "delivered"
	OutcomeCompleted   Outcome = "completed"
	OutcomeRateLimited Outcome = "rate_limited"
	OutcomeRetrying    Outcome = "retrying"
	OutcomePaused      Outcome = "paused"
	OutcomeFailed      Outcome = "failed"
	// OutcomeAborted: se abandono sin registrar resultado (apagado, campana pausada a
	// mitad). El lote sigue pendiente y se reintenta con la misma clave.
	OutcomeAborted Outcome = "aborted"
)

// Tick es una pasada del orquestador por una empresa: arranca las programadas vencidas
// y entrega un lote de cada campana en envio.
func (uc *UseCase) Tick(ctx context.Context, tenantID uuid.UUID) error {
	if err := uc.startDue(ctx, tenantID); err != nil {
		return fmt.Errorf("arrancar programadas: %w", err)
	}
	ids, err := uc.campaigns.ListRunnable(ctx, tenantID, uc.now(), runnablePerTick)
	if err != nil {
		return fmt.Errorf("listar campanas en envio: %w", err)
	}
	for _, id := range ids {
		if !enoughTime(ctx) {
			return nil
		}
		outcome, err := uc.ProcessBatch(ctx, tenantID, id)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			uc.logger.Warn("campaigns: lote no procesado; se reintentara",
				zap.String("tenant_id", tenantID.String()), zap.String("campaign_id", id.String()), zap.Error(err))
			continue
		}
		switch outcome {
		case OutcomeCompleted, OutcomePaused, OutcomeFailed:
			uc.logger.Info("campaigns: cambio de estado del orquestador",
				zap.String("tenant_id", tenantID.String()), zap.String("campaign_id", id.String()),
				zap.String("outcome", string(outcome)))
		}
	}
	return nil
}

// enoughTime: no se empieza un lote que no cabe en lo que queda del presupuesto de la
// empresa; abandonarlo a mitad solo gasta un intento.
func enoughTime(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) > domain.CallTimeout+recordTimeout
}

func (uc *UseCase) startDue(ctx context.Context, tenantID uuid.UUID) error {
	for {
		n := 0
		err := uc.tx.Transact(ctx, func(ctx context.Context) error {
			started, err := uc.campaigns.StartDue(ctx, tenantID, uc.now(), dueBatch)
			if err != nil {
				return err
			}
			for i := range started {
				if err := uc.publishStarted(ctx, &started[i]); err != nil {
					return err
				}
			}
			n = len(started)
			return nil
		})
		if err != nil {
			return err
		}
		if n < dueBatch {
			return nil
		}
	}
}

// ProcessBatch reclama, envia y cierra un lote de la campana (pasos 1 a 4).
func (uc *UseCase) ProcessBatch(ctx context.Context, tenantID, campaignID uuid.UUID) (Outcome, error) {
	now := uc.now()
	var (
		c       *domain.Campaign
		b       *domain.Batch
		outcome = OutcomeSkipped
	)
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		var err error
		c, err = uc.campaigns.LockRunnable(ctx, tenantID, campaignID, now)
		if err != nil || c == nil {
			return err
		}
		pending, err := uc.batches.Pending(ctx, tenantID, campaignID)
		if err != nil {
			return err
		}
		if pending != nil {
			if pending.Leased(now) {
				return nil
			}
			pending.Lease(now)
			if err := uc.batches.Lease(ctx, pending); err != nil {
				return err
			}
			b = pending
			return nil
		}
		last, err := uc.batches.Last(ctx, tenantID, campaignID)
		if err != nil {
			return err
		}
		if last != nil && last.Exhausted() {
			// La ultima pagina se entrego con la campana en pausa: se cierra ahora.
			if err := c.Complete(now); err != nil {
				return err
			}
			if err := uc.campaigns.Update(ctx, c); err != nil {
				return err
			}
			outcome = OutcomeCompleted
			return uc.publishCompleted(ctx, c)
		}
		next := domain.NextBatch(c, last, now)
		if err := uc.batches.Insert(ctx, next); err != nil {
			return err
		}
		b = next
		return nil
	})
	if err != nil {
		return OutcomeSkipped, err
	}
	if b == nil {
		return outcome, nil
	}
	return uc.deliver(ctx, c, b)
}

// deliver hace los pasos 2 y 3 fuera de toda transaccion.
func (uc *UseCase) deliver(ctx context.Context, c *domain.Campaign, b *domain.Batch) (Outcome, error) {
	callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
	defer cancel()

	if !b.PageFetched {
		page, err := uc.audience.Audience(callCtx, c.TenantID, ports.AudienceQuery{
			Audience: c.Audience, Cursor: b.CursorIn, Limit: uc.batchSize,
		})
		if err != nil {
			return uc.onFailure(ctx, c, b, fmt.Errorf("contacts: %w", err))
		}
		b.Page = domain.RecipientsFromContacts(page.Contacts)
		b.CursorOut = page.NextCursor
		b.Recipients = len(b.Page)
		b.PageFetched = true
		rctx, rcancel := recordContext(ctx)
		saved, err := uc.batches.SavePage(rctx, b)
		rcancel()
		if err != nil {
			return OutcomeAborted, err
		}
		if !saved {
			return uc.abandon(ctx, b)
		}
	}

	if len(b.Page) == 0 {
		return uc.onDelivered(ctx, c, b, &ports.BatchResult{})
	}
	res, err := uc.sender.SendBatch(callCtx, c.TenantID, ports.BatchRequest{
		CampaignID:      c.ID,
		IdempotencyKey:  b.IdempotencyKey(),
		FromEmail:       c.FromEmail,
		FromName:        c.FromName,
		ReplyTo:         c.ReplyTo,
		TemplateID:      c.TemplateID,
		TemplateVersion: *c.TemplateVersion,
		Recipients:      b.Page,
	})
	if err != nil {
		return uc.onFailure(ctx, c, b, fmt.Errorf("transactional: %w", err))
	}
	return uc.onDelivered(ctx, c, b, res)
}

// onDelivered es el paso 4.
func (uc *UseCase) onDelivered(ctx context.Context, c *domain.Campaign, b *domain.Batch, res *ports.BatchResult) (Outcome, error) {
	rctx, cancel := recordContext(ctx)
	defer cancel()
	outcome := OutcomeDelivered
	err := uc.tx.Transact(rctx, func(ctx context.Context) error {
		cur, err := uc.campaigns.GetForUpdate(ctx, c.TenantID, c.ID)
		if err != nil {
			return err
		}
		ok, err := uc.batches.MarkDelivered(ctx, b.TenantID, b.ID, res.Accepted, len(res.Suppressed))
		if err != nil {
			return err
		}
		if !ok {
			outcome = OutcomeSkipped
			return nil
		}
		if err := uc.campaigns.AddDeliveryTotals(ctx, c.TenantID, c.ID, b.Recipients, res.Accepted, len(res.Suppressed)); err != nil {
			return err
		}
		changed := cur.ResumeAfter != nil
		cur.ResumeAfter = nil
		if b.CursorOut == nil && cur.Status == domain.StatusSending {
			if err := cur.Complete(uc.now()); err != nil {
				return err
			}
			changed = true
			outcome = OutcomeCompleted
		}
		if changed {
			if err := uc.campaigns.Update(ctx, cur); err != nil {
				return err
			}
		}
		if outcome == OutcomeCompleted {
			return uc.publishCompleted(ctx, cur)
		}
		return nil
	})
	if err != nil {
		return OutcomeAborted, err
	}
	return outcome, nil
}

// onFailure registra por que no se entrego el lote y decide que pasa con la campana.
func (uc *UseCase) onFailure(ctx context.Context, c *domain.Campaign, b *domain.Batch, cause error) (Outcome, error) {
	if ctx.Err() != nil {
		// El proceso se apaga: no es un fallo del vecino ni cuenta como intento. La
		// reserva vence y otro trabajador reintenta con la misma clave.
		return OutcomeAborted, nil
	}
	now := uc.now()
	rctx, cancel := recordContext(ctx)
	defer cancel()

	var (
		limited  *ports.RateLimitedError
		blocked  *ports.BlockedError
		rejected *ports.RejectedError
		outcome  = OutcomeSkipped
	)
	err := uc.tx.Transact(rctx, func(ctx context.Context) error {
		cur, err := uc.campaigns.GetForUpdate(ctx, c.TenantID, c.ID)
		if err != nil {
			return err
		}
		switch {
		case errors.As(cause, &limited):
			ok, err := uc.batches.Release(ctx, b, cause.Error())
			if err != nil || !ok {
				return err
			}
			outcome = OutcomeRateLimited
			if cur.Status != domain.StatusSending {
				return nil
			}
			until := now.Add(domain.ClampRetryAfter(limited.RetryAfter))
			cur.ResumeAfter = &until
			return uc.campaigns.Update(ctx, cur)

		case errors.As(cause, &blocked):
			ok, err := uc.batches.Release(ctx, b, cause.Error())
			if err != nil || !ok {
				return err
			}
			if cur.Status != domain.StatusSending {
				return nil
			}
			outcome = OutcomePaused
			return uc.pauseBy(ctx, cur, blocked.Code+": "+blocked.Message)

		case errors.As(cause, &rejected):
			ok, err := uc.batches.MarkFailed(ctx, b, cause.Error())
			if err != nil || !ok {
				return err
			}
			if cur.Status != domain.StatusSending {
				return nil
			}
			outcome = OutcomeFailed
			// El motivo empieza por el codigo del vecino (TEMPLATE_MISSING_UNSUBSCRIBE...):
			// es lo que la interfaz y quien opera necesitan para corregir la campana.
			if err := cur.Fail(rejected.Error()); err != nil {
				return err
			}
			if err := uc.campaigns.Update(ctx, cur); err != nil {
				return err
			}
			return uc.publishFailed(ctx, cur)

		default:
			attempts, ok, err := uc.batches.RecordFailure(ctx, b, cause.Error())
			if err != nil || !ok {
				return err
			}
			outcome = OutcomeRetrying
			if cur.Status != domain.StatusSending {
				return nil
			}
			if attempts >= domain.MaxBatchAttempts {
				outcome = OutcomePaused
				return uc.pauseBy(ctx, cur, fmt.Sprintf("lote %d sin entregar tras %d intentos: %s", b.Seq, attempts, cause.Error()))
			}
			until := now.Add(domain.RetryBackoff(attempts))
			cur.ResumeAfter = &until
			return uc.campaigns.Update(ctx, cur)
		}
	})
	if err != nil {
		return OutcomeAborted, err
	}
	uc.logger.Warn("campaigns: lote no entregado",
		zap.String("tenant_id", c.TenantID.String()), zap.String("campaign_id", c.ID.String()),
		zap.Int("seq", b.Seq), zap.String("outcome", string(outcome)), zap.Error(cause))
	return outcome, nil
}

func (uc *UseCase) pauseBy(ctx context.Context, cur *domain.Campaign, reason string) error {
	if err := cur.Pause(reason); err != nil {
		return err
	}
	if err := uc.campaigns.Update(ctx, cur); err != nil {
		return err
	}
	return uc.publishPaused(ctx, cur)
}

// abandon suelta la reserva de un lote que no se llego a enviar (la campana dejo de
// estar en envio mientras se pedia la pagina, u otro trabajador se lo quedo).
func (uc *UseCase) abandon(ctx context.Context, b *domain.Batch) (Outcome, error) {
	rctx, cancel := recordContext(ctx)
	defer cancel()
	if _, err := uc.batches.Release(rctx, b, b.LastError); err != nil {
		return OutcomeAborted, err
	}
	return OutcomeAborted, nil
}

func recordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
}
