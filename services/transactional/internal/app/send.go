package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// transientRetryDelays son los reintentos inmediatos ante throttling o 5xx del proveedor,
// antes de devolver el mensaje a la cola (sin ack) para que JetStream lo reentregue.
var transientRetryDelays = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}

// SendOutcome dice al consumidor que hacer con la entrega: Ack confirma (enviado, fallido
// definitivo o ya procesado); sin Ack la cola reentrega mas tarde.
type SendOutcome struct {
	Ack    bool
	Status string
}

// SendQueued entrega un mensaje de la cola al proveedor. Es idempotente: si el mensaje
// ya no esta en queued (otra entrega lo envio) se confirma sin reenviar. La fila queda
// bloqueada durante el envio para que dos workers no compitan por el mismo mensaje.
func (uc *UseCase) SendQueued(ctx context.Context, tenantID, messageID uuid.UUID) (SendOutcome, error) {
	outcome := SendOutcome{Ack: true}
	err := uc.repo.Transact(ctx, func(ctx context.Context) error {
		msg, err := uc.repo.LockQueuedMessage(ctx, tenantID, messageID)
		if errors.Is(err, domain.ErrNotFound) {
			uc.logger.Warn("transactional: mensaje encolado inexistente; se descarta",
				zap.String("tenant_id", tenantID.String()), zap.String("message_id", messageID.String()))
			return nil
		}
		if err != nil {
			return err
		}
		if msg.Status != domain.StatusQueued {
			outcome.Status = msg.Status
			return nil
		}
		// El carril sale de la clase guardada en el mensaje, no del subject por el que
		// llego: un mensaje de marketing nunca sale por el emisor ni la tasa transaccional.
		lane, err := uc.laneFor(msg.Class)
		if err != nil {
			uc.logger.Error("transactional: la clase del mensaje no tiene carril; queda en la cola",
				zap.String("message_id", msg.ID.String()), zap.String("class", msg.Class))
			return fmt.Errorf("mensaje %s: %w", msg.ID, err)
		}

		email := uc.outgoing(msg)
		if domain.OriginOrDefault(msg.Origin) == domain.OriginSMTP {
			raw, err := uc.repo.GetRawContent(ctx, tenantID, msg.ID)
			if errors.Is(err, domain.ErrNotFound) {
				return uc.failMessage(ctx, tenantID, msg, &domain.SendError{Kind: domain.ErrorPermanent, Code: "RawContentMissing",
					Message: "el mensaje de SMTP no tiene su contenido guardado"}, &outcome)
			}
			if err != nil {
				return err
			}
			email.Raw = raw
		}
		providerID, sendErr := uc.sendWithRetries(ctx, domain.ClassOrDefault(msg.Class), lane, email)
		if sendErr == nil {
			sentAt := uc.now()
			if err := uc.repo.MarkSent(ctx, tenantID, msg.ID, providerID, sentAt); err != nil {
				return err
			}
			if _, err := uc.repo.InsertEvent(ctx, &domain.Event{
				ID: uuid.New(), TenantID: tenantID, MessageID: msg.ID, Type: domain.EventSend,
				Recipient:  firstRecipient(msg),
				Detail:     map[string]any{"ses_message_id": providerID, "source": domain.OriginOrDefault(msg.Origin)},
				OccurredAt: sentAt, CreatedAt: sentAt,
			}); err != nil {
				return err
			}
			outcome.Status = domain.StatusSent
			attr := msg.Attribution()
			return uc.events.Publish(ctx, "transactional.email.sent", tenantID, map[string]any{
				"tenant_id":      tenantID.String(),
				"message_id":     msg.ID.String(),
				"ses_message_id": providerID,
				"to":             msg.AllRecipients(),
				"class":          attr.Class,
				"campaign_id":    nullableID(attr.CampaignID),
				"contact_id":     nullableID(attr.ContactID),
				"test":           attr.Test,
				"occurred_at":    eventTime(sentAt),
			})
		}

		var se *domain.SendError
		if !errors.As(sendErr, &se) {
			se = &domain.SendError{Kind: domain.ErrorTransient, Code: "unknown", Message: sendErr.Error()}
		}
		if se.Kind == domain.ErrorTransient {
			attempts, err := uc.repo.RecordAttempt(ctx, tenantID, msg.ID, se.Error())
			if err != nil {
				return err
			}
			if attempts < domain.MaxSendAttempts {
				uc.logger.Warn("transactional: fallo transitorio del proveedor; se reintentara",
					zap.String("message_id", msg.ID.String()), zap.Int("attempts", attempts), zap.Error(se))
				outcome.Ack = false
				outcome.Status = domain.StatusQueued
				return nil
			}
			se = &domain.SendError{Kind: domain.ErrorPermanent, Code: se.Code,
				Message: fmt.Sprintf("reintentos agotados (%d): %s", attempts, se.Message)}
		}
		return uc.failMessage(ctx, tenantID, msg, se, &outcome)
	})
	if err != nil {
		return SendOutcome{Ack: false}, err
	}
	return outcome, nil
}

