package app

import (
	"context"
	"strings"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// DomainEvent es un evento domains.domain.* ya decodificado.
type DomainEvent struct {
	Action   string // verified | failed | deleted | sending_status_changed
	TenantID uuid.UUID
	Domain   string
	Purpose  string
	Status   string
	// SendingReady es sending_ready del evento; nil si no lo trae.
	SendingReady *bool
}

// ApplyDomainEvent mantiene la proyeccion local de dominios de envio. Los eventos que
// no cambian la capacidad de envio se ignoran. Un evento sin sending_ready conserva el que ya
// habia: lo ultimo que domain-service dijo de SES sigue valiendo.
func (uc *UseCase) ApplyDomainEvent(ctx context.Context, ev DomainEvent) error {
	name := strings.ToLower(strings.TrimSpace(ev.Domain))
	if name == "" {
		return domain.NewValidationError("domain vacío")
	}
	switch ev.Action {
	case "verified", "failed", "sending_status_changed":
		status := ev.Status
		if status == "" {
			if ev.Action == "sending_status_changed" {
				return domain.NewValidationError("status vacío")
			}
			status = ev.Action
		}
		return uc.repo.UpsertSendingDomain(ctx, &domain.SendingDomain{
			TenantID: ev.TenantID, Domain: name, Status: status, Purpose: ev.Purpose,
			SendingReady: ev.SendingReady, UpdatedAt: uc.now(),
		})
	case "deleted":
		return uc.repo.DeleteSendingDomain(ctx, ev.TenantID, name)
	}
	return nil
}
