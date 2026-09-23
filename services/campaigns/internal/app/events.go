package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
)

// Eventos propios del stream CAMPAIGNS. Los subjects se escriben literales en cada
// llamada por convencion del repositorio: el registro de eventos los lee ahi. Todos se
// encolan en la outbox dentro de la transaccion del cambio de estado. occurred_at es el
// momento de la transicion (RFC 3339 con nanosegundos, como los de transactional): el
// envelope lleva la hora de publicacion, que con la outbox puede ser posterior.

func eventTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (uc *UseCase) publishScheduled(ctx context.Context, c *domain.Campaign) error {
	return uc.events.Publish(ctx, "campaigns.campaign.scheduled", c.TenantID, map[string]any{
		"tenant_id":   c.TenantID.String(),
		"campaign_id": c.ID.String(),
		"status":      string(c.Status),
		"occurred_at": eventTime(uc.now()),
	})
}

func (uc *UseCase) publishStarted(ctx context.Context, c *domain.Campaign) error {
	return uc.events.Publish(ctx, "campaigns.campaign.started", c.TenantID, map[string]any{
		"tenant_id":   c.TenantID.String(),
		"campaign_id": c.ID.String(),
		"status":      string(c.Status),
		"occurred_at": eventTime(uc.now()),
	})
}

func (uc *UseCase) publishPaused(ctx context.Context, c *domain.Campaign) error {
	return uc.events.Publish(ctx, "campaigns.campaign.paused", c.TenantID, map[string]any{
		"tenant_id":   c.TenantID.String(),
		"campaign_id": c.ID.String(),
		"status":      string(c.Status),
		"reason":      c.PauseReason,
		"occurred_at": eventTime(uc.now()),
	})
}

func (uc *UseCase) publishResumed(ctx context.Context, c *domain.Campaign) error {
	return uc.events.Publish(ctx, "campaigns.campaign.resumed", c.TenantID, map[string]any{
		"tenant_id":   c.TenantID.String(),
		"campaign_id": c.ID.String(),
		"status":      string(c.Status),
		"occurred_at": eventTime(uc.now()),
	})
}

func (uc *UseCase) publishCompleted(ctx context.Context, c *domain.Campaign) error {
	return uc.events.Publish(ctx, "campaigns.campaign.completed", c.TenantID, map[string]any{
		"tenant_id":   c.TenantID.String(),
		"campaign_id": c.ID.String(),
		"status":      string(c.Status),
		"occurred_at": eventTime(uc.now()),
	})
}

func (uc *UseCase) publishCancelled(ctx context.Context, c *domain.Campaign) error {
	return uc.events.Publish(ctx, "campaigns.campaign.cancelled", c.TenantID, map[string]any{
		"tenant_id":   c.TenantID.String(),
		"campaign_id": c.ID.String(),
		"status":      string(c.Status),
		"occurred_at": eventTime(uc.now()),
	})
}

func (uc *UseCase) publishFailed(ctx context.Context, c *domain.Campaign) error {
	return uc.events.Publish(ctx, "campaigns.campaign.failed", c.TenantID, map[string]any{
		"tenant_id":   c.TenantID.String(),
		"campaign_id": c.ID.String(),
		"status":      string(c.Status),
		"reason":      c.FailureReason,
		"occurred_at": eventTime(uc.now()),
	})
}

// publishABDecided deja constancia de la eleccion de la prueba A/B con los contadores con
// que se tomo: es la auditoria de la decision y lo que un consumidor necesita para
// explicarla sin volver a preguntar.
func (uc *UseCase) publishABDecided(ctx context.Context, c *domain.Campaign, d domain.ABDecision) error {
	results := make([]map[string]any, len(d.Results))
	for i, r := range d.Results {
		results[i] = map[string]any{
			"variant":   domain.VariantLabel(r.Variant),
			"accepted":  r.Accepted,
			"delivered": r.Delivered,
			"opened":    r.Opened,
			"clicked":   r.Clicked,
		}
	}
	return uc.events.Publish(ctx, "campaigns.campaign.ab_decided", c.TenantID, map[string]any{
		"tenant_id":   c.TenantID.String(),
		"campaign_id": c.ID.String(),
		"status":      string(c.Status),
		"winner":      domain.VariantLabel(d.Winner),
		"criterion":   string(d.Criterion),
		"reason":      string(d.Reason),
		"results":     results,
		"occurred_at": eventTime(uc.now()),
	})
}
