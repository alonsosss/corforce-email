package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

// IngestResult dice que hizo la ingesta con un evento.
type IngestResult struct {
	// Duplicate: el evento ya se habia contado (reentrega).
	Duplicate bool
	// Changed: el evento cambio la fila del mensaje o de la campana.
	Changed bool
	// Test: el evento es de un envio de prueba y se descarto sin contarlo.
	Test bool
}

// IngestMessageEvent cuenta un hito de un mensaje. Todo ocurre en una transaccion:
// registrar el id del evento (si ya estaba, no se hace nada mas), bloquear la fila del
// mensaje, decidir si el hito es nuevo y, solo entonces, mover los agregados. Una
// reentrega o un segundo evento del mismo hito no suman. Un envio de prueba no toca nada.
func (uc *UseCase) IngestMessageEvent(ctx context.Context, ev domain.MessageEvent) (IngestResult, error) {
	if ev.Test {
		return IngestResult{Test: true}, nil
	}
	ev.OccurredAt = domain.OccurredAt(ev.OccurredAt, ev.PublishedAt, uc.now())
	if err := ev.Validate(); err != nil {
		return IngestResult{}, err
	}
	var res IngestResult
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		res = IngestResult{}
		fresh, err := uc.ledger.MarkProcessed(ctx, ev.TenantID, ev.EventID)
		if err != nil {
			return err
		}
		if !fresh {
			res.Duplicate = true
			return nil
		}
		fact, err := uc.facts.LockOrCreate(ctx, domain.NewMessageFact(ev))
		if err != nil {
			return err
		}
		// Cada clic cuenta en su enlace aunque el hito del mensaje ya estuviera: el
		// segundo clic de una persona no mueve clicked_unique pero si los clics del enlace.
		if click, ok := linkClick(ev, fact); ok {
			if err := uc.links.RecordClick(ctx, click); err != nil {
				return err
			}
			res.Changed = true
		}
		change := fact.Apply(ev)
		if !change.Changed {
			return nil
		}
		if err := uc.facts.Save(ctx, fact); err != nil {
			return err
		}
		for _, day := range domain.GroupByDay(change.Deltas) {
			if err := uc.stats.Apply(ctx, fact.TenantID, fact.Dimensions, day); err != nil {
				return err
			}
		}
		res.Changed = true
		return nil
	})
	if err != nil {
		return IngestResult{}, err
	}
	return res, nil
}

// linkClick es el clic a desglosar por enlace: solo los de campana con URL agregable. La
// campana sale de la fila del mensaje, fijada por su primer evento.
func linkClick(ev domain.MessageEvent, fact *domain.MessageFact) (domain.LinkClick, bool) {
	if ev.Milestone != domain.MilestoneClicked || ev.Link == "" || fact.CampaignID == nil {
		return domain.LinkClick{}, false
	}
	return domain.LinkClick{
		TenantID: fact.TenantID, CampaignID: *fact.CampaignID, MessageID: fact.MessageID,
		URL: ev.Link, At: ev.OccurredAt,
	}, true
}

// IngestCampaignEvent registra un cambio de estado de una campana, idempotente por id de
// evento e independiente del orden de llegada.
func (uc *UseCase) IngestCampaignEvent(ctx context.Context, ev domain.CampaignEvent) (IngestResult, error) {
	ev.OccurredAt = domain.OccurredAt(ev.OccurredAt, ev.PublishedAt, uc.now())
	ev.Status = domain.CampaignStatus(ev.Status, ev.Action)
	if err := ev.Validate(); err != nil {
		return IngestResult{}, err
	}
	var res IngestResult
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		res = IngestResult{}
		fresh, err := uc.ledger.MarkProcessed(ctx, ev.TenantID, ev.EventID)
		if err != nil {
			return err
		}
		if !fresh {
			res.Duplicate = true
			return nil
		}
		created, err := uc.campaigns.CreateIfAbsent(ctx, domain.NewCampaignSeen(ev))
		if err != nil {
			return err
		}
		if created {
			res.Changed = true
			return nil
		}
		current, err := uc.campaigns.Lock(ctx, ev.TenantID, ev.CampaignID)
		if err != nil {
			return err
		}
		if !current.Merge(ev) {
			return nil
		}
		res.Changed = true
		return uc.campaigns.Save(ctx, current)
	})
	if err != nil {
		return IngestResult{}, err
	}
	return res, nil
}

// PruneResult cuenta lo borrado por la poda de una empresa.
type PruneResult struct {
	Messages   int64
	Events     int64
	LinkClicks int64
}

// Prune borra las filas de mensaje sin actividad en la retencion configurada y los ids de
// evento ya olvidables. Los agregados diarios no se tocan.
func (uc *UseCase) Prune(ctx context.Context, tenantID uuid.UUID) (PruneResult, error) {
	now := uc.now()
	var res PruneResult
	var err error
	if res.Messages, err = uc.facts.PruneInactive(ctx, tenantID, now.Add(-uc.retention)); err != nil {
		return res, err
	}
	if res.Events, err = uc.ledger.PruneProcessed(ctx, tenantID, now.Add(-ProcessedEventsRetention)); err != nil {
		return res, err
	}
	if res.LinkClicks, err = uc.links.PruneClicks(ctx, tenantID, now.Add(-uc.retention)); err != nil {
		return res, err
	}
	return res, nil
}
