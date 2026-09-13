package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// IngestSESEvent registra un evento de SES ya verificado, actualiza el estado del
// mensaje y, ante un rebote permanente o una queja, alimenta la lista de supresion y
// publica el hecho. Idempotente por sns_message_id: una notificacion repetida no cuenta.
//
// La supresion se da de alta ANTES de la transaccion: si suppression no responde, no se
// registra nada y SNS reintentara la notificacion completa.
func (uc *UseCase) IngestSESEvent(ctx context.Context, routeTenant uuid.UUID, ev domain.InboundEvent) error {
	if ev.TenantID != routeTenant {
		return domain.ErrTenantMismatch
	}
	if ev.Type == "" {
		return domain.NewValidationError("eventType no reconocido")
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = uc.now()
	}

	summary := eventSummary(ev.Detail)
	suppressReason := ""
	switch {
	case ev.Type == domain.EventBounce && ev.BounceType == domain.BounceTypePermanent:
		suppressReason = "hard_bounce"
	case ev.Type == domain.EventComplaint:
		suppressReason = "complaint"
	}
	var known *domain.MessageAttribution
	if suppressReason != "" {
		known = uc.attributionFor(ctx, ev.TenantID, ev.MessageID)
		for _, email := range ev.Recipients {
			if err := uc.suppression.Add(ctx, ev.TenantID, ports.SuppressionEntry{
				Email: email, Reason: suppressReason, Source: uc.cfg.Source,
				Detail: summary, MessageID: ev.MessageID.String(), CampaignID: campaignOf(known),
			}); err != nil {
				uc.logger.Warn("transactional: no se pudo suprimir tras evento de SES", zap.Error(err))
				return domain.ErrSuppressionUnavailable
			}
		}
	}

	return uc.repo.Transact(ctx, func(ctx context.Context) error {
		snsID := ev.SNSMessageID
		inserted, err := uc.repo.InsertEvent(ctx, &domain.Event{
			ID: uuid.New(), TenantID: ev.TenantID, MessageID: ev.MessageID, Type: ev.Type,
			Recipient: firstOf(ev.Recipients), Detail: ev.Detail, SNSMessageID: optional(snsID),
			OccurredAt: ev.OccurredAt, CreatedAt: uc.now(),
		})
		if err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		if status, from := domain.StatusForEvent(ev.Type); status != "" {
			if _, err := uc.repo.TransitionStatus(ctx, ev.TenantID, ev.MessageID, status, from); err != nil {
				return err
			}
		}
		if !publishesEvent(ev.Type) {
			return nil
		}
		attr, err := uc.resolveAttribution(ctx, ev.TenantID, ev.MessageID, known)
		if err != nil {
			return err
		}
		return uc.publishDeliveryEvents(ctx, ev, attr, summary)
	})
}

// publishesEvent dice que eventos de SES se convierten en un transactional.email.*.
func publishesEvent(eventType string) bool {
	switch eventType {
	case domain.EventBounce, domain.EventComplaint, domain.EventDelivery, domain.EventOpen, domain.EventClick:
		return true
	}
	return false
}

