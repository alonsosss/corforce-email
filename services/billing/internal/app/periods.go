package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// sweepBatch es cuantas empresas se leen por vuelta del barrido.
const sweepBatch = 100

// SweepReport resume una pasada del barrido de periodos.
type SweepReport struct {
	Closed      int
	Transitions int
	Failed      int
}

// SweepPeriods cierra los periodos vencidos y aplica las transiciones del reloj (prueba
// terminada, cancelacion programada). Cada empresa va en su transaccion: una que falla se
// deja para la siguiente pasada y no bloquea a las demas. El llamador garantiza que solo
// una replica lo ejecuta a la vez.
func (uc *UseCase) SweepPeriods(ctx context.Context) (SweepReport, error) {
	now := uc.now()
	var (
		rep    SweepReport
		failed []uuid.UUID
	)
	for ctx.Err() == nil {
		due, err := uc.subs.ListDue(ctx, now, failed, sweepBatch)
		if err != nil {
			return rep, err
		}
		if len(due) == 0 {
			return rep, nil
		}
		for _, tenantID := range due {
			closed, changed, err := uc.sweepTenant(ctx, tenantID, now)
			if err != nil {
				failed = append(failed, tenantID)
				rep.Failed++
				uc.logger.Error("billing: no se pudo cerrar el periodo de la empresa",
					zap.String("tenant_id", tenantID.String()), zap.Error(err))
				continue
			}
			rep.Closed += closed
			if changed {
				rep.Transitions++
			}
		}
	}
	return rep, ctx.Err()
}

// sweepTenant cierra todos los periodos vencidos de una empresa (si el servicio estuvo
// parado varios periodos, publica un cierre por cada uno) y aplica su ciclo de vida.
func (uc *UseCase) sweepTenant(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, bool, error) {
	var (
		closed  int
		changed bool
	)
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		closed, changed = 0, false
		sub, err := uc.subs.GetByTenantForUpdate(ctx, tenantID)
		if errors.Is(err, domain.ErrSubscriptionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		plan, err := uc.plans.Get(ctx, sub.PlanID)
		if err != nil {
			return err
		}
		previous := sub.Status
		changed = sub.ApplyLifecycle(now)
		for sub.Status != domain.StatusCancelled && sub.PeriodExpired(now) {
			start, end := sub.AdvancePeriod(plan.BillingPeriod)
			usage, err := uc.periodUsage(ctx, tenantID, start)
			if err != nil {
				return err
			}
			if err := uc.events.PeriodClosed(ctx, sub, start, end, usage); err != nil {
				return err
			}
			closed++
		}
		if !changed && closed == 0 {
			return nil
		}
		if err := uc.subs.Update(ctx, sub); err != nil {
			return err
		}
		if changed {
			return uc.events.SubscriptionChanged(ctx, sub, previous, sub.PlanCode)
		}
		return nil
	})
	return closed, changed, err
}

// periodUsage es el consumo de cada recurso en el periodo que empieza en start: el de
// flujo de ese periodo y el stock vivo al cerrarlo. Todos los recursos aparecen.
func (uc *UseCase) periodUsage(ctx context.Context, tenantID uuid.UUID, start time.Time) (map[domain.Resource]int64, error) {
	used, err := uc.usage.Quantities(ctx, tenantID, start)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.Resource]int64, len(domain.Resources()))
	for _, r := range domain.Resources() {
		out[r] = used[r]
	}
	return out, nil
}
