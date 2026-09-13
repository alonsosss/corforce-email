package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// releaseBatch acota cuantos programados se liberan por transaccion.
const releaseBatch = 200

// ReleaseDue encola los mensajes programados cuya hora ya paso. Cada lote va en su
// transaccion con FOR UPDATE SKIP LOCKED: varias replicas pueden correrlo a la vez.
func (uc *UseCase) ReleaseDue(ctx context.Context, tenantID uuid.UUID) (int, error) {
	total := 0
	for {
		released := 0
		err := uc.repo.Transact(ctx, func(ctx context.Context) error {
			ids, err := uc.repo.ReleaseDue(ctx, tenantID, uc.now(), releaseBatch)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := uc.publishQueued(ctx, &domain.Message{ID: id, TenantID: tenantID}); err != nil {
					return err
				}
			}
			released = len(ids)
			return nil
		})
		if err != nil {
			return total, err
		}
		total += released
		if released < releaseBatch {
			return total, nil
		}
	}
}
