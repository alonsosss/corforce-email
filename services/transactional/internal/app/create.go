package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// CreateMessagesCommand es una peticion de envio ya decodificada.
type CreateMessagesCommand struct {
	TenantID        uuid.UUID
	CreatedBy       *uuid.UUID
	IdempotencyKey  string
	From            domain.Recipient
	ReplyTo         string
	To              []domain.Recipient
	Cc              []domain.Recipient
	Bcc             []domain.Recipient
	Subject         string
	HTML            string
	Text            string
	TemplateID      *uuid.UUID
	TemplateVersion *int
	Variables       map[string]any
	Headers         map[string]string
	Tags            map[string]string
	ScheduledAt     *time.Time
	Unsubscribable  bool
	HasAttachments  bool
	// Purpose solo llega por el envio interno (domain.PurposeDoubleOptIn); el API publico
	// no lo acepta.
	Purpose string
}

// MessageSummary es lo que se devuelve por cada mensaje creado.
type MessageSummary struct {
	ID     uuid.UUID          `json:"id"`
	Status string             `json:"status"`
	To     []domain.Recipient `json:"to"`
}

// CreateResult es la respuesta de CreateMessages. Replayed indica que la clave de
// idempotencia ya existia y se devuelve lo creado entonces, sin reenviar.
type CreateResult struct {
	Messages   []MessageSummary   `json:"messages"`
	Suppressed []ports.Suppressed `json:"suppressed"`
	Replayed   bool               `json:"-"`
}

