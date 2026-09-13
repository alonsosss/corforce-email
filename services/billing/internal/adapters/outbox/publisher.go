// Package outbox publica los eventos de billing con garantia transaccional: cada uno se
// encola en platform.event_outbox del registro dentro de la transaccion que lo origina, y
// el rele de pkg/outbox (en main) lo entrega a JetStream.
package outbox

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/outbox"
	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/google/uuid"
)

// Subjects propios; main declara el stream que los cubre.
const (
	StreamName     = "BILLING"
	StreamSubjects = "billing.>"

	SubjectSubscriptionCreated   = "billing.subscription.created"
	SubjectSubscriptionChanged   = "billing.subscription.changed"
	SubjectSubscriptionSuspended = "billing.subscription.suspended"
	SubjectPeriodClosed          = "billing.period.closed"
	SubjectLimitReached          = "billing.limit.reached"

	source     = "billing"
	dateLayout = "2006-01-02"
)

type Publisher struct {
	q outbox.Execer
}

// NewPublisher recibe el Store del registro: Enqueue escribe por la transaccion del contexto.
func NewPublisher(q outbox.Execer) *Publisher { return &Publisher{q: q} }

func (p *Publisher) SubscriptionCreated(ctx context.Context, s *domain.Subscription) error {
	return p.enqueue(ctx, SubjectSubscriptionCreated, s.TenantID, subscriptionData(s))
}

func (p *Publisher) SubscriptionChanged(ctx context.Context, s *domain.Subscription, previousStatus domain.SubscriptionStatus, previousPlanCode string) error {
	data := subscriptionData(s)
	data["previous_status"] = string(previousStatus)
	data["previous_plan_code"] = previousPlanCode
	return p.enqueue(ctx, SubjectSubscriptionChanged, s.TenantID, data)
}

func (p *Publisher) SubscriptionSuspended(ctx context.Context, s *domain.Subscription, previousStatus domain.SubscriptionStatus) error {
	data := subscriptionData(s)
	data["previous_status"] = string(previousStatus)
	return p.enqueue(ctx, SubjectSubscriptionSuspended, s.TenantID, data)
}

// PeriodClosed deja constancia de lo consumido en un periodo: es la entrada de una futura
// facturacion.
func (p *Publisher) PeriodClosed(ctx context.Context, s *domain.Subscription, start, end time.Time, usage map[domain.Resource]int64) error {
	quantities := make(map[string]int64, len(usage))
	for r, q := range usage {
		quantities[string(r)] = q
	}
	return p.enqueue(ctx, SubjectPeriodClosed, s.TenantID, map[string]interface{}{
		"tenant_id":       s.TenantID.String(),
		"subscription_id": s.ID.String(),
		"plan_code":       s.PlanCode,
		"period_start":    start.Format(dateLayout),
		"period_end":      end.Format(dateLayout),
		"usage":           quantities,
	})
}

func (p *Publisher) LimitReached(ctx context.Context, tenantID uuid.UUID, limit domain.PlanLimit, periodStart time.Time, used int64) error {
	return p.enqueue(ctx, SubjectLimitReached, tenantID, map[string]interface{}{
		"tenant_id":    tenantID.String(),
		"resource":     string(limit.Resource),
		"period_start": periodStart.Format(dateLayout),
		"limit":        limit.Included,
		"used":         used,
	})
}

func subscriptionData(s *domain.Subscription) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id":            s.TenantID.String(),
		"subscription_id":      s.ID.String(),
		"plan_id":              s.PlanID.String(),
		"plan_code":            s.PlanCode,
		"status":               string(s.Status),
		"current_period_start": s.CurrentPeriodStart.Format(dateLayout),
		"current_period_end":   s.CurrentPeriodEnd.Format(dateLayout),
		"trial_ends_at":        timestamp(s.TrialEndsAt),
		"cancel_at":            timestamp(s.CancelAt),
	}
}

func timestamp(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// enqueue arma el envelope. UserID lleva a quien actuo cuando el cambio vino de una
// persona de la plataforma; los que nacen de eventos o del reloj van sin usuario.
func (p *Publisher) enqueue(ctx context.Context, subject string, tenantID uuid.UUID, data map[string]interface{}) error {
	return outbox.Enqueue(ctx, p.q, subject, events.Event{
		Type:     subject,
		Source:   source,
		TenantID: tenantID.String(),
		UserID:   middleware.GetUserID(ctx),
		Data:     data,
	})
}
