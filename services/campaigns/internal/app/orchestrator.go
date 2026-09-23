package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
//
// Fases. Los lotes pertenecen a una fase (campaigns.phases) y cada fase recorre la
// audiencia desde el principio eligiendo a quien envia (domain.Campaign.Select): la
// principal a todos; en una prueba A/B, una muestra por variante y despues la ganadora a
// quien no la recibio; en el envio por zona horaria, un tramo por instante local objetivo;
// y el reenvio a quien no abrio. Las fases se procesan en orden y una termina cuando su
// ultimo lote entrega la ultima pagina (advance). Las esperas (ventana de decision,
// instante del tramo, retraso del reenvio) se expresan con not_before de la fase y
// resume_after de la campana, el mismo mecanismo que un 429: una campana que espera no la
// toma ningun orquestador, y un reinicio o varias replicas no cambian nada porque cada
// paso queda escrito antes de actuar. La numeracion de lotes es unica en la campana, asi
// que la clave de idempotencia sigue siendo campaign:<id>:batch:<seq> en todas las fases.

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
	// OutcomeEmpty: el lote no tenia a nadie que enviar en su fase y se cerro sin llamar a
	// transactional. El orquestador sigue con la misma campana en la misma pasada.
	OutcomeEmpty Outcome = "empty"
	// OutcomeWaiting: la fase siguiente espera su momento (ventana de decision A/B, tramo
	// de zona, retraso del reenvio); la campana queda aplazada hasta entonces.
	OutcomeWaiting Outcome = "waiting"
)

// maxStepsPerCampaign acota cuantos lotes vacios seguidos procesa una pasada para una
// misma campana antes de pasar a la siguiente.
const maxStepsPerCampaign = 50

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
		if err := uc.processCampaign(ctx, tenantID, id); err != nil {
			return err
		}
	}
	return nil
}

// processCampaign entrega un lote de la campana; si el lote salio vacio (nadie de esa
// pagina era de la fase) sigue con el siguiente sin esperar al proximo tick.
func (uc *UseCase) processCampaign(ctx context.Context, tenantID, id uuid.UUID) error {
	for step := 0; step < maxStepsPerCampaign; step++ {
		if step > 0 && !enoughTime(ctx) {
			return nil
		}
		outcome, err := uc.ProcessBatch(ctx, tenantID, id)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			uc.logger.Warn("campaigns: lote no procesado; se reintentara",
				zap.String("tenant_id", tenantID.String()), zap.String("campaign_id", id.String()), zap.Error(err))
			return nil
		}
		switch outcome {
		case OutcomeCompleted, OutcomePaused, OutcomeFailed:
			uc.logger.Info("campaigns: cambio de estado del orquestador",
				zap.String("tenant_id", tenantID.String()), zap.String("campaign_id", id.String()),
				zap.String("outcome", string(outcome)))
		}
		if outcome != OutcomeEmpty {
			return nil
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
		phase   *domain.Phase
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
			if pending.PhaseID != nil {
				phase, err = uc.phases.Get(ctx, tenantID, *pending.PhaseID)
			}
			return err
		}
		last, err := uc.batches.Last(ctx, tenantID, campaignID)
		if err != nil {
			return err
		}
		phase, outcome, err = uc.advance(ctx, c, last, now)
		if err != nil || phase == nil {
			return err
		}
		next := domain.NextPhaseBatch(c, last, phase, now)
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
	return uc.deliver(ctx, c, phase, b)
}