// publishDeliveryEvents publica el hecho con la atribucion del mensaje. Rebotes, quejas y
// entregas van uno por destinatario afectado. Aperturas y clics llegan por mensaje: SES no
// sabe quien de varios destinatarios abrio, asi que email solo lleva la direccion cuando
// el mensaje tiene un unico destinatario (siempre en marketing).
func (uc *UseCase) publishDeliveryEvents(ctx context.Context, ev domain.InboundEvent, attr domain.MessageAttribution, summary string) error {
	switch ev.Type {
	case domain.EventBounce:
		for _, email := range ev.Recipients {
			if err := uc.events.Publish(ctx, "transactional.email.bounced", ev.TenantID, map[string]any{
				"tenant_id":   ev.TenantID.String(),
				"message_id":  ev.MessageID.String(),
				"email":       email,
				"bounce_type": ev.BounceType,
				"detail":      summary,
				"class":       attr.Class,
				"campaign_id": nullableID(attr.CampaignID),
				"contact_id":  nullableID(attr.ContactID),
				"test":        attr.Test,
				"occurred_at": eventTime(ev.OccurredAt),
			}); err != nil {
				return err
			}
		}
	case domain.EventComplaint:
		for _, email := range ev.Recipients {
			if err := uc.events.Publish(ctx, "transactional.email.complained", ev.TenantID, map[string]any{
				"tenant_id":   ev.TenantID.String(),
				"message_id":  ev.MessageID.String(),
				"email":       email,
				"detail":      summary,
				"class":       attr.Class,
				"campaign_id": nullableID(attr.CampaignID),
				"contact_id":  nullableID(attr.ContactID),
				"test":        attr.Test,
				"occurred_at": eventTime(ev.OccurredAt),
			}); err != nil {
				return err
			}
		}
	case domain.EventDelivery:
		for _, email := range ev.Recipients {
			if err := uc.events.Publish(ctx, "transactional.email.delivered", ev.TenantID, map[string]any{
				"tenant_id":   ev.TenantID.String(),
				"message_id":  ev.MessageID.String(),
				"email":       email,
				"class":       attr.Class,
				"campaign_id": nullableID(attr.CampaignID),
				"contact_id":  nullableID(attr.ContactID),
				"test":        attr.Test,
				"occurred_at": eventTime(ev.OccurredAt),
			}); err != nil {
				return err
			}
		}
	case domain.EventOpen:
		return uc.events.Publish(ctx, "transactional.email.opened", ev.TenantID, map[string]any{
			"tenant_id":   ev.TenantID.String(),
			"message_id":  ev.MessageID.String(),
			"email":       soleRecipient(ev.Recipients),
			"class":       attr.Class,
			"campaign_id": nullableID(attr.CampaignID),
			"contact_id":  nullableID(attr.ContactID),
			"test":        attr.Test,
			"occurred_at": eventTime(ev.OccurredAt),
		})
	case domain.EventClick:
		return uc.events.Publish(ctx, "transactional.email.clicked", ev.TenantID, map[string]any{
			"tenant_id":   ev.TenantID.String(),
			"message_id":  ev.MessageID.String(),
			"email":       soleRecipient(ev.Recipients),
			"link":        detailString(ev.Detail, "link"),
			"class":       attr.Class,
			"campaign_id": nullableID(attr.CampaignID),
			"contact_id":  nullableID(attr.ContactID),
			"test":        attr.Test,
			"occurred_at": eventTime(ev.OccurredAt),
		})
	}
	return nil
}

// IsIgnorableIngestError distingue los fallos que reintentar no arregla (mensaje
// desconocido) de los que si (base caida).
func IsIgnorableIngestError(err error) bool {
	return errors.Is(err, domain.ErrNotFound)
}

func firstOf(list []string) string {
	if len(list) > 0 {
		return list[0]
	}
	return ""
}

// soleRecipient devuelve la direccion solo si hay exactamente una.
func soleRecipient(list []string) string {
	if len(list) == 1 {
		return list[0]
	}
	return ""
}

// maxSummaryLen acota el resumen que viaja a suppression, que lo guarda como texto.
const maxSummaryLen = 500

// eventSummary resume el detalle del evento en una linea (causa y diagnostico SMTP).
func eventSummary(detail map[string]any) string {
	summary := detailString(detail, "reason")
	if diag := detailString(detail, "diagnostic_code"); diag != "" {
		if summary != "" {
			summary += ": "
		}
		summary += diag
	}
	if len(summary) > maxSummaryLen {
		summary = summary[:maxSummaryLen]
	}
	return summary
}

func detailString(detail map[string]any, key string) string {
	if detail == nil {
		return ""
	}
	s, _ := detail[key].(string)
	return s
}
