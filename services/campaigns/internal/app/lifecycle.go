package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

// Schedule programa la campana y fija la version de la plantilla. version nil = la
// publicada ahora mismo, que se consulta a templates FUERA de la transaccion; dentro se
// comprueba que nadie cambio la plantilla entretanto.
func (uc *UseCase) Schedule(ctx context.Context, tenantID, id uuid.UUID, at time.Time, version *int) (*domain.Campaign, error) {
	c, err := uc.campaigns.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if !c.CanSchedule() {
		return nil, domain.TransitionError(c.Status, domain.StatusScheduled)
	}
	if err := domain.ValidateScheduleTime(at, uc.now()); err != nil {
		return nil, err
	}
	v, variants, err := uc.resolveVersions(ctx, c, version)
	if err != nil {
		return nil, err
	}
	return uc.transition(ctx, tenantID, id, func(ctx context.Context, cur *domain.Campaign) error {
		if !cur.SameContent(c) {
			return domain.ErrConcurrentChange
		}
		if err := cur.PinVariants(variants); err != nil {
			return err
		}
		if err := cur.Schedule(at, v, uc.now()); err != nil {
			return err
		}
		if err := uc.campaigns.Update(ctx, cur); err != nil {
			return err
		}
		return uc.publishScheduled(ctx, cur)
	})
}

// Start pone la campana en envio ya; el orquestador toma su primer lote en el siguiente
// tick.
func (uc *UseCase) Start(ctx context.Context, tenantID, id uuid.UUID, version *int) (*domain.Campaign, error) {
	c, err := uc.campaigns.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if !c.CanStart() {
		return nil, domain.TransitionError(c.Status, domain.StatusSending)
	}
	v, variants, err := uc.resolveVersions(ctx, c, version)
	if err != nil {
		return nil, err
	}
	return uc.transition(ctx, tenantID, id, func(ctx context.Context, cur *domain.Campaign) error {
		if !cur.SameContent(c) {
			return domain.ErrConcurrentChange
		}
		if err := cur.PinVariants(variants); err != nil {
			return err
		}
		if err := cur.Start(v, uc.now()); err != nil {
			return err
		}
		if err := uc.campaigns.Update(ctx, cur); err != nil {
			return err
		}
		return uc.publishStarted(ctx, cur)
	})
}

// ScheduleLocal programa el envio por zona horaria: cada contacto recibe la campana a la
// hora de pared local en su zona, o en fallback si no tiene una valida.
func (uc *UseCase) ScheduleLocal(ctx context.Context, tenantID, id uuid.UUID, local, fallback string, version *int) (*domain.Campaign, error) {
	at, err := domain.ParseLocalDateTime(local)
	if err != nil {
		return nil, err
	}
	c, err := uc.campaigns.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if !c.CanSchedule() {
		return nil, domain.TransitionError(c.Status, domain.StatusScheduled)
	}
	if c.ABTest != nil {
		return nil, domain.ErrABWithTimezone
	}
	v, err := uc.resolveVersion(ctx, c, version)
	if err != nil {
		return nil, err
	}
	return uc.transition(ctx, tenantID, id, func(ctx context.Context, cur *domain.Campaign) error {
		if !cur.SameContent(c) {
			return domain.ErrConcurrentChange
		}
		if err := cur.ScheduleLocal(at, fallback, v, uc.now()); err != nil {
			return err
		}
		if err := uc.campaigns.Update(ctx, cur); err != nil {
			return err
		}
		return uc.publishScheduled(ctx, cur)
	})
}

// Pause detiene la campana. Un lote que ya estaba en vuelo termina su entrega (lo
// aceptado por transactional se cuenta); el siguiente no sale.
func (uc *UseCase) Pause(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error) {
	return uc.transition(ctx, tenantID, id, func(ctx context.Context, cur *domain.Campaign) error {
		if err := cur.Pause(domain.PauseReasonManual); err != nil {
			return err
		}
		if err := uc.campaigns.Update(ctx, cur); err != nil {
			return err
		}
		return uc.publishPaused(ctx, cur)
	})
}

// Resume la reanuda. Los intentos del lote pendiente vuelven a cero: quien reanuda da
// por resuelto lo que la paro, y el lote merece de nuevo sus diez intentos.
func (uc *UseCase) Resume(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error) {
	return uc.transition(ctx, tenantID, id, func(ctx context.Context, cur *domain.Campaign) error {
		firstStart, err := cur.Resume(uc.now())
		if err != nil {
			return err
		}
		if err := uc.campaigns.Update(ctx, cur); err != nil {
			return err
		}
		if err := uc.batches.ResetAttempts(ctx, tenantID, id); err != nil {
			return err
		}
		if err := uc.publishResumed(ctx, cur); err != nil {
			return err
		}
		if firstStart {
			return uc.publishStarted(ctx, cur)
		}
		return nil
	})
}

