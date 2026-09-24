package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
	"github.com/google/uuid"
)

// TestSendInput es la prueba de una version: a quien, desde que remitente y con que valores
// de ejemplo. Las variables que falten las completa el render de prueba.
type TestSendInput struct {
	To        []string
	FromEmail string
	FromName  string
	ReplyTo   string
	Variables map[string]json.RawMessage
}

// SendTest envia una version, tambien un borrador, a unas pocas direcciones por transactional.
// Aqui se comprueba lo que es de templates (la plantilla existe, no esta archivada, la version
// existe, los destinatarios son validos); el remitente verificado, la supresion, reputation y
// el tope de pruebas los decide transactional, que es quien envia.
func (uc *UseCase) SendTest(ctx context.Context, tenantID, userID, templateID uuid.UUID, version int, in TestSendInput) (*ports.TestSendResult, error) {
	if uc.testSender == nil {
		return nil, domain.ErrTestSendUnavailable
	}
	if userID == uuid.Nil {
		return nil, domain.ErrMissingCreator
	}
	to, err := domain.NormalizeTestRecipients(in.To)
	if err != nil {
		return nil, err
	}
	from := strings.TrimSpace(in.FromEmail)
	if !domain.ValidEmailAddress(from) {
		return nil, fmt.Errorf("%w: from.email no es un correo válido", domain.ErrInvalidTestSend)
	}
	replyTo := strings.TrimSpace(in.ReplyTo)
	if replyTo != "" && !domain.ValidEmailAddress(replyTo) {
		return nil, fmt.Errorf("%w: reply_to no es un correo válido", domain.ErrInvalidTestSend)
	}
	t, err := uc.repo.GetTemplate(ctx, tenantID, templateID)
	if err != nil {
		return nil, err
	}
	if t.Status == domain.TemplateStatusArchived {
		return nil, domain.ErrTemplateArchived
	}
	if _, err := uc.repo.GetVersion(ctx, tenantID, templateID, version); err != nil {
		return nil, err
	}
	return uc.testSender.SendTest(ctx, tenantID, ports.TestSendRequest{
		TemplateID: templateID, Version: version,
		FromEmail: from, FromName: strings.TrimSpace(in.FromName), ReplyTo: replyTo,
		To: to, Variables: in.Variables, RequestedBy: userID,
	})
}
