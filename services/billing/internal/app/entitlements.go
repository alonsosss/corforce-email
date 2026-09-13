package app

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// CheckEntitlement responde si la empresa puede consumir quantity unidades mas del
// recurso. Solo consulta: el consumo se cuenta cuando llega el evento del hecho consumado.
func (uc *UseCase) CheckEntitlement(ctx context.Context, tenantID uuid.UUID, resource domain.Resource, quantity int64) (domain.Entitlement, error) {
	if _, err := domain.ParseResource(string(resource)); err != nil {
		return domain.Entitlement{}, err
	}
	if quantity < 1 || quantity > domain.MaxCheckQuantity {
		return domain.Entitlement{}, fmt.Errorf("%w: quantity debe estar entre 1 y %d", domain.ErrInvalidQuantity, domain.MaxCheckQuantity)
	}
	sub, plan, err := uc.subscriptionWithPlan(ctx, tenantID)
	if err != nil {
		return domain.Entitlement{}, err
	}
	in := domain.EntitlementInput{Resource: resource, Quantity: quantity, Subscription: sub, Enforce: uc.cfg.Enforce}
	counterPeriod := domain.StockPeriodStart
	if sub != nil {
		limit := plan.EffectiveLimit(resource)
		in.Limit = &limit
		start, _ := sub.PeriodAt(uc.now(), plan.BillingPeriod)
		counterPeriod = resource.CounterPeriod(start)
	}
	// Sin suscripcion no hay periodo en que buscar el consumo de flujo.
	if sub != nil || !resource.IsFlow() {
		if in.Used, err = uc.usage.Quantity(ctx, tenantID, resource, counterPeriod); err != nil {
			return domain.Entitlement{}, err
		}
	}
	ent := domain.Evaluate(in)
	if sub == nil {
		uc.logger.Warn("billing: consulta de derechos de una empresa sin suscripcion",
			zap.String("tenant_id", tenantID.String()), zap.String("resource", string(resource)),
			zap.Bool("allowed", ent.Allowed), zap.Bool("enforce", uc.cfg.Enforce))
	}
	return ent, nil
}
