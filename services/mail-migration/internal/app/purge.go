package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

// PurgeMailbox borra los trabajos de un buzon que se dio de baja en mail-directory y devuelve
// cuantos borro. Se identifica por el id del buzon y no por su nombre: uno recreado con el mismo
// nombre tiene otro id y conserva sus trabajos.
//
// Se borran las filas, no se anonimizan: guardan el nombre del buzon, el usuario y el servidor de
// origen y los nombres de las carpetas, y nada mas las lee una vez que el buzon no existe. La
// evidencia de cada trabajo esta en sus eventos de auditoria, que no dependen de la fila: un
// trabajo activo anuncia su cancelacion (migration.job.cancelled, error_code mailbox_deleted) en
// la misma transaccion que lo borra, y un ejecutor que aun lo tenga pierde el lease en su
// siguiente latido y se detiene.
//
// Es idempotente: sin trabajos no hace nada.
func (uc *UseCase) PurgeMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (int, error) {
	if tenantID == uuid.Nil || mailboxID == uuid.Nil {
		return 0, domain.ErrInvalidMailbox
	}
	now := uc.now()
	removed := 0
	err := uc.inTenant(ctx, tenantID, func(ctx context.Context) error {
		jobs, err := uc.repo.DeleteByMailbox(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		for i := range jobs {
			if !jobs[i].Status.Active() {
				continue
			}
			jobs[i].CancelForDeletedMailbox(now)
			if err := uc.events.Finished(ctx, &jobs[i], uuid.Nil); err != nil {
				return err
			}
		}
		removed = len(jobs)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
