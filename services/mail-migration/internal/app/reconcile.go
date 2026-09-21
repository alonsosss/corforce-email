package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

// StaleMailboxes devuelve hasta limit ids de buzon de la empresa con trabajos, en orden y mayores que
// after, cuyo trabajo mas antiguo es anterior a before: lo que la conciliacion compara con los buzones que
// existen en mail-directory. Un buzon con trabajos recientes queda fuera hasta que pase la gracia.
func (uc *UseCase) StaleMailboxes(ctx context.Context, tenantID uuid.UUID, before time.Time, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	if tenantID == uuid.Nil {
		return nil, domain.ErrInvalidMailbox
	}
	tctx, err := uc.tenants.For(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return uc.repo.StaleMailboxIDs(tctx, tenantID, before, after, limit)
}