func (uc *UseCase) failMessage(ctx context.Context, tenantID uuid.UUID, msg *domain.Message, se *domain.SendError, outcome *SendOutcome) error {
	if err := uc.repo.MarkFailed(ctx, tenantID, msg.ID, se.Error()); err != nil {
		return err
	}
	uc.logger.Error("transactional: el proveedor rechazo el mensaje",
		zap.String("message_id", msg.ID.String()), zap.String("code", se.Code), zap.String("detail", se.Message))
	outcome.Status = domain.StatusFailed
	attr := msg.Attribution()
	return uc.events.Publish(ctx, "transactional.email.failed", tenantID, map[string]any{
		"tenant_id":   tenantID.String(),
		"message_id":  msg.ID.String(),
		"code":        se.Code,
		"error":       se.Message,
		"to":          msg.AllRecipients(),
		"class":       attr.Class,
		"campaign_id": nullableID(attr.CampaignID),
		"contact_id":  nullableID(attr.ContactID),
		"test":        attr.Test,
		"occurred_at": eventTime(uc.now()),
	})
}

// sendWithRetries respeta el limitador de tasa del carril en cada intento y repite solo
// los fallos transitorios, con una espera corta entre ellos.
func (uc *UseCase) sendWithRetries(ctx context.Context, class string, lane Lane, email domain.OutgoingEmail) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= len(transientRetryDelays); attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(transientRetryDelays[attempt-1])
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
		if err := lane.Limiter.Wait(ctx); err != nil {
			return "", err
		}
		id, err := lane.Sender.Send(ctx, email)
		uc.metrics.SendAttempt(class, domain.SendResult(err))
		if err == nil {
			return id, nil
		}
		lastErr = err
		var se *domain.SendError
		if errors.As(err, &se) && se.Kind == domain.ErrorPermanent {
			return "", err
		}
	}
	return "", lastErr
}

// outgoing resuelve el correo final a partir del mensaje guardado.
func (uc *UseCase) outgoing(msg *domain.Message) domain.OutgoingEmail {
	email := domain.OutgoingEmail{
		MessageID: msg.ID,
		TenantID:  msg.TenantID,
		From:      domain.FormatAddress(msg.FromName, msg.FromEmail),
		To:        formatRecipients(msg.To),
		Cc:        formatRecipients(msg.Cc),
		Bcc:       formatRecipients(msg.Bcc),
		Subject:   msg.Subject,
		Headers:   msg.Headers,
		Tags:      msg.Tags,
	}
	if msg.ReplyTo != nil {
		email.ReplyTo = []string{*msg.ReplyTo}
	}
	if msg.HTML != nil {
		email.HTML = *msg.HTML
	}
	if msg.Text != nil {
		email.Text = *msg.Text
	}
	if msg.Unsubscribable && len(msg.To) == 1 {
		email.UnsubscribeURL = uc.links.UnsubscribeURL(domain.UnsubscribeClaims{
			TenantID: msg.TenantID, MessageID: msg.ID, Email: msg.To[0].Email,
		})
	}
	return email
}

func formatRecipients(list []domain.Recipient) []string {
	out := make([]string, 0, len(list))
	for _, r := range list {
		out = append(out, domain.FormatAddress(r.Name, r.Email))
	}
	return out
}

func firstRecipient(msg *domain.Message) string {
	if len(msg.To) > 0 {
		return msg.To[0].Email
	}
	return ""
}