// advance pone al dia las fases de una campana en envio, bloqueada y sin lote pendiente:
// da de alta las iniciales, cierra la fase cuyo ultimo lote entrego la ultima pagina, fija
// las esperas, decide la ganadora A/B al vencer la ventana, da de alta el reenvio al
// terminar la ronda inicial y completa la campana cuando no queda nada. Devuelve la fase en
// la que toca crear el siguiente lote, o nil si la campana espera o termino.
func (uc *UseCase) advance(ctx context.Context, c *domain.Campaign, last *domain.Batch, now time.Time) (*domain.Phase, Outcome, error) {
	phases, err := uc.phases.List(ctx, c.TenantID, c.ID)
	if err != nil {
		return nil, OutcomeSkipped, err
	}
	if len(phases) == 0 {
		for _, p := range c.InitialPhases(now) {
			if _, err := uc.phases.Insert(ctx, p); err != nil {
				return nil, OutcomeSkipped, err
			}
		}
		if phases, err = uc.phases.List(ctx, c.TenantID, c.ID); err != nil {
			return nil, OutcomeSkipped, err
		}
	}
	for {
		cur := firstPending(phases)
		if cur == nil {
			if rp := c.ResendPhase(now); rp != nil && !hasKind(phases, domain.PhaseResend) {
				if _, err := uc.phases.Insert(ctx, rp); err != nil {
					return nil, OutcomeSkipped, err
				}
				phases = append(phases, *rp)
				continue
			}
			if err := c.Complete(now); err != nil {
				return nil, OutcomeSkipped, err
			}
			if err := uc.campaigns.Update(ctx, c); err != nil {
				return nil, OutcomeSkipped, err
			}
			return nil, OutcomeCompleted, uc.publishCompleted(ctx, c)
		}
		if last != nil && last.InPhase(cur) && last.Exhausted() {
			cur.Complete(now)
			if err := uc.phases.Update(ctx, cur); err != nil {
				return nil, OutcomeSkipped, err
			}
			continue
		}
		if cur.Kind == domain.PhaseWinner && cur.NotBefore == nil && c.ABTest != nil {
			// La ventana de decision cuenta desde que la ultima muestra termino.
			at := now.Add(c.ABTest.DecisionWindow())
			cur.NotBefore = &at
			if err := uc.phases.Update(ctx, cur); err != nil {
				return nil, OutcomeSkipped, err
			}
		}
		if !cur.Due(now) {
			wait := *cur.NotBefore
			c.ResumeAfter = &wait
			if err := uc.campaigns.Update(ctx, c); err != nil {
				return nil, OutcomeSkipped, err
			}
			return nil, OutcomeWaiting, nil
		}
		if cur.Kind == domain.PhaseWinner && c.ABWinner == nil {
			if err := uc.decideWinner(ctx, c, now); err != nil {
				return nil, OutcomeSkipped, err
			}
		}
		if cur.StartedAt == nil {
			cur.Start(now)
			if err := uc.phases.Update(ctx, cur); err != nil {
				return nil, OutcomeSkipped, err
			}
		}
		return cur, OutcomeSkipped, nil
	}
}

// decideWinner elige la variante con los contadores de la muestra en este momento y deja
// la decision en la campana y en la outbox, en la misma transaccion.
func (uc *UseCase) decideWinner(ctx context.Context, c *domain.Campaign, now time.Time) error {
	rows, err := uc.ledger.Engagement(ctx, c.TenantID, c.ID)
	if err != nil {
		return err
	}
	d := domain.SelectWinner(c.ABTest.Criterion, len(c.ABTest.Variants), domain.SampleResults(rows))
	if err := c.DecideWinner(d, now); err != nil {
		return err
	}
	if err := uc.campaigns.Update(ctx, c); err != nil {
		return err
	}
	uc.logger.Info("campaigns: ganadora de la prueba A/B",
		zap.String("tenant_id", c.TenantID.String()), zap.String("campaign_id", c.ID.String()),
		zap.String("winner", domain.VariantLabel(d.Winner)), zap.String("reason", string(d.Reason)))
	return uc.publishABDecided(ctx, c, d)
}

func firstPending(phases []domain.Phase) *domain.Phase {
	for i := range phases {
		if phases[i].Status == domain.PhasePending {
			return &phases[i]
		}
	}
	return nil
}

func hasKind(phases []domain.Phase, kind domain.PhaseKind) bool {
	for _, p := range phases {
		if p.Kind == kind {
			return true
		}
	}
	return false
}

