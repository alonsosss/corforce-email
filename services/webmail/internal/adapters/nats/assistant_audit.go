package nats

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// SubjectAssistantUsed es el uso del asistente del webmail. Su dueno es el webmail y lo guarda audit en
// el rastro de la empresa (AUDIT_SUBJECTS, stream WEBMAIL). Payload, sin contenido del correo:
// tenant_id, mailbox_id, username, action, outcome, model, input_chars, output_chars, input_tokens,
// output_tokens, messages y at (RFC 3339, UTC).
const SubjectAssistantUsed = "webmail.assistant.used"

// Publisher es lo que el apunte necesita del bus.
type Publisher interface {
	PublishPersistent(subject string, evt events.Event) error
}

// AssistantAudit implementa ports.AssistantAudit. Publica en JetStream con acuse: si el stream no
// existe (audit aun no lo declaro) o NATS no responde, devuelve el error y el caso de uso lo cuenta.
type AssistantAudit struct {
	bus Publisher
}

func NewAssistantAudit(bus Publisher) *AssistantAudit { return &AssistantAudit{bus: bus} }

func (a *AssistantAudit) AssistantUsed(_ context.Context, rec domain.AssistantUsageRecord) error {
	if a.bus == nil {
		return errors.New("sin bus de eventos")
	}
	return a.bus.PublishPersistent(SubjectAssistantUsed, events.Event{
		Type:     SubjectAssistantUsed,
		Source:   "webmail",
		TenantID: rec.TenantID,
		UserID:   rec.MailboxID,
		Data: map[string]interface{}{
			"tenant_id":     rec.TenantID,
			"mailbox_id":    rec.MailboxID,
			"username":      rec.Username,
			"action":        string(rec.Action),
			"outcome":       rec.Outcome,
			"model":         rec.Model,
			"input_chars":   rec.InputChars,
			"output_chars":  rec.OutputChars,
			"input_tokens":  rec.InputTokens,
			"output_tokens": rec.OutputTokens,
			"messages":      rec.Messages,
			"at":            rec.At.UTC().Format("2006-01-02T15:04:05Z07:00"),
		},
	})
}
