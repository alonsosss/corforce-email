package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
)

// renderConcurrency acota los renders simultaneos de un lote contra templates: un lote de
// 500 en serie tardaria varios segundos, y sin tope se abririan 500 conexiones a la vez.
const renderConcurrency = 8

// MarketingRecipient es un destinatario de un lote de campana.
type MarketingRecipient struct {
	Email     string
	Name      string
	ContactID uuid.UUID
	Variables map[string]any
}

// MarketingBatchCommand es el contrato de POST /internal/transactional/batch (lo usa
// campaigns).
type MarketingBatchCommand struct {
	TenantID        uuid.UUID
	Class           string
	CampaignID      uuid.UUID
	IdempotencyKey  string
	From            domain.Recipient
	ReplyTo         string
	TemplateID      uuid.UUID
	TemplateVersion int
	// Subject sustituye al asunto que renderiza la plantilla (variante A/B o reenvio de
	// campaigns); vacio = el de la plantilla. Es texto literal, sin variables.
	Subject    string
	Recipients []MarketingRecipient
	Tags       map[string]string
	// UTM es la configuracion de UTM de la campana; nil aplica los valores por defecto.
	UTM *UTMInput

	utm domain.UTMSettings
}

// UTMInput es el objeto utm del lote. Enabled nil equivale a true. Los valores vacios se
// derivan: source del nombre del remitente (o de su dominio), campaign del identificador
// de la campana. Todos se normalizan con domain.UTMValue.
type UTMInput struct {
	Enabled  *bool
	Source   string
	Campaign string
	Content  string
}

// MaxSubjectOverride acota el asunto alternativo del lote: termina en la cabecera Subject.
const MaxSubjectOverride = 250

// BatchResult es la respuesta del lote. Replayed indica que la clave ya existia y se
// devuelve exactamente lo que se respondio entonces, sin crear nada.
type BatchResult struct {
	Accepted   int                `json:"accepted"`
	Suppressed []ports.Suppressed `json:"suppressed"`
	MessageIDs []uuid.UUID        `json:"message_ids"`
	Replayed   bool               `json:"-"`
}

// CreateMarketingBatch crea y encola un lote de campana. Orden: validar, repetir si la
// clave ya existe, remitente verificado, supresion en bloque, autorizacion de reputation
// por los no suprimidos (falla cerrado), render por destinatario y, en UNA transaccion, la
// peticion, un mensaje por destinatario y su evento de la cola de marketing. Si cualquier
// paso falla no queda nada creado.
func (uc *UseCase) CreateMarketingBatch(ctx context.Context, cmd MarketingBatchCommand) (*BatchResult, error) {
	if err := validateBatch(&cmd); err != nil {
		return nil, err
	}
	if replay, err := uc.replayBatch(ctx, cmd.TenantID, cmd.IdempotencyKey); err != nil || replay != nil {
		return replay, err
	}
	if err := uc.requireSendingDomain(ctx, cmd.TenantID, cmd.From.Email); err != nil {
		return nil, err
	}

	emails := make([]string, len(cmd.Recipients))
	for i, r := range cmd.Recipients {
		emails[i] = r.Email
	}
	suppressed, blocked, err := uc.checkSuppressed(ctx, cmd.TenantID, emails)
	if err != nil {
		return nil, err
	}
	kept := make([]MarketingRecipient, 0, len(cmd.Recipients))
	for _, r := range cmd.Recipients {
		if !blocked[strings.ToLower(r.Email)] {
			kept = append(kept, r)
		}
	}

	if err := uc.authorize(ctx, cmd.TenantID, domain.ClassMarketing, len(kept), false); err != nil {
		return nil, err
	}
	messages, err := uc.buildMarketingMessages(ctx, cmd, kept)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(messages))
	for i, m := range messages {
		ids[i] = m.ID
	}

	replayed := false
	err = uc.repo.Transact(ctx, func(ctx context.Context) error {
		sub := &domain.Submission{
			ID: uuid.New(), TenantID: cmd.TenantID, IdempotencyKey: cmd.IdempotencyKey,
			Class: domain.ClassMarketing, MessageIDs: ids, Suppressed: suppressed,
		}
		inserted, err := uc.repo.InsertSubmission(ctx, sub)
		if err != nil {
			return err
		}
		if !inserted {
			// Otro lote con la misma clave gano la carrera: se devuelve el suyo.
			replayed = true
			return errSubmissionRace
		}
		for _, m := range messages {
			m.SubmissionID = &sub.ID
			if err := uc.repo.InsertMessage(ctx, m); err != nil {
				return err
			}
			if err := uc.publishQueued(ctx, m); err != nil {
				return err
			}
		}
		return nil
	})
	if replayed {
		replay, err := uc.replayBatch(ctx, cmd.TenantID, cmd.IdempotencyKey)
		if err == nil && replay == nil {
			err = fmt.Errorf("la clave de idempotencia %q existe pero no se pudo leer", cmd.IdempotencyKey)
		}
		return replay, err
	}
	if err != nil {
		return nil, err
	}
	return &BatchResult{Accepted: len(messages), Suppressed: suppressed, MessageIDs: ids}, nil
}

