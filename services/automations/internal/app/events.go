package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
)

// Eventos propios, publicados por la outbox en el stream AUTOMATIONS. Se encolan dentro
// de la transaccion del cambio que describen.

func workflowPayload(w *domain.Workflow, reason string) map[string]any {
	p := map[string]any{
		"tenant_id":   w.TenantID.String(),
		"workflow_id": w.ID.String(),
	}
	if reason != "" {
		p["reason"] = reason
	}
	return p
}

func runPayload(r *domain.Run, reason string) map[string]any {
	p := map[string]any{
		"tenant_id":   r.TenantID.String(),
		"workflow_id": r.WorkflowID.String(),
		"run_id":      r.ID.String(),
		"contact_id":  r.ContactID.String(),
	}
	if reason != "" {
		p["reason"] = reason
	}
	return p
}

func (uc *UseCase) publishActivated(ctx context.Context, w *domain.Workflow) error {
	return uc.events.Publish(ctx, "automations.workflow.activated", w.TenantID, workflowPayload(w, ""))
}

func (uc *UseCase) publishPaused(ctx context.Context, w *domain.Workflow) error {
	return uc.events.Publish(ctx, "automations.workflow.paused", w.TenantID, workflowPayload(w, w.PauseReason))
}

func (uc *UseCase) publishArchived(ctx context.Context, w *domain.Workflow) error {
	return uc.events.Publish(ctx, "automations.workflow.archived", w.TenantID, workflowPayload(w, ""))
}

func (uc *UseCase) publishRunCompleted(ctx context.Context, r *domain.Run) error {
	return uc.events.Publish(ctx, "automations.run.completed", r.TenantID, runPayload(r, ""))
}

func (uc *UseCase) publishRunFailed(ctx context.Context, r *domain.Run, reason string) error {
	return uc.events.Publish(ctx, "automations.run.failed", r.TenantID, runPayload(r, reason))
}
