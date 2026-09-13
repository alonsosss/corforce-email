package app

import (
	"context"
	"encoding/json"
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
	v, err := uc.resolveVersion(ctx, c, version)
	if err != nil {
		return nil, err
	}
	return uc.transition(ctx, tenantID, id, func(ctx context.Context, cur *domain.Campaign) error {
		if cur.TemplateID != c.TemplateID {
			return domain.ErrConcurrentChange
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
	v, err := uc.resolveVersion(ctx, c, version)
	if err != nil {
		return nil, err
	}
	return uc.transition(ctx, tenantID, id, func(ctx context.Context, cur *domain.Campaign) error {
		if cur.TemplateID != c.TemplateID {
			return domain.ErrConcurrentChange
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

// TestInput es un envio de prueba de la campana.
type TestInput struct {
	Emails          []string
	TemplateVersion *int
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
	var version int
	if in.TemplateVersion == nil && c.TemplateVersion != nil {
		version = *c.TemplateVersion
	} else if version, err = uc.resolveVersion(ctx, c, in.TemplateVersion); err != nil {
		return nil, err
	}
	recipients := make([]domain.Recipient, len(emails))
	for i, e := range emails {
		contactID := domain.TestContactID(c.ID, e)
		recipients[i] = domain.Recipient{Email: e, ContactID: &contactID, Variables: map[string]json.RawMessage{}}
	}
	return uc.sender.SendBatch(ctx, tenantID, ports.BatchRequest{
		CampaignID:      c.ID,
		IdempotencyKey:  domain.TestIdempotencyKey(c.ID, uuid.New()),
		FromEmail:       c.FromEmail,
		FromName:        c.FromName,
		ReplyTo:         c.ReplyTo,
		TemplateID:      c.TemplateID,
		TemplateVersion: version,
		Recipients:      recipients,
		Tags:            map[string]string{"test": "true"},
	})
}
