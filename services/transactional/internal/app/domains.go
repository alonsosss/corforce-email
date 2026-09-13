package app

import (
	"context"
	"strings"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// DomainEvent es un evento domains.domain.* ya decodificado.
type DomainEvent struct {
	Action   string // verified | failed | deleted
	TenantID uuid.UUID
	Domain   string
	Purpose  string
	Status   string
}

// ApplyDomainEvent mantiene la proyeccion local de dominios de envio. Los eventos que
// no cambian la capacidad de envio se ignoran.
func (uc *UseCase) ApplyDomainEvent(ctx context.Context, ev DomainEvent) error {
	name := strings.ToLower(strings.TrimSpace(ev.Domain))
	if name == "" {
		return domain.NewValidationError("domain vacio")
	}
	switch ev.Action {
	case "verified", "failed":
		status := ev.Status
		if status == "" {
			status = ev.Action
		}
		return uc.repo.UpsertSendingDomain(ctx, &domain.SendingDomain{
			TenantID: ev.TenantID, Domain: name, Status: status, Purpose: ev.Purpose, UpdatedAt: uc.now(),
		})
	case "deleted":
		return uc.repo.DeleteSendingDomain(ctx, ev.TenantID, name)
	}
	return nil
}