func validateBatch(cmd *MarketingBatchCommand) error {
	if cmd.Class != domain.ClassMarketing {
		return domain.NewValidationError("class must be %q on this route", domain.ClassMarketing)
	}
	if cmd.CampaignID == uuid.Nil {
		return domain.NewValidationError("campaign_id is required")
	}
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if cmd.IdempotencyKey == "" || len(cmd.IdempotencyKey) > 200 {
		return domain.NewValidationError("idempotency_key is required and must be at most 200 characters")
	}
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
		return domain.NewValidationError("template_version must be a published version (>= 1)")
	}
	cmd.Subject = strings.TrimSpace(cmd.Subject)
	if utf8.RuneCountInString(cmd.Subject) > MaxSubjectOverride {
		return domain.NewValidationError("subject must be at most %d characters", MaxSubjectOverride)
	}
	if strings.IndexFunc(cmd.Subject, unicode.IsControl) >= 0 {
		return domain.NewValidationError("subject must not contain control characters")
	}
	if len(cmd.Recipients) == 0 || len(cmd.Recipients) > domain.MaxBatchRecipients {
		return domain.NewValidationError("recipients must have between 1 and %d entries", domain.MaxBatchRecipients)
	}
	seen := make(map[string]bool, len(cmd.Recipients))
	for i := range cmd.Recipients {
		r := &cmd.Recipients[i]
		r.Email = domain.NormalizeEmail(r.Email)
		if !domain.ValidEmail(r.Email) {
			return domain.NewValidationError("recipients[%d].email must be a valid email", i)
		}
		r.Name = strings.TrimSpace(r.Name)
		if r.ContactID == uuid.Nil {
			return domain.NewValidationError("recipients[%d].contact_id is required", i)
		}
		key := strings.ToLower(r.Email)
		if seen[key] {
			return domain.NewValidationError("recipient %s appears more than once", r.Email)
		}
		seen[key] = true
		if r.Variables == nil {
			r.Variables = map[string]any{}
		}
	}
	utm, err := resolveUTM(cmd.UTM, cmd.From, cmd.CampaignID)
	if err != nil {
		return err
	}
	cmd.utm = utm
	return domain.ValidateTags(cmd.Tags)
}

// resolveUTM valida el objeto utm y completa lo que falte. Un valor dado que no conserva
// ningun caracter valido se rechaza: callarlo mandaria la campana con otro nombre.
func resolveUTM(in *UTMInput, from domain.Recipient, campaignID uuid.UUID) (domain.UTMSettings, error) {
	if in == nil {
		in = &UTMInput{}
	}
	if in.Enabled != nil && !*in.Enabled {
		return domain.UTMSettings{}, nil
	}
	out := domain.UTMSettings{Enabled: true}
	for _, f := range []struct {
		name, raw string
		out       *string
	}{
		{"source", in.Source, &out.Source},
		{"campaign", in.Campaign, &out.Campaign},
		{"content", in.Content, &out.Content},
	} {
		if len(f.raw) > domain.MaxUTMInputLen {
			return domain.UTMSettings{}, domain.NewValidationError("utm.%s must be at most %d characters", f.name, domain.MaxUTMInputLen)
		}
		*f.out = domain.UTMValue(f.raw)
		if strings.TrimSpace(f.raw) != "" && *f.out == "" {
			return domain.UTMSettings{}, domain.NewValidationError("utm.%s must contain letters or digits", f.name)
		}
	}
	if out.Source == "" {
		out.Source = domain.UTMValue(from.Name)
	}
	if out.Source == "" {
		_, host, _ := strings.Cut(from.Email, "@")
		out.Source = domain.UTMValue(host)
	}
	if out.Campaign == "" {
		out.Campaign = campaignID.String()
	}
	return out, nil
}

// replayBatch devuelve la respuesta guardada de un lote con la misma clave. Una clave que
// ya identifica una peticion transaccional no es un lote: se rechaza.
func (uc *UseCase) replayBatch(ctx context.Context, tenantID uuid.UUID, key string) (*BatchResult, error) {
	sub, err := uc.repo.GetSubmission(ctx, tenantID, key)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if domain.ClassOrDefault(sub.Class) != domain.ClassMarketing {
		return nil, domain.ErrIdempotencyKeyReused
	}
	res := &BatchResult{Accepted: len(sub.MessageIDs), Suppressed: sub.Suppressed, MessageIDs: sub.MessageIDs, Replayed: true}
	if res.Suppressed == nil {
		res.Suppressed = []ports.Suppressed{}
	}
	if res.MessageIDs == nil {
		res.MessageIDs = []uuid.UUID{}
	}
	return res, nil
}

