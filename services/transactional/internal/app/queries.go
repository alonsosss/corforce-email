package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// MessageWithEvents es el detalle de un mensaje con su historial.
type MessageWithEvents struct {
	domain.Message
	Events []domain.Event `json:"events"`
}

func (uc *UseCase) ListMessages(ctx context.Context, tenantID uuid.UUID, f domain.MessageFilter, page, perPage int) ([]domain.Message, int64, error) {
	if f.Status != "" {
		valid := false
		for _, s := range domain.Statuses {
			if s == f.Status {
				valid = true
				break
			}
		}
		if !valid {
			return nil, 0, domain.NewValidationError("status must be one of the known statuses")
		}
	}
	return uc.repo.ListMessages(ctx, tenantID, f, (page-1)*perPage, perPage)
}

func (uc *UseCase) GetMessage(ctx context.Context, tenantID, id uuid.UUID) (*MessageWithEvents, error) {
	msg, err := uc.repo.GetMessage(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	events, err := uc.repo.ListEvents(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []domain.Event{}
	}
	return &MessageWithEvents{Message: *msg, Events: events}, nil
}

func (uc *UseCase) ListEvents(ctx context.Context, tenantID, id uuid.UUID) ([]domain.Event, error) {
	if _, err := uc.repo.GetMessage(ctx, tenantID, id); err != nil {
		return nil, err
	}
	return uc.repo.ListEvents(ctx, tenantID, id)
}

func (uc *UseCase) Stats(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.StatusCount, error) {
	if !to.After(from) {
		return nil, domain.NewValidationError("to must be after from")
	}
	return uc.repo.CountByStatus(ctx, tenantID, from, to)
}

func (uc *UseCase) ListSendingDomains(ctx context.Context, tenantID uuid.UUID) ([]domain.SendingDomain, error) {
	return uc.repo.ListSendingDomains(ctx, tenantID)
}