// deliver hace los pasos 2 y 3 fuera de toda transaccion.
func (uc *UseCase) deliver(ctx context.Context, c *domain.Campaign, p *domain.Phase, b *domain.Batch) (Outcome, error) {
	callCtx, cancel := context.WithTimeout(ctx, domain.CallTimeout)
	defer cancel()

	if !b.PageFetched {
		sel, cursorOut, err := uc.collect(callCtx, c, p, b.CursorIn)
		if err != nil {
			return uc.onFailure(ctx, c, b, err)
		}
		if len(sel.FutureSlots) > 0 {
			rctx, rcancel := recordContext(ctx)
			err := uc.registerSlots(rctx, c, sel.FutureSlots)
			rcancel()
			if err != nil {
				return OutcomeAborted, err
			}
		}
		b.Page = sel.Recipients
		b.CursorOut = cursorOut
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
		return uc.onDelivered(ctx, c, p, b, &ports.BatchResult{})
	}
	content, err := c.ContentFor(p)
	if err != nil {
		return uc.onFailure(ctx, c, b, &ports.RejectedError{Code: "CAMPAIGN_CONTENT_INVALID", Message: err.Error()})
	}
	res, err := uc.sender.SendBatch(callCtx, c.TenantID, ports.BatchRequest{
		CampaignID:      c.ID,
		CampaignName:    c.Name,
		IdempotencyKey:  b.IdempotencyKey(),
		FromEmail:       c.FromEmail,
		FromName:        c.FromName,
		ReplyTo:         c.ReplyTo,
		TemplateID:      content.TemplateID,
		TemplateVersion: content.TemplateVersion,
		Subject:         content.Subject,
		UTMContent:      content.UTMContent,
		Recipients:      b.Page,
	})
	if err != nil {
		return uc.onFailure(ctx, c, b, fmt.Errorf("transactional: %w", err))
	}
	return uc.onDelivered(ctx, c, p, b, res)
}

// collect arma la pagina del lote. La fase principal toma una pagina de la audiencia tal
// cual. Las que filtran leen paginas seguidas pidiendo a contacts solo lo que cabe (limit =
// hueco que queda en el lote), asi que nunca superan el tope del lote de transactional, y
// paran al llenarlo, al acabarse la audiencia o tras MaxAudiencePagesPerBatch paginas.
func (uc *UseCase) collect(ctx context.Context, c *domain.Campaign, p *domain.Phase, cursor *string) (domain.Selection, *string, error) {
	if p == nil || !p.Filtered() {
		page, err := uc.audience.Audience(ctx, c.TenantID, ports.AudienceQuery{Audience: c.Audience, Cursor: cursor, Limit: uc.batchSize})
		if err != nil {
			return domain.Selection{}, nil, fmt.Errorf("contacts: %w", err)
		}
		return c.Select(p, page.Contacts, domain.SelectionLookup{}), page.NextCursor, nil
	}
	out := domain.Selection{Recipients: []domain.Recipient{}}
	seen := map[string]bool{}
	slots := map[time.Time]bool{}
	for i := 0; i < domain.MaxAudiencePagesPerBatch; i++ {
		room := uc.batchSize - len(out.Recipients)
		if room <= 0 {
			break
		}
		page, err := uc.audience.Audience(ctx, c.TenantID, ports.AudienceQuery{Audience: c.Audience, Cursor: cursor, Limit: room})
		if err != nil {
			return domain.Selection{}, nil, fmt.Errorf("contacts: %w", err)
		}
		look, err := uc.lookup(ctx, c, p, page.Contacts)
		if err != nil {
			return domain.Selection{}, nil, err
		}
		sel := c.Select(p, page.Contacts, look)
		for _, r := range sel.Recipients {
			key := strings.ToLower(r.Email)
			if !seen[key] {
				seen[key] = true
				out.Recipients = append(out.Recipients, r)
			}
		}
		for _, s := range sel.FutureSlots {
			if !slots[s] {
				slots[s] = true
				out.FutureSlots = append(out.FutureSlots, s)
			}
		}
		cursor = page.NextCursor
		if cursor == nil {
			break
		}
	}
	return out, cursor, nil
}