// buildMarketingMessages materializa un mensaje por destinatario y lo renderiza con sus
// variables y su enlace de baja. Los renders corren en paralelo con tope; el primer error
// cancela los demas y el lote entero se descarta.
func (uc *UseCase) buildMarketingMessages(ctx context.Context, cmd MarketingBatchCommand, recipients []MarketingRecipient) ([]*domain.Message, error) {
	messages := make([]*domain.Message, len(recipients))
	for i, r := range recipients {
		messages[i] = uc.newMarketingMessage(cmd, r)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	sem := make(chan struct{}, renderConcurrency)
launch:
	for _, msg := range messages {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launch
		}
		wg.Add(1)
		go func(msg *domain.Message) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := uc.renderMarketing(ctx, cmd.TenantID, msg, cmd.utm); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}(msg)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cmd.Subject != "" {
		for _, m := range messages {
			m.Subject = cmd.Subject
		}
	}
	return messages, nil
}

func (uc *UseCase) newMarketingMessage(cmd MarketingBatchCommand, r MarketingRecipient) *domain.Message {
	templateID, version, campaignID, contactID := cmd.TemplateID, cmd.TemplateVersion, cmd.CampaignID, r.ContactID
	msg := uc.newMessage(CreateMessagesCommand{
		TenantID:        cmd.TenantID,
		IdempotencyKey:  cmd.IdempotencyKey,
		From:            cmd.From,
		ReplyTo:         cmd.ReplyTo,
		TemplateID:      &templateID,
		TemplateVersion: &version,
		Variables:       r.Variables,
		Tags:            cmd.Tags,
		Unsubscribable:  true,
	}, []domain.Recipient{{Email: r.Email, Name: r.Name}})
	msg.Class = domain.ClassMarketing
	msg.CampaignID = &campaignID
	msg.ContactID = &contactID
	msg.Test = domain.IsTestSend(cmd.Tags)
	return msg
}

// renderMarketing renderiza el mensaje en templates y exige dos reglas distintas: que la
// plantilla sea de tipo marketing segun templates, y que su contenido visible lleve el
// enlace de baja de este mensaje (RFC 8058). La primera impide que una plantilla
// transaccional que use unsubscribe_url salga como campana; la segunda, que salga una de
// marketing sin enlace de baja. Los UTM se anaden despues del render y antes de medir el
// tamano, sin tocar los enlaces de la plataforma (baja, ver en el navegador).
func (uc *UseCase) renderMarketing(ctx context.Context, tenantID uuid.UUID, msg *domain.Message, utm domain.UTMSettings) error {
	rcpt := msg.To[0].Email
	rendered, err := uc.templates.Render(ctx, tenantID, ports.RenderRequest{
		TemplateID: *msg.TemplateID,
		Version:    msg.TemplateVersion,
		Variables:  msg.Variables,
		Reserved: ports.ReservedVariables{
			UnsubscribeURL: uc.links.UnsubscribeURL(domain.UnsubscribeClaims{TenantID: tenantID, MessageID: msg.ID, Email: rcpt}),
			RecipientEmail: rcpt,
		},
	})
	if err != nil {
		return err
	}
	switch rendered.Kind {
	case domain.TemplateKindMarketing:
	case "":
		// Templates desplegado sin el campo kind: sin el tipo no se puede probar que la
		// plantilla sea de marketing y el lote falla cerrado.
		return fmt.Errorf("%w: el render no informa el tipo de plantilla", domain.ErrTemplatesUnavailable)
	default:
		return domain.ErrTemplateNotMarketing
	}
	if uc.utm != nil {
		rendered.HTML = uc.utm.Tag(rendered.HTML, utm)
	}
	if len(rendered.HTML)+len(rendered.Text) > domain.MaxBodyBytes {
		return domain.NewValidationError("la plantilla renderizada supera el limite de %d bytes", domain.MaxBodyBytes)
	}
	visible := rendered.HTML
	if strings.TrimSpace(visible) == "" {
		visible = rendered.Text
	}
	if !uc.links.ContainsUnsubscribeLink(visible, msg.ID) {
		return domain.ErrTemplateMissingUnsubscribe
	}
	msg.Subject = rendered.Subject
	msg.HTML = optional(rendered.HTML)
	msg.Text = optional(rendered.Text)
	version := rendered.Version
	msg.TemplateVersion = &version
	return nil
}
