package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// MaxSendableIDs es el tope de ids por consulta de enviables.
const MaxSendableIDs = 500

// SendableContacts devuelve, de los ids pedidos, los contactos de la empresa a los que se
// puede enviar marketing ahora: la misma regla que la audiencia de una campana (activos y
// con consentimiento vigente). Los demas se omiten sin decir por que. Lo usa automations
// antes de cada paso de envio.
func (uc *UseCase) SendableContacts(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Contact, error) {
	ids = dedupeIDs(ids)
	if len(ids) == 0 || len(ids) > MaxSendableIDs {
		return nil, domain.ErrInvalidContactIDs
	}
	out, err := uc.query.Sendable(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.Contact{}
	}
	return out, nil
}

// ListMembersAmong dice cuales de los contactos pedidos estan en la lista (el filtro por
// lista de los flujos de automations). domain.ErrListNotFound si la lista no existe.
func (uc *UseCase) ListMembersAmong(ctx context.Context, tenantID, listID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) > MaxMembersPerRequest {
		return nil, domain.ErrTooManyMembers
	}
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		return nil, domain.ErrInvalidContactIDs
	}
	if _, err := uc.lists.Get(ctx, tenantID, listID); err != nil {
		return nil, err
	}
	out, err := uc.lists.MembersAmong(ctx, tenantID, listID, ids)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []uuid.UUID{}
	}
	return out, nil
}