// lookup consulta lo que la fase necesita saber de los contactos de la pagina.
func (uc *UseCase) lookup(ctx context.Context, c *domain.Campaign, p *domain.Phase, contacts []domain.Contact) (domain.SelectionLookup, error) {
	var look domain.SelectionLookup
	if (!p.NeedsSent() && !p.NeedsResendEligibility()) || len(contacts) == 0 {
		return look, nil
	}
	ids := make([]uuid.UUID, 0, len(contacts))
	for _, ct := range contacts {
		ids = append(ids, ct.ID)
	}
	var err error
	if p.NeedsSent() {
		if look.Sent, err = uc.ledger.Sent(ctx, c.TenantID, c.ID, ids); err != nil {
			return look, fmt.Errorf("destinatarios de la campana: %w", err)
		}
	}
	if p.NeedsResendEligibility() {
		if look.ResendEligible, err = uc.ledger.ResendEligible(ctx, c.TenantID, c.ID, ids); err != nil {
			return look, fmt.Errorf("destinatarios del reenvio: %w", err)
		}
	}
	return look, nil
}

// registerSlots da de alta los tramos de zona que aparecieron en la pagina. El alta es
// idempotente: repetirla tras una caida no duplica ningun tramo.
func (uc *UseCase) registerSlots(ctx context.Context, c *domain.Campaign, slots []time.Time) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		for _, s := range slots {
			if _, err := uc.phases.Insert(ctx, c.ZonePhase(s)); err != nil {
				return err
			}
		}
		return nil
	})
}

// onDelivered es el paso 4. Junto al cierre del lote quedan anotados sus contactos en la
// ronda y la fase y variante de sus mensajes: la ganadora, los tramos y el reenvio
// dependen de esa anotacion, asi que existe si y solo si el lote se cerro.
func (uc *UseCase) onDelivered(ctx context.Context, c *domain.Campaign, p *domain.Phase, b *domain.Batch, res *ports.BatchResult) (Outcome, error) {
	rctx, cancel := recordContext(ctx)
	defer cancel()
	outcome := OutcomeDelivered
	if len(b.Page) == 0 {
		outcome = OutcomeEmpty
	}
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
		if err := uc.recordBatch(ctx, c, p, b, res); err != nil {
			return err
		}
		if cur.ResumeAfter != nil {
			cur.ResumeAfter = nil
			if err := uc.campaigns.Update(ctx, cur); err != nil {
				return err
			}
		}
		if b.CursorOut != nil || cur.Status != domain.StatusSending {
			return nil
		}
		delivered := *b
		delivered.Status = domain.BatchDelivered
		_, next, err := uc.advance(ctx, cur, &delivered, uc.now())
		if err != nil {
			return err
		}
		if next == OutcomeCompleted {
			outcome = OutcomeCompleted
		}
		return nil
	})
	if err != nil {
		return OutcomeAborted, err
	}
	return outcome, nil
}

func (uc *UseCase) recordBatch(ctx context.Context, c *domain.Campaign, p *domain.Phase, b *domain.Batch, res *ports.BatchResult) error {
	kind := domain.PhaseMain
	var variant *int
	if p != nil {
		kind, variant = p.Kind, p.Variant
		if err := uc.phases.AddTotals(ctx, c.TenantID, p.ID, b.Recipients, res.Accepted, len(res.Suppressed)); err != nil {
			return err
		}
		ids := make([]uuid.UUID, 0, len(b.Page))
		for _, r := range b.Page {
			if r.ContactID != nil {
				ids = append(ids, *r.ContactID)
			}
		}
		if err := uc.ledger.RecordRecipients(ctx, c.TenantID, c.ID, p.ID, p.Round(), ids); err != nil {
			return err
		}
	}
	if len(res.MessageIDs) == 0 {
		return nil
	}
	return uc.ledger.RecordMessages(ctx, c.TenantID, c.ID, kind, variant, res.MessageIDs)
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
