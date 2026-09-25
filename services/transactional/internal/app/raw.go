package app

import (
	"context"
	"strings"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// RawMessageCommand es un mensaje MIME que llega por smtp-relay, ya leido y limpiado por el
// adaptador (pkg/rawmail): el sobre SMTP, lo que dice su cabecera y el MIME que saldra.
type RawMessageCommand struct {
	TenantID       uuid.UUID
	APIKeyID       *uuid.UUID
	IdempotencyKey string
	EnvelopeFrom   string
	Recipients     []string
	From           domain.Recipient
	// Sender es la cabecera Sender, si la hay: tambien firma el mensaje ante quien lo recibe.
	Sender  string
	ReplyTo []string
	Subject string
	Text    string
	HTML    string
	Raw     []byte
	// Bulk: el mensaje se declara masivo en sus cabeceras (rawmail.Message.Bulk). No cambia como
	// se envia; solo cuenta para vigilar el umbral de la cuenta compartida.
	Bulk bool
}

// CreateRawMessage aplica al mensaje de SMTP las reglas del API: remitente (el del sobre, el de
// From y el de Sender) de un dominio de envio verificado de la empresa, supresion antes de
// encolar, autorizacion de reputation en el carril transaccional y un solo mensaje con todos los
// destinatarios del sobre. Nunca es de marketing ni de prueba.
func (uc *UseCase) CreateRawMessage(ctx context.Context, cmd RawMessageCommand) (*CreateResult, error) {
	if err := validateRaw(&cmd); err != nil {
		return nil, err
	}
	if cmd.IdempotencyKey != "" {
		if replay, err := uc.replaySubmission(ctx, cmd.TenantID, cmd.IdempotencyKey); err != nil || replay != nil {
			return replay, err
		}
	}
	checked := map[string]bool{}
	for _, addr := range []string{cmd.EnvelopeFrom, cmd.From.Email, cmd.Sender} {
		d := domain.DomainOf(addr)
		if addr == "" || checked[d] {
			continue
		}
		checked[d] = true
		if err := uc.requireSendingDomain(ctx, cmd.TenantID, addr); err != nil {
			return nil, err
		}
	}

	to := make([]domain.Recipient, len(cmd.Recipients))
	for i, r := range cmd.Recipients {
		to[i] = domain.Recipient{Email: r}
	}
	var cc, bcc []domain.Recipient
	suppressed, err := uc.filterSuppressed(ctx, cmd.TenantID, "", &to, &cc, &bcc)
	if err != nil {
		return nil, err
	}
	if len(to) > 0 {
		if err := uc.authorize(ctx, cmd.TenantID, domain.ClassTransactional, len(to), true); err != nil {
			return nil, err
		}
	}

	now := uc.now()
	msg := &domain.Message{
		ID:        uuid.New(),
		TenantID:  cmd.TenantID,
		FromEmail: cmd.From.Email,
		FromName:  cmd.From.Name,
		To:        to,
		Subject:   cmd.Subject,
		HTML:      optional(cmd.HTML),
		// La fila exige un cuerpo; el texto vacio lo es para un mensaje que solo trae adjuntos.
		Text:      &cmd.Text,
		Class:     domain.ClassTransactional,
		Status:    domain.StatusQueued,
		Origin:    domain.OriginSMTP,
		APIKeyID:  cmd.APIKeyID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if len(cmd.ReplyTo) > 0 {
		msg.ReplyTo = optional(cmd.ReplyTo[0])
	}
	if cmd.IdempotencyKey != "" {
		key := cmd.IdempotencyKey
		msg.IdempotencyKey = &key
	}
	if len(to) == 0 {
		msg.To = make([]domain.Recipient, len(cmd.Recipients))
		for i, r := range cmd.Recipients {
			msg.To[i] = domain.Recipient{Email: r}
		}
		msg.Status = domain.StatusSuppressed
	}
	result := &CreateResult{Suppressed: suppressed}
	res, err := uc.storeSubmission(ctx, cmd.TenantID, cmd.IdempotencyKey, []*domain.Message{msg}, result,
		func(ctx context.Context, m *domain.Message) error {
			if m.Status != domain.StatusQueued {
				return nil
			}
			return uc.repo.InsertRawContent(ctx, m.TenantID, m.ID, cmd.Raw)
		})
	if err == nil && res != nil && !res.Replayed && msg.Status == domain.StatusQueued {
		uc.metrics.RelayMessage(uc.relayAccount(cmd.TenantID), domain.RelayClassOf(cmd.Bulk))
	}
	return res, err
}

// relayAccount distingue lo que entra por la cuenta de la empresa de plataforma (la clave global del
// otro producto, que comparte reputacion con el correo de esta plataforma) de lo que entra por la
// cuenta propia de una empresa. Sin PlatformTenantID todo cuenta como empresa.
func (uc *UseCase) relayAccount(tenantID uuid.UUID) string {
	if uc.cfg.PlatformTenantID != uuid.Nil && tenantID == uc.cfg.PlatformTenantID {
		return domain.RelayAccountPlatform
	}
	return domain.RelayAccountTenant
}

func validateRaw(cmd *RawMessageCommand) error {
	if len(cmd.Raw) == 0 || len(cmd.Raw) > domain.MaxRawBytes {
		return domain.NewValidationError("el mensaje debe tener entre 1 y %d bytes", domain.MaxRawBytes)
	}
	cmd.EnvelopeFrom = domain.NormalizeEmail(cmd.EnvelopeFrom)
	if !domain.ValidEmail(cmd.EnvelopeFrom) {
		return domain.NewValidationError("envelope_from must be a valid email")
	}
	cmd.From.Email = domain.NormalizeEmail(cmd.From.Email)
	cmd.From.Name = strings.TrimSpace(cmd.From.Name)
	if !domain.ValidEmail(cmd.From.Email) {
		return domain.NewValidationError("the From header must hold a valid email")
	}
	if cmd.Sender != "" {
		cmd.Sender = domain.NormalizeEmail(cmd.Sender)
		if !domain.ValidEmail(cmd.Sender) {
			return domain.NewValidationError("the Sender header must hold a valid email")
		}
	}
	for i := range cmd.ReplyTo {
		cmd.ReplyTo[i] = domain.NormalizeEmail(cmd.ReplyTo[i])
		if !domain.ValidEmail(cmd.ReplyTo[i]) {
			return domain.NewValidationError("the Reply-To header must hold valid emails")
		}
	}
	if len(cmd.Recipients) == 0 || len(cmd.Recipients) > domain.MaxRecipients {
		return domain.NewValidationError("recipients must have between 1 and %d addresses", domain.MaxRecipients)
	}
	seen := make(map[string]bool, len(cmd.Recipients))
	for i, r := range cmd.Recipients {
		r = domain.NormalizeEmail(r)
		if !domain.ValidEmail(r) {
			return domain.NewValidationError("recipients[%d] must be a valid email", i)
		}
		if seen[strings.ToLower(r)] {
			return domain.NewValidationError("recipient %s appears more than once", r)
		}
		seen[strings.ToLower(r)] = true
		cmd.Recipients[i] = r
	}
	if len(cmd.IdempotencyKey) > 200 {
		return domain.NewValidationError("idempotency_key must be at most 200 characters")
	}
	return nil
}