// CreateMessages valida, filtra por supresion, pide autorizacion a reputation, renderiza y
// encola. Un mensaje por destinatario cuando hay plantilla o enlace de baja; uno solo con
// todos los destinatarios cuando el cuerpo llega crudo.
func (uc *UseCase) CreateMessages(ctx context.Context, cmd CreateMessagesCommand) (*CreateResult, error) {
	if err := uc.validateCreate(&cmd); err != nil {
		return nil, err
	}
	if cmd.IdempotencyKey != "" {
		if replay, err := uc.replaySubmission(ctx, cmd.TenantID, cmd.IdempotencyKey); err != nil || replay != nil {
			return replay, err
		}
	}
	if err := uc.requireSendingDomain(ctx, cmd.TenantID, cmd.From.Email); err != nil {
		return nil, err
	}

	suppressed, err := uc.filterSuppressed(ctx, cmd.TenantID, cmd.Purpose, &cmd.To, &cmd.Cc, &cmd.Bcc)
	if err != nil {
		return nil, err
	}
	// En el carril transaccional una caida de reputation no bloquea (failOpen), pero una
	// denegacion explicita si.
	if err := uc.authorize(ctx, cmd.TenantID, domain.ClassTransactional, recipientCount(cmd), true); err != nil {
		return nil, err
	}
	result := &CreateResult{Suppressed: suppressed}

	var messages []*domain.Message
	if len(cmd.To) == 0 {
		// Todos los destinatarios estaban suprimidos: queda constancia del intento, sin
		// encolar nada.
		msg := uc.newMessage(cmd, cmd.To)
		msg.Status = domain.StatusSuppressed
		messages = []*domain.Message{msg}
	} else if messages, err = uc.buildMessages(ctx, cmd); err != nil {
		return nil, err
	}

	replayed := false
	err = uc.repo.Transact(ctx, func(ctx context.Context) error {
		if cmd.IdempotencyKey != "" {
			ids := make([]uuid.UUID, len(messages))
			for i, m := range messages {
				ids[i] = m.ID
			}
			sub := &domain.Submission{
				ID: uuid.New(), TenantID: cmd.TenantID, IdempotencyKey: cmd.IdempotencyKey,
				Class: domain.ClassTransactional, MessageIDs: ids, Suppressed: suppressed,
			}
			inserted, err := uc.repo.InsertSubmission(ctx, sub)
			if err != nil {
				return err
			}
			if !inserted {
				// Otra peticion con la misma clave gano la carrera: se devuelve la suya.
				replayed = true
				return errSubmissionRace
			}
			for _, m := range messages {
				m.SubmissionID = &sub.ID
			}
		}
		for _, m := range messages {
			if err := uc.repo.InsertMessage(ctx, m); err != nil {
				return err
			}
			if m.Status == domain.StatusQueued {
				if err := uc.publishQueued(ctx, m); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if replayed {
		replay, err := uc.replaySubmission(ctx, cmd.TenantID, cmd.IdempotencyKey)
		if err == nil && replay == nil {
			err = fmt.Errorf("la clave de idempotencia %q existe pero no se pudo leer", cmd.IdempotencyKey)
		}
		return replay, err
	}
	if err != nil {
		return nil, err
	}
	for _, m := range messages {
		result.Messages = append(result.Messages, MessageSummary{ID: m.ID, Status: m.Status, To: m.To})
	}
	return result, nil
}

var errSubmissionRace = errors.New("submission already exists")

// InternalSendCommand es el contrato de POST /internal/send-email (lo usa identity).
type InternalSendCommand struct {
	TenantID uuid.UUID
	To       string
	Subject  string
	HTMLBody string
	TextBody string
}

// InternalSendResult es la respuesta del envio interno.
type InternalSendResult struct {
	MessageID uuid.UUID `json:"message_id"`
	Status    string    `json:"status"`
}

// InternalSend crea y encola un correo de la propia plataforma con el remitente de
// PLATFORM_FROM_EMAIL. La supresion se respeta igual que en el API. No pasa por
// reputation: son correos de la plataforma (codigos de acceso, restablecimientos), no
// practica de envio de la empresa, y no pueden quedar bloqueados por su reputacion.
func (uc *UseCase) InternalSend(ctx context.Context, cmd InternalSendCommand) (*InternalSendResult, error) {
	if uc.cfg.PlatformFromEmail == "" {
		return nil, domain.NewValidationError("PLATFORM_FROM_EMAIL no esta configurado")
	}
	cmd.To = domain.NormalizeEmail(cmd.To)
	if !domain.ValidEmail(cmd.To) {
		return nil, domain.NewValidationError("to must be a valid email")
	}
	if strings.TrimSpace(cmd.Subject) == "" {
		return nil, domain.NewValidationError("subject is required")
	}
	if strings.TrimSpace(cmd.HTMLBody) == "" && strings.TrimSpace(cmd.TextBody) == "" {
		return nil, domain.NewValidationError("html_body or text_body is required")
	}
	if len(cmd.HTMLBody)+len(cmd.TextBody) > domain.MaxBodyBytes {
		return nil, domain.NewValidationError("el cuerpo supera el limite de %d bytes", domain.MaxBodyBytes)
	}

	from := domain.NormalizeEmail(uc.cfg.PlatformFromEmail)
	if err := uc.requirePlatformDomain(ctx, cmd.TenantID, from); err != nil {
		return nil, err
	}
	to := []domain.Recipient{{Email: cmd.To}}
	var cc, bcc []domain.Recipient
	if _, err := uc.filterSuppressed(ctx, cmd.TenantID, "", &to, &cc, &bcc); err != nil {
		return nil, err
	}

	create := CreateMessagesCommand{
		TenantID: cmd.TenantID,
		From:     domain.Recipient{Email: from, Name: uc.cfg.PlatformFromName},
		To:       to,
		Subject:  cmd.Subject,
		HTML:     cmd.HTMLBody,
		Text:     cmd.TextBody,
	}
	msg := uc.newMessage(create, to)
	if len(to) == 0 {
		msg.To = []domain.Recipient{{Email: cmd.To}}
		msg.Status = domain.StatusSuppressed
	}
	err := uc.repo.Transact(ctx, func(ctx context.Context) error {
		if err := uc.repo.InsertMessage(ctx, msg); err != nil {
			return err
		}
		if msg.Status == domain.StatusQueued {
			return uc.publishQueued(ctx, msg)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &InternalSendResult{MessageID: msg.ID, Status: msg.Status}, nil
}

func (uc *UseCase) validateCreate(cmd *CreateMessagesCommand) error {
	if cmd.HasAttachments {
		return domain.ErrAttachmentsNotSupported
	}
	cmd.From.Email = domain.NormalizeEmail(cmd.From.Email)
	if !domain.ValidEmail(cmd.From.Email) {
		return domain.NewValidationError("from.email must be a valid email")
	}
	cmd.ReplyTo = domain.NormalizeEmail(cmd.ReplyTo)
	if cmd.ReplyTo != "" && !domain.ValidEmail(cmd.ReplyTo) {
		return domain.NewValidationError("reply_to must be a valid email")
	}
	if len(cmd.To) == 0 || len(cmd.To) > domain.MaxRecipients {
		return domain.NewValidationError("to must have between 1 and %d recipients", domain.MaxRecipients)
	}
	if len(cmd.To)+len(cmd.Cc)+len(cmd.Bcc) > domain.MaxRecipients {
		return domain.NewValidationError("to, cc and bcc must not exceed %d recipients in total", domain.MaxRecipients)
	}
	for field, list := range map[string][]domain.Recipient{"to": cmd.To, "cc": cmd.Cc, "bcc": cmd.Bcc} {
		for i := range list {
			list[i].Email = domain.NormalizeEmail(list[i].Email)
			if !domain.ValidEmail(list[i].Email) {
				return domain.NewValidationError("%s[%d].email must be a valid email", field, i)
			}
			list[i].Name = strings.TrimSpace(list[i].Name)
		}
	}
	if dup := duplicateRecipient(cmd.To, cmd.Cc, cmd.Bcc); dup != "" {
		return domain.NewValidationError("recipient %s appears more than once", dup)
	}
	cmd.Purpose = strings.TrimSpace(cmd.Purpose)
	if !domain.ValidPurpose(cmd.Purpose) {
		return domain.NewValidationError("purpose must be %q or empty", domain.PurposeDoubleOptIn)
	}
	// El proposito relaja la supresion para UNA persona: un mensaje con varios
	// destinatarios o copias escribiria a quien se dio de baja sin que lo hubiera pedido.
	if cmd.Purpose != "" && (len(cmd.To) != 1 || len(cmd.Cc) > 0 || len(cmd.Bcc) > 0) {
		return domain.NewValidationError("purpose %q requires exactly one recipient in to and no cc or bcc", cmd.Purpose)
	}

	hasBody := strings.TrimSpace(cmd.HTML) != "" || strings.TrimSpace(cmd.Text) != ""
	hasSubject := strings.TrimSpace(cmd.Subject) != ""
	switch {
	case cmd.TemplateID != nil && (hasBody || hasSubject):
		return domain.NewValidationError("template_id cannot be combined with subject, html or text")
	case cmd.TemplateID == nil && !(hasBody && hasSubject):
		return domain.NewValidationError("either template_id or subject with html or text is required")
	}
	if cmd.TemplateID == nil && cmd.TemplateVersion != nil {
		return domain.NewValidationError("template_version requires template_id")
	}
	if len(cmd.HTML)+len(cmd.Text) > domain.MaxBodyBytes {
		return domain.NewValidationError("el cuerpo supera el limite de %d bytes", domain.MaxBodyBytes)
	}
	if fansOut(*cmd) && (len(cmd.Cc) > 0 || len(cmd.Bcc) > 0) {
		// Con plantilla o enlace de baja cada destinatario recibe su propio mensaje; un
		// cc o bcc recibiria una copia por cada uno.
		return domain.NewValidationError("cc and bcc are not allowed with template_id or unsubscribable")
	}
	if err := domain.ValidateHeaders(cmd.Headers); err != nil {
		return err
	}
	if err := domain.ValidateTags(cmd.Tags); err != nil {
		return err
	}
	if len(cmd.IdempotencyKey) > 200 {
		return domain.NewValidationError("idempotency_key must be at most 200 characters")
	}
	if cmd.ScheduledAt != nil && cmd.ScheduledAt.After(uc.now().Add(30*24*time.Hour)) {
		return domain.NewValidationError("scheduled_at cannot be more than 30 days ahead")
	}
	return nil
}

func duplicateRecipient(lists ...[]domain.Recipient) string {
	seen := make(map[string]bool)
	for _, list := range lists {
		for _, r := range list {
			key := strings.ToLower(r.Email)
			if seen[key] {
				return r.Email
			}
			seen[key] = true
		}
	}
	return ""
}

// replaySubmission devuelve lo creado por una peticion con la misma clave. Una clave que
// ya identifica un lote de marketing no es una peticion transaccional: se rechaza.
func (uc *UseCase) replaySubmission(ctx context.Context, tenantID uuid.UUID, key string) (*CreateResult, error) {
	sub, err := uc.repo.GetSubmission(ctx, tenantID, key)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if domain.ClassOrDefault(sub.Class) != domain.ClassTransactional {
		return nil, domain.ErrIdempotencyKeyReused
	}
	msgs, err := uc.repo.GetMessages(ctx, tenantID, sub.MessageIDs)
	if err != nil {
		return nil, err
	}
	result := &CreateResult{Replayed: true, Suppressed: sub.Suppressed}
	if result.Suppressed == nil {
		result.Suppressed = []ports.Suppressed{}
	}
	for _, m := range msgs {
		result.Messages = append(result.Messages, MessageSummary{ID: m.ID, Status: m.Status, To: m.To})
	}
	return result, nil
}

func (uc *UseCase) requireSendingDomain(ctx context.Context, tenantID uuid.UUID, from string) error {
	d, err := uc.repo.GetSendingDomain(ctx, tenantID, domain.DomainOf(from))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrSendingDomainNotVerified
	}
	if err != nil {
		return err
	}
	if !d.CanSend() {
		return domain.ErrSendingDomainNotVerified
	}
	return nil
}

// requirePlatformDomain es requireSendingDomain con la excepcion de arranque: si el
// dominio de plataforma no figura en la proyeccion y PLATFORM_FROM_ALLOW_UNVERIFIED esta
// activo, se permite y se avisa.
func (uc *UseCase) requirePlatformDomain(ctx context.Context, tenantID uuid.UUID, from string) error {
	err := uc.requireSendingDomain(ctx, tenantID, from)
	if errors.Is(err, domain.ErrSendingDomainNotVerified) && uc.cfg.AllowUnverifiedPlatformFrom {
		uc.logger.Warn("transactional: remitente de plataforma sin dominio verificado; se permite por PLATFORM_FROM_ALLOW_UNVERIFIED",
			zap.String("from", from), zap.String("tenant_id", tenantID.String()))
		return nil
	}
	return err
}

// checkSuppressed consulta suppression con todas las direcciones y devuelve las
// suprimidas y el conjunto (en minusculas) que hay que retirar. Sin respuesta de
// suppression no se encola nada.
func (uc *UseCase) checkSuppressed(ctx context.Context, tenantID uuid.UUID, emails []string) ([]ports.Suppressed, map[string]bool, error) {
	suppressed, err := uc.suppression.Check(ctx, tenantID, emails)
	if err != nil {
		uc.logger.Warn("transactional: suppression no respondio; no se encola", zap.Error(err))
		return nil, nil, domain.ErrSuppressionUnavailable
	}
	if suppressed == nil {
		suppressed = []ports.Suppressed{}
	}
	blocked := make(map[string]bool, len(suppressed))
	for _, s := range suppressed {
		blocked[strings.ToLower(s.Email)] = true
	}
	return suppressed, blocked, nil
}

// filterSuppressed consulta suppression con todos los destinatarios y quita los
// suprimidos de cada lista. Con proposito, las causas que ese proposito no respeta
// (domain.IgnoresSuppression) ni bloquean ni figuran como suprimidas.
func (uc *UseCase) filterSuppressed(ctx context.Context, tenantID uuid.UUID, purpose string, lists ...*[]domain.Recipient) ([]ports.Suppressed, error) {
	var emails []string
	for _, l := range lists {
		for _, r := range *l {
			emails = append(emails, r.Email)
		}
	}
	suppressed, blocked, err := uc.checkSuppressed(ctx, tenantID, emails)
	if err != nil {
		return nil, err
	}
	if purpose != "" {
		kept := make([]ports.Suppressed, 0, len(suppressed))
		blocked = make(map[string]bool, len(suppressed))
		for _, s := range suppressed {
			if domain.IgnoresSuppression(purpose, s.Reason) {
				continue
			}
			kept = append(kept, s)
			blocked[strings.ToLower(s.Email)] = true
		}
		suppressed = kept
	}
	for _, l := range lists {
		kept := (*l)[:0]
		for _, r := range *l {
			if !blocked[strings.ToLower(r.Email)] {
				kept = append(kept, r)
			}
		}
		*l = kept
	}
	return suppressed, nil
}

// fansOut dice si la peticion produce un mensaje por destinatario: con plantilla o con
// enlace de baja, las variables reservadas y la baja son por persona.
func fansOut(cmd CreateMessagesCommand) bool {
	return cmd.TemplateID != nil || cmd.Unsubscribable
}

// recipientCount es el numero de destinatarios que saldran de la peticion ya filtrada: la
// unidad que autoriza reputation, que cuenta los envios por destinatario (el campo to de
// transactional.email.sent lleva to, cc y bcc). Sin to no sale nada.
func recipientCount(cmd CreateMessagesCommand) int {
	if len(cmd.To) == 0 {
		return 0
	}
	return len(cmd.To) + len(cmd.Cc) + len(cmd.Bcc)
}

// buildMessages materializa los mensajes: uno por destinatario con plantilla o baja
// (renderizando por destinatario, porque las variables reservadas cambian), uno con
// todos los destinatarios si el cuerpo llega crudo.
func (uc *UseCase) buildMessages(ctx context.Context, cmd CreateMessagesCommand) ([]*domain.Message, error) {
	if !fansOut(cmd) {
		return []*domain.Message{uc.newMessage(cmd, cmd.To)}, nil
	}
	messages := make([]*domain.Message, 0, len(cmd.To))
	for _, rcpt := range cmd.To {
		msg := uc.newMessage(cmd, []domain.Recipient{rcpt})
		if cmd.TemplateID != nil {
			rendered, err := uc.templates.Render(ctx, cmd.TenantID, ports.RenderRequest{
				TemplateID: *cmd.TemplateID,
				Version:    cmd.TemplateVersion,
				Variables:  cmd.Variables,
				Reserved: ports.ReservedVariables{
					UnsubscribeURL: uc.links.UnsubscribeURL(domain.UnsubscribeClaims{TenantID: cmd.TenantID, MessageID: msg.ID, Email: rcpt.Email}),
					RecipientEmail: rcpt.Email,
				},
			})
			if err != nil {
				return nil, err
			}
			// Solo se rechaza lo que templates declara de marketing. Un kind vacio (templates
			// desplegado sin el campo) no para el carril transaccional.
			if rendered.Kind == domain.TemplateKindMarketing {
				return nil, domain.ErrTemplateNotTransactional
			}
			if len(rendered.HTML)+len(rendered.Text) > domain.MaxBodyBytes {
				return nil, domain.NewValidationError("la plantilla renderizada supera el limite de %d bytes", domain.MaxBodyBytes)
			}
			msg.Subject = rendered.Subject
			msg.HTML = optional(rendered.HTML)
			msg.Text = optional(rendered.Text)
			version := rendered.Version
			msg.TemplateVersion = &version
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func (uc *UseCase) newMessage(cmd CreateMessagesCommand, to []domain.Recipient) *domain.Message {
	now := uc.now()
	msg := &domain.Message{
		ID:              uuid.New(),
		TenantID:        cmd.TenantID,
		FromEmail:       cmd.From.Email,
		FromName:        strings.TrimSpace(cmd.From.Name),
		ReplyTo:         optional(cmd.ReplyTo),
		To:              copyRecipients(to),
		Cc:              copyRecipients(cmd.Cc),
		Bcc:             copyRecipients(cmd.Bcc),
		Subject:         cmd.Subject,
		TemplateID:      cmd.TemplateID,
		TemplateVersion: cmd.TemplateVersion,
		Variables:       cmd.Variables,
		HTML:            optional(cmd.HTML),
		Text:            optional(cmd.Text),
		Headers:         cmd.Headers,
		Tags:            cmd.Tags,
		Unsubscribable:  cmd.Unsubscribable,
		Class:           domain.ClassTransactional,
		Status:          domain.StatusQueued,
		ScheduledAt:     cmd.ScheduledAt,
		CreatedBy:       cmd.CreatedBy,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if cmd.IdempotencyKey != "" {
		key := cmd.IdempotencyKey
		msg.IdempotencyKey = &key
	}
	if cmd.ScheduledAt != nil && cmd.ScheduledAt.After(now) {
		msg.Status = domain.StatusAccepted
	}
	if msg.Variables == nil {
		msg.Variables = map[string]any{}
	}
	if msg.Headers == nil {
		msg.Headers = map[string]string{}
	}
	if msg.Tags == nil {
		msg.Tags = map[string]string{}
	}
	return msg
}

// publishQueued encola el mensaje en la cola de su clase: cada carril tiene su subject, su
// consumidor durable, su configuration set y su tasa, y nunca comparten reputacion.
func (uc *UseCase) publishQueued(ctx context.Context, m *domain.Message) error {
	var err error
	if domain.ClassOrDefault(m.Class) == domain.ClassMarketing {
		err = uc.events.Publish(ctx, "transactional.marketing.queued", m.TenantID, map[string]any{
			"tenant_id":  m.TenantID.String(),
			"message_id": m.ID.String(),
		})
	} else {
		err = uc.events.Publish(ctx, "transactional.message.queued", m.TenantID, map[string]any{
			"tenant_id":  m.TenantID.String(),
			"message_id": m.ID.String(),
		})
	}
	if err != nil {
		return fmt.Errorf("encolar mensaje %s: %w", m.ID, err)
	}
	return nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func copyRecipients(in []domain.Recipient) []domain.Recipient {
	out := make([]domain.Recipient, len(in))
	copy(out, in)
	return out
}
