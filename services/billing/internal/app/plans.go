package app

import (
	"context"
	"strings"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
)

// CreatePlan da de alta un plan activo con sus limites.
func (uc *UseCase) CreatePlan(ctx context.Context, draft domain.Plan) (*domain.Plan, error) {
	p := draft
	p.Name = strings.TrimSpace(p.Name)
	p.Status = domain.PlanActive
	p.Limits = append([]domain.PlanLimit(nil), draft.Limits...)
	if err := p.Validate(); err != nil {
		return nil, err
	}
	domain.SortLimits(p.Limits)
	if err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		return uc.plans.Create(ctx, &p)
	}); err != nil {
		return nil, err
	}
	return &p, nil
}

func (uc *UseCase) GetPlan(ctx context.Context, id uuid.UUID) (*domain.Plan, error) {
	return uc.plans.Get(ctx, id)
}

func (uc *UseCase) ListPlans(ctx context.Context, status domain.PlanStatus) ([]domain.Plan, error) {
	return uc.plans.List(ctx, status)
}

// UpdatePlan edita un plan. Si el cambio toca sus condiciones y el plan ya tiene
// suscripciones, responde domain.ErrPlanInUse: se crea un plan nuevo y se migra a las
// empresas. El bloqueo del plan impide que una suscripcion nueva lo tome mientras tanto.
func (uc *UseCase) UpdatePlan(ctx context.Context, id uuid.UUID, patch domain.PlanPatch) (*domain.Plan, error) {
	var out *domain.Plan
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		p, err := uc.plans.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if patch.ChangesTerms() {
			inUse, err := uc.plans.HasSubscriptions(ctx, id)
			if err != nil {
				return err
			}
			if inUse {
				return domain.ErrPlanInUse
			}
		}
		p.Apply(patch)
		if err := p.Validate(); err != nil {
			return err
		}
		domain.SortLimits(p.Limits)
		if err := uc.plans.Update(ctx, p, patch.ReplaceLimits); err != nil {
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RetirePlan saca el plan del catalogo: sus suscripciones siguen, pero nadie mas lo toma.
// Retirar un plan ya retirado no cambia nada.
func (uc *UseCase) RetirePlan(ctx context.Context, id uuid.UUID) (*domain.Plan, error) {
	var out *domain.Plan
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		p, err := uc.plans.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		out = p
		if p.Status == domain.PlanRetired {
			return nil
		}
		p.Status = domain.PlanRetired
		return uc.plans.Update(ctx, p, false)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
