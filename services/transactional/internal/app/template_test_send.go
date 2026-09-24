package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

// testSendWindow es la ventana del tope de pruebas por empresa.
const testSendWindow = time.Hour

// TemplateTestCommand es el contrato de POST /internal/transactional/test-send: la prueba de
// una version de plantilla, tambien de un borrador, que pide templates desde el editor.
type TemplateTestCommand struct {
	TenantID        uuid.UUID
	RequestedBy     *uuid.UUID
	From            domain.Recipient
	ReplyTo         string
	To              []string
	TemplateID      uuid.UUID
	TemplateVersion int
	Variables       map[string]any
}

// TemplateTestResult son los mensajes de prueba encolados y las direcciones que la supresion
// retiro.
type TemplateTestResult struct {
	Messages   []MessageSummary   `json:"messages"`
	Suppressed []ports.Suppressed `json:"suppressed"`
}

// CreateTemplateTest envia una version de plantilla a unas pocas direcciones de prueba. Sale
// con el remitente verificado de la empresa (nunca el de la plataforma), por el carril del
// tipo de la plantilla, con "[Prueba] " delante del asunto y marcada como prueba: sus eventos
// llevan test=true y no cuentan en la analitica, la reputacion ni la facturacion. La supresion
// se respeta igual que en un envio real y reputation puede denegarla (una empresa suspendida
// no envia ni pruebas). Las variables que falten las completa templates con valores de
// ejemplo; los enlaces de baja y de ver en el navegador son los reales del mensaje de prueba.
func (uc *UseCase) CreateTemplateTest(ctx context.Context, cmd TemplateTestCommand) (*TemplateTestResult, error) {
	if err := validateTemplateTest(&cmd); err != nil {
		return nil, err
	}
	if err := uc.requireSendingDomain(ctx, cmd.TenantID, cmd.From.Email); err != nil {
		return nil, err
	}
	used, err := uc.repo.CountTestMessagesSince(ctx, cmd.TenantID, uc.now().Add(-testSendWindow))
	if err != nil {
		return nil, err
	}
	if used+len(cmd.To) > uc.cfg.TestSendsPerHour {
		return nil, domain.ErrTestSendLimit
	}

	suppressed, blocked, err := uc.checkSuppressed(ctx, cmd.TenantID, cmd.To)
	if err != nil {
		return nil, err
	}
	result := &TemplateTestResult{Messages: []MessageSummary{}, Suppressed: suppressed}
	var messages []*domain.Message
	for _, email := range cmd.To {
		if blocked[strings.ToLower(email)] {
			continue
		}
		msg, err := uc.buildTestMessage(ctx, cmd, email)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if len(messages) == 0 {
		return result, nil
	}
	class := messages[0].Class
	if err := uc.authorize(ctx, cmd.TenantID, class, len(messages), class == domain.ClassTransactional); err != nil {
		return nil, err
	}
	err = uc.repo.Transact(ctx, func(ctx context.Context) error {
		for _, m := range messages {
			if err := uc.repo.InsertMessage(ctx, m); err != nil {
				return err
			}
			if err := uc.publishQueued(ctx, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, m := range messages {
		result.Messages = append(result.Messages, MessageSummary{ID: m.ID, Status: m.Status, To: m.To})
	}
	return result, nil
}

func validateTemplateTest(cmd *TemplateTestCommand) error {
	cmd.From.Email = domain.NormalizeEmail(cmd.From.Email)
	cmd.From.Name = strings.TrimSpace(cmd.From.Name)
	if !domain.ValidEmail(cmd.From.Email) {
		return domain.NewValidationError("from.email must be a valid email")
	}
	cmd.ReplyTo = domain.NormalizeEmail(cmd.ReplyTo)
	if cmd.ReplyTo != "" && !domain.ValidEmail(cmd.ReplyTo) {
		return domain.NewValidationError("reply_to must be a valid email")
	}
	if cmd.TemplateID == uuid.Nil {
		return domain.NewValidationError("template_id is required")
	}
	if cmd.TemplateVersion < 1 {
		return domain.NewValidationError("template_version must be >= 1")
	}
	if len(cmd.To) == 0 || len(cmd.To) > domain.MaxTestRecipients {
		return domain.NewValidationError("to must have between 1 and %d recipients", domain.MaxTestRecipients)
	}
	seen := make(map[string]bool, len(cmd.To))
	for i, email := range cmd.To {
		email = domain.NormalizeEmail(email)
		if !domain.ValidEmail(email) {
			return domain.NewValidationError("to[%d] must be a valid email", i)
		}
		key := strings.ToLower(email)
		if seen[key] {
			return domain.NewValidationError("recipient %s appears more than once", email)
		}
		seen[key] = true
		cmd.To[i] = email
	}
	if cmd.Variables == nil {
		cmd.Variables = map[string]any{}
	}
	return nil
}

// buildTestMessage renderiza la version para un destinatario de prueba y fija el carril por el
// tipo que informa templates: una plantilla de marketing sale por el de marketing, con su
// configuration set y su cabecera de baja, aunque no haya campana.
func (uc *UseCase) buildTestMessage(ctx context.Context, cmd TemplateTestCommand, email string) (*domain.Message, error) {
	templateID, version := cmd.TemplateID, cmd.TemplateVersion
	msg := uc.newMessage(CreateMessagesCommand{
		TenantID:        cmd.TenantID,
		CreatedBy:       cmd.RequestedBy,
		From:            cmd.From,
		ReplyTo:         cmd.ReplyTo,
		TemplateID:      &templateID,
		TemplateVersion: &version,
		Variables:       cmd.Variables,
		Tags:            map[string]string{domain.TestSendTag: domain.TestSendValue},
	}, []domain.Recipient{{Email: email}})
	rendered, err := uc.templates.Render(ctx, cmd.TenantID, ports.RenderRequest{
		TemplateID: templateID,
		Version:    &version,
		Variables:  cmd.Variables,
		Reserved:   uc.reservedFor(cmd.TenantID, msg.ID, email),
		Test:       true,
	})
	if err != nil {
		return nil, err
	}
	switch rendered.Kind {
	case domain.TemplateKindMarketing:
		msg.Class = domain.ClassMarketing
		msg.Unsubscribable = true
	case domain.TemplateKindTransactional:
	default:
		return nil, fmt.Errorf("%w: el render no informa el tipo de plantilla", domain.ErrTemplatesUnavailable)
	}
	if len(rendered.HTML)+len(rendered.Text) > domain.MaxBodyBytes {
		return nil, domain.NewValidationError("la plantilla renderizada supera el límite de %d bytes", domain.MaxBodyBytes)
	}
	msg.Subject = domain.TestSubjectPrefix + rendered.Subject
	msg.HTML = optional(rendered.HTML)
	msg.Text = optional(rendered.Text)
	renderedVersion := rendered.Version
	msg.TemplateVersion = &renderedVersion
	msg.Test = true
	return msg, nil
}
