package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/alonsosss/corforce-email/services/billing/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TenantStatusSuspended es el estado de empresa suspendida en organization.tenants.status,
// tal como lo publica organization.tenant.status_changed.
const TenantStatusSuspended = "suspended"

// GetSubscription devuelve la suscripcion de la empresa con su plan.
func (uc *UseCase) GetSubscription(ctx context.Context, tenantID uuid.UUID) (*domain.Subscription, *domain.Plan, error) {
	sub, plan, err := uc.subscriptionWithPlan(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	if sub == nil {
		return nil, nil, domain.ErrSubscriptionNotFound
	}
	return sub, plan, nil
}

func (uc *UseCase) ListSubscriptions(ctx context.Context, f ports.SubscriptionFilter) ([]domain.Subscription, int64, error) {
	return uc.subs.List(ctx, f)
}

// PutSubscriptionInput es lo que la plataforma fija de la suscripcion de una empresa.
type PutSubscriptionInput struct {
	PlanCode string
	Update   domain.SubscriptionUpdate
}

// PutSubscription asigna un plan a una empresa o se lo cambia. Un cambio de plan aplica
// desde ya y no reinicia el periodo. Devuelve si la suscripcion es nueva.
func (uc *UseCase) PutSubscription(ctx context.Context, tenantID uuid.UUID, in PutSubscriptionInput) (*domain.Subscription, bool, error) {
	if tenantID == uuid.Nil {
		return nil, false, domain.ErrInvalidSubscription
	}
	now := uc.now()
	var (
		out     *domain.Subscription
		created bool
	)
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		plan, err := uc.plans.GetByCode(ctx, in.PlanCode)
		if errors.Is(err, domain.ErrPlanNotFound) {
			return domain.ErrUnknownPlanCode
		}
		if err != nil {
			return err
		}
		sub, err := uc.subs.GetByTenantForUpdate(ctx, tenantID)
		if errors.Is(err, domain.ErrSubscriptionNotFound) {
			out, err = uc.openSubscription(ctx, tenantID, plan, in.Update, now)
			created = err == nil
			return err
		}
		if err != nil {
			return err
		}
		out = sub
		return uc.changeSubscription(ctx, sub, plan, in.Update, now)
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

func (uc *UseCase) openSubscription(ctx context.Context, tenantID uuid.UUID, plan *domain.Plan, u domain.SubscriptionUpdate, now time.Time) (*domain.Subscription, error) {
	var trial *time.Time
	if u.TrialEndsAt.Set {
		trial = u.TrialEndsAt.Value
	}
	sub, err := domain.NewSubscription(tenantID, plan, now, trial)
	if err != nil {
		return nil, err
	}
	if err := sub.ApplyUpdate(u, now); err != nil {
		return nil, err
	}
	ok, err := uc.subs.Create(ctx, sub)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrSubscriptionExists
	}
	return sub, uc.events.SubscriptionCreated(ctx, sub)
}

func (uc *UseCase) changeSubscription(ctx context.Context, sub *domain.Subscription, plan *domain.Plan, u domain.SubscriptionUpdate, now time.Time) error {
	before := *sub
	if err := sub.ChangePlan(plan); err != nil {
		return err
	}
	if err := sub.ApplyUpdate(u, now); err != nil {
		return err
	}
	if sub.Status == before.Status && sub.PlanID == before.PlanID &&
		sameTime(sub.TrialEndsAt, before.TrialEndsAt) && sameTime(sub.CancelAt, before.CancelAt) {
		return nil
	}
	if err := uc.subs.Update(ctx, sub); err != nil {
		return err
	}
	if sub.Status == domain.StatusSuspended && before.Status != domain.StatusSuspended {
		return uc.events.SubscriptionSuspended(ctx, sub, before.Status)
	}
	return uc.events.SubscriptionChanged(ctx, sub, before.Status, before.PlanCode)
}

// OnTenantCreated abre la suscripcion de una empresa nueva con el plan por defecto. Sin
// plan por defecto utilizable no inventa ninguno: lo avisa y la plataforma lo asigna.
func (uc *UseCase) OnTenantCreated(ctx context.Context, eventID, subject string, tenantID uuid.UUID) error {
	if err := validateEventRef(eventID, tenantID); err != nil {
		return err
	}
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		fresh, err := uc.ledger.MarkProcessed(ctx, eventID, subject)
		if err != nil || !fresh {
			return err
		}
		log := uc.logger.With(zap.String("tenant_id", tenantID.String()), zap.String("plan_code", uc.cfg.DefaultPlanCode))
		if uc.cfg.DefaultPlanCode == "" {
			log.Warn("billing: empresa nueva sin suscripcion: BILLING_DEFAULT_PLAN_CODE esta vacio")
			return nil
		}
		plan, err := uc.plans.GetByCode(ctx, uc.cfg.DefaultPlanCode)
		if errors.Is(err, domain.ErrPlanNotFound) {
			log.Warn("billing: empresa nueva sin suscripcion: el plan por defecto no existe")
			return nil
		}
		if err != nil {
			return err
		}
		if !plan.Assignable() {
			log.Warn("billing: empresa nueva sin suscripcion: el plan por defecto esta retirado")
			return nil
		}
		now := uc.now()
		var trial *time.Time
		if uc.cfg.TrialDays > 0 {
			end := now.Add(time.Duration(uc.cfg.TrialDays) * 24 * time.Hour)
			trial = &end
		}
		sub, err := domain.NewSubscription(tenantID, plan, now, trial)
		if err != nil {
			return err
		}
		ok, err := uc.subs.Create(ctx, sub)
		if err != nil {
			return err
		}
		if !ok {
			log.Info("billing: la empresa ya tenia suscripcion")
			return nil
		}
		return uc.events.SubscriptionCreated(ctx, sub)
	})
}

// OnTenantStatusChanged suspende la suscripcion cuando la empresa se suspende. Reactivar
// la empresa no reactiva la suscripcion: esa decision es de la plataforma (PUT).
func (uc *UseCase) OnTenantStatusChanged(ctx context.Context, eventID, subject string, tenantID uuid.UUID, status string) error {
	if err := validateEventRef(eventID, tenantID); err != nil {
		return err
	}
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		fresh, err := uc.ledger.MarkProcessed(ctx, eventID, subject)
		if err != nil || !fresh || status != TenantStatusSuspended {
			return err
		}
		sub, err := uc.subs.GetByTenantForUpdate(ctx, tenantID)
		if errors.Is(err, domain.ErrSubscriptionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if sub.Status == domain.StatusSuspended || sub.Status == domain.StatusCancelled {
			return nil
		}
		previous := sub.Status
		sub.Status = domain.StatusSuspended
		if err := uc.subs.Update(ctx, sub); err != nil {
			return err
		}
		return uc.events.SubscriptionSuspended(ctx, sub, previous)
	})
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
