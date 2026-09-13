package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Reevaluate recalcula una clase con los numeros de su ventana. Si el estado cambia, el
// nuevo estado, su linea de historial y el evento se escriben en la misma transaccion; la
// fila de estado se bloquea para que dos rebotes simultaneos de la misma empresa no
// produzcan dos transiciones. Devuelve el estado vigente y si cambio.
func (uc *UseCase) Reevaluate(ctx context.Context, tenantID uuid.UUID, class domain.Class) (domain.Record, bool, error) {
	var (
		out    domain.Record
		change *domain.Change
	)
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		cur, verdict, err := uc.lockAndEvaluate(ctx, tenantID, class)
		if err != nil {
			return err
		}
		next, changed := domain.Transition(cur, verdict, uc.now())
		out = next
		if !changed {
			return nil
		}
		change, err = uc.applyChange(ctx, cur, next, false)
		return err
	})
	if err != nil {
		return domain.Record{}, false, err
	}
	uc.recordChange(change)
	return out, change != nil, nil
}

// lockAndEvaluate bloquea la fila de estado de la clase (creandola en ok si no existia) y
// calcula el veredicto con la ventana actual. Debe correr dentro de una transaccion.
func (uc *UseCase) lockAndEvaluate(ctx context.Context, tenantID uuid.UUID, class domain.Class) (domain.Record, domain.Verdict, error) {
	now := uc.now()
	if err := uc.states.Ensure(ctx, tenantID, class, now); err != nil {
		return domain.Record{}, domain.Verdict{}, err
	}
	cur, err := uc.states.GetForUpdate(ctx, tenantID, class)
	if err != nil {
		return domain.Record{}, domain.Verdict{}, err
	}
	counts, err := uc.stats.WindowCounts(ctx, tenantID, class, uc.policy.WindowStart(now))
	if err != nil {
		return domain.Record{}, domain.Verdict{}, err
	}
	return cur, domain.Evaluate(class, counts, uc.policy.Thresholds), nil
}

// applyChange guarda el nuevo estado, su linea de historial y el evento por la outbox.
// Debe correr dentro de la transaccion que bloqueo la fila.
func (uc *UseCase) applyChange(ctx context.Context, from, to domain.Record, manual bool) (*domain.Change, error) {
	if err := uc.states.Save(ctx, to); err != nil {
		return nil, err
	}
	c := domain.ChangeOf(from, to, manual)
	if err := uc.states.AppendHistory(ctx, &c); err != nil {
		return nil, err
	}
	if err := uc.events.StateChanged(ctx, c); err != nil {
		return nil, err
	}
	return &c, nil
}

// recordChange cuenta y registra una transicion ya confirmada.
func (uc *UseCase) recordChange(c *domain.Change) {
	if c == nil {
		return
	}
	uc.metrics.StateChanged(c.Class, c.To)
	uc.logger.Info("reputation: cambio de estado",
		zap.String("tenant_id", c.TenantID.String()), zap.String("class", string(c.Class)),
		zap.String("from", string(c.From)), zap.String("to", string(c.To)),
		zap.String("reason", c.Reason), zap.Bool("manual", c.Manual),
		zap.String("bounce_rate", c.BounceRate.StringFixed(domain.RateScale)),
		zap.String("complaint_rate", c.ComplaintRate.StringFixed(domain.RateScale)))
}
