package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RecordResult cuenta que paso con un evento de consumo.
type RecordResult struct {
	// Duplicate: el evento ya se habia procesado (reentrega) y no se toco nada.
	Duplicate bool
	// Skipped: consumo de flujo de una empresa sin suscripcion, que no tiene periodo.
	Skipped bool
	// Quantity es el contador tras el evento.
	Quantity int64
}

// RecordUsage aplica un hecho consumado al contador en UNA transaccion que registra el id
// del evento: una reentrega no cuenta dos veces. Si el alta lleva un limite duro a su
// tope, publica billing.limit.reached una vez por periodo y recurso.
func (uc *UseCase) RecordUsage(ctx context.Context, ch domain.UsageChange) (RecordResult, error) {
	if err := ch.Validate(); err != nil {
		return RecordResult{}, err
	}
	var res RecordResult
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		res = RecordResult{}
		fresh, err := uc.ledger.MarkProcessed(ctx, ch.EventID, ch.Subject)
		if err != nil {
			return err
		}
		if !fresh {
			res.Duplicate = true
			return nil
		}
		sub, plan, err := uc.subscriptionWithPlan(ctx, ch.TenantID)
		if err != nil {
			return err
		}
		var periodStart time.Time
		if sub != nil {
			periodStart, _ = sub.PeriodAt(uc.now(), plan.BillingPeriod)
		} else if ch.Resource.IsFlow() {
			res.Skipped = true
			return nil
		}

		counter, err := uc.usage.LockCounter(ctx, ch.TenantID, ch.Resource, ch.Resource.CounterPeriod(periodStart))
		if err != nil {
			return err
		}
		res.Quantity = counter.Quantity
		delta, err := uc.itemDelta(ctx, ch)
		if err != nil || delta == 0 {
			return err
		}
		counter.Quantity = domain.NextQuantity(counter.Quantity, delta)
		res.Quantity = counter.Quantity

		if sub != nil && delta > 0 {
			limit := plan.EffectiveLimit(ch.Resource)
			if limit.ReachedBy(counter.Quantity) && !counter.NotifiedIn(periodStart) {
				notified := periodStart
				counter.LimitReachedPeriod = &notified
				if err := uc.events.LimitReached(ctx, ch.TenantID, limit, periodStart, counter.Quantity); err != nil {
					return err
				}
			}
		}
		return uc.usage.SaveCounter(ctx, counter)
	})
	if err != nil {
		return RecordResult{}, err
	}
	if res.Skipped {
		uc.logger.Warn("billing: consumo de una empresa sin suscripcion; no se cuenta",
			zap.String("tenant_id", ch.TenantID.String()), zap.String("resource", string(ch.Resource)),
			zap.String("subject", ch.Subject))
	}
	return res, nil
}

// itemDelta traduce el alta o baja de un objeto con varias fuentes: solo la primera fuente
// suma y solo la ultima resta. Sin objeto, el delta es el del evento.
func (uc *UseCase) itemDelta(ctx context.Context, ch domain.UsageChange) (int64, error) {
	if ch.ItemKey == "" {
		return ch.Delta, nil
	}
	if ch.Delta > 0 {
		first, err := uc.usage.AddStockItem(ctx, ch.TenantID, ch.Resource, ch.ItemKey, ch.Source)
		if err != nil || !first {
			return 0, err
		}
		return 1, nil
	}
	last, err := uc.usage.RemoveStockItem(ctx, ch.TenantID, ch.Resource, ch.ItemKey, ch.Source)
	if err != nil || !last {
		return 0, err
	}
	return -1, nil
}

// TenantUsage devuelve el consumo de la empresa en su periodo vigente frente a su plan.
func (uc *UseCase) TenantUsage(ctx context.Context, tenantID uuid.UUID) (*domain.UsageReport, error) {
	sub, plan, err := uc.GetSubscription(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	start, end := sub.PeriodAt(uc.now(), plan.BillingPeriod)
	used, err := uc.usage.Quantities(ctx, tenantID, start)
	if err != nil {
		return nil, err
	}
	rep := &domain.UsageReport{TenantID: tenantID, PlanCode: plan.Code, PeriodStart: start, PeriodEnd: end}
	for _, r := range domain.Resources() {
		rep.Lines = append(rep.Lines, domain.NewUsageLine(plan.EffectiveLimit(r), used[r]))
	}
	return rep, nil
}

// PruneProcessed borra el rastro de deduplicacion anterior a la retencion.
func (uc *UseCase) PruneProcessed(ctx context.Context, retention time.Duration) (int64, error) {
	return uc.ledger.PruneProcessed(ctx, uc.now().Add(-retention))
}