// Cancel la cierra sin vuelta atras y descarta la pagina que hubiera guardada.
func (uc *UseCase) Cancel(ctx context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error) {
	return uc.transition(ctx, tenantID, id, func(ctx context.Context, cur *domain.Campaign) error {
		if err := cur.Cancel(); err != nil {
			return err
		}
		if err := uc.campaigns.Update(ctx, cur); err != nil {
			return err
		}
		if err := uc.batches.DiscardPendingPages(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.publishCancelled(ctx, cur)
	})
}

// transition corre fn con la campana bloqueada y devuelve su estado final.
func (uc *UseCase) transition(ctx context.Context, tenantID, id uuid.UUID, fn func(ctx context.Context, cur *domain.Campaign) error) (*domain.Campaign, error) {
	var out *domain.Campaign
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		cur, err := uc.campaigns.GetForUpdate(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if err := fn(ctx, cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveVersion devuelve la version pedida o, si no se pidio, la publicada ahora.
func (uc *UseCase) resolveVersion(ctx context.Context, c *domain.Campaign, requested *int) (int, error) {
	if requested != nil {
		if *requested < 1 {
			return 0, domain.NewValidationError("template_version: debe ser mayor que cero")
		}
		return *requested, nil
	}
	return uc.templates.PublishedVersion(ctx, c.TenantID, c.TemplateID)
}

// resolveVersions fija la version de la campana y la de cada variante A/B. Una variante
// con su propia plantilla y sin version pedida toma la publicada de esa plantilla; sin
// plantilla propia, la de la campana.
func (uc *UseCase) resolveVersions(ctx context.Context, c *domain.Campaign, requested *int) (int, []int, error) {
	v, err := uc.resolveVersion(ctx, c, requested)
	if err != nil {
		return 0, nil, err
	}
	if c.ABTest == nil {
		return v, nil, nil
	}
	out := make([]int, len(c.ABTest.Variants))
	for i, variant := range c.ABTest.Variants {
		switch {
		case variant.TemplateVersion != nil:
			out[i] = *variant.TemplateVersion
		case variant.TemplateID == nil || *variant.TemplateID == c.TemplateID:
			out[i] = v
		default:
			pv, err := uc.templates.PublishedVersion(ctx, c.TenantID, *variant.TemplateID)
			if err != nil {
				return 0, nil, fmt.Errorf("variante %s: %w", domain.VariantLabel(i), err)
			}
			out[i] = pv
		}
	}
	return v, out, nil
}

// TestInput es un envio de prueba de la campana. Variant elige una variante A/B (su
// plantilla y su asunto); nil prueba el contenido de la campana.
type TestInput struct {
	Emails          []string
	TemplateVersion *int
	Variant         *int
}

// SendTest envia la campana a unas pocas direcciones por la misma via que los lotes,
// con clave propia y sin variables. Cada destinatario lleva el contact_id sintetico de
// domain.TestContactID: el consumidor de estadisticas lo reconoce y no suma sus eventos.
// Tampoco se toca ningun contador.
func (uc *UseCase) SendTest(ctx context.Context, tenantID, id uuid.UUID, in TestInput) (*ports.BatchResult, error) {
	emails, err := domain.NormalizeTestRecipients(in.Emails)
	if err != nil {
		return nil, err
	}
	c, err := uc.campaigns.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	content, err := uc.testContent(ctx, c, in)
	if err != nil {
		return nil, err
	}
	recipients := make([]domain.Recipient, len(emails))
	for i, e := range emails {
		contactID := domain.TestContactID(c.ID, e)
		recipients[i] = domain.Recipient{Email: e, ContactID: &contactID, Variables: map[string]json.RawMessage{}}
	}
	return uc.sender.SendBatch(ctx, tenantID, ports.BatchRequest{
		CampaignID:      c.ID,
		CampaignName:    c.Name,
		IdempotencyKey:  domain.TestIdempotencyKey(c.ID, uuid.New()),
		FromEmail:       c.FromEmail,
		FromName:        c.FromName,
		ReplyTo:         c.ReplyTo,
		TemplateID:      content.TemplateID,
		TemplateVersion: content.TemplateVersion,
		Subject:         content.Subject,
		UTMContent:      content.UTMContent,
		Recipients:      recipients,
		Tags:            map[string]string{"test": "true"},
	})
}

// testContent es lo que recibe un envio de prueba: la version pedida, la ya fijada o la
// publicada, de la plantilla de la campana o de la variante elegida.
func (uc *UseCase) testContent(ctx context.Context, c *domain.Campaign, in TestInput) (domain.Content, error) {
	if in.TemplateVersion != nil && *in.TemplateVersion < 1 {
		return domain.Content{}, domain.NewValidationError("template_version: debe ser mayor que cero")
	}
	if in.Variant == nil {
		version := c.TemplateVersion
		if in.TemplateVersion != nil {
			version = in.TemplateVersion
		}
		if version != nil {
			return domain.Content{TemplateID: c.TemplateID, TemplateVersion: *version}, nil
		}
		v, err := uc.templates.PublishedVersion(ctx, c.TenantID, c.TemplateID)
		return domain.Content{TemplateID: c.TemplateID, TemplateVersion: v}, err
	}
	i := *in.Variant
	if c.ABTest == nil || i < 0 || i >= len(c.ABTest.Variants) {
		return domain.Content{}, domain.NewValidationError("variant: la campana no tiene esa variante")
	}
	variant := c.ABTest.Variants[i]
	content := domain.Content{TemplateID: c.ABTest.VariantTemplate(i, c.TemplateID), Subject: variant.Subject,
		UTMContent: domain.VariantUTMContent(i)}
	own := content.TemplateID != c.TemplateID
	switch {
	case in.TemplateVersion != nil:
		content.TemplateVersion = *in.TemplateVersion
	case variant.PinnedVersion != nil:
		content.TemplateVersion = *variant.PinnedVersion
	case variant.TemplateVersion != nil:
		content.TemplateVersion = *variant.TemplateVersion
	case !own && c.TemplateVersion != nil:
		content.TemplateVersion = *c.TemplateVersion
	default:
		v, err := uc.templates.PublishedVersion(ctx, c.TenantID, content.TemplateID)
		if err != nil {
			return domain.Content{}, err
		}
		content.TemplateVersion = v
	}
	return content, nil
}
