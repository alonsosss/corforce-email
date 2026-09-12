package nats

import (
	"context"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
)

// Eventos persistentes del plano de control. Los consumen los servicios que
// materializan algo por tenant (identity, access-control, el gateway) con workers
// idempotentes: con JetStream, si un servicio esta caido el aviso se reentrega en vez
// de perderse. El subject se escribe literal en cada llamada por convencion.
const (
	// StreamName agrupa los eventos persistentes del dominio organization.
	StreamName = "ORGANIZATION"

	SubjectTenantCreated        = "organization.tenant.created"
	SubjectTenantStatusChanged  = "organization.tenant.status_changed"
	SubjectTenantModulesChanged = "organization.tenant.modules_changed"

	eventSource = "organization-service"
)

// TenantPublisher implementa ports.TenantEventPublisher. Con bus nil no publica: el
// servicio arranca sin NATS y el alta sigue funcionando sin avisos.
type TenantPublisher struct {
	bus *events.Bus
}

func NewTenantPublisher(bus *events.Bus) *TenantPublisher { return &TenantPublisher{bus: bus} }

func (p *TenantPublisher) TenantCreated(_ context.Context, t *domain.Tenant) error {
	if p == nil || p.bus == nil {
		return nil
	}
	return p.bus.PublishPersistent(SubjectTenantCreated, events.Event{
		Type:     SubjectTenantCreated,
		Source:   eventSource,
		TenantID: t.ID.String(),
		Data: map[string]interface{}{
			"tenant_id": t.ID.String(),
			"slug":      t.Slug,
			"name":      t.Name,
			"db_name":   t.DBName,
			"cell_id":   t.CellID.String(),
			"status":    t.Status,
		},
	})
}

func (p *TenantPublisher) TenantStatusChanged(_ context.Context, t *domain.Tenant, previous string) error {
	if p == nil || p.bus == nil {
		return nil
	}
	return p.bus.PublishPersistent(SubjectTenantStatusChanged, events.Event{
		Type:     SubjectTenantStatusChanged,
		Source:   eventSource,
		TenantID: t.ID.String(),
		Data: map[string]interface{}{
			"tenant_id":       t.ID.String(),
			"slug":            t.Slug,
			"status":          t.Status,
			"previous_status": previous,
		},
	})
}

func (p *TenantPublisher) TenantModulesChanged(_ context.Context, tenantID uuid.UUID, enabled, disabled []string) error {
	if p == nil || p.bus == nil {
		return nil
	}
	return p.bus.PublishPersistent(SubjectTenantModulesChanged, events.Event{
		Type:     SubjectTenantModulesChanged,
		Source:   eventSource,
		TenantID: tenantID.String(),
		Data: map[string]interface{}{
			"tenant_id": tenantID.String(),
			"enabled":   enabled,
			"disabled":  disabled,
		},
	})
}
