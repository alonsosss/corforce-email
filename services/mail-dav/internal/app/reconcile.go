package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

// StaleMailboxes devuelve hasta limit ids de buzon de la empresa con libretas o calendarios, en orden y
// mayores que after, cuyo dato mas antiguo es anterior a before: lo que la conciliacion compara con los
// buzones que existen en mail-directory. Un buzon con datos recientes queda fuera hasta que pase la gracia.
func (uc *UseCase) StaleMailboxes(ctx context.Context, tenantID uuid.UUID, before time.Time, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	if tenantID == uuid.Nil {
		return nil, domain.ErrInvalidMailbox
	}
	if uc.index == nil {
		return nil, errors.New("mail-dav: sin indice de buzones para la conciliacion")
	}
	bound, err := uc.tenant.Bind(ctx, domain.Principal{TenantID: tenantID})
	if err != nil {
		return nil, err
	}
	return uc.index.StaleMailboxIDs(bound, tenantID, before, after, limit)
}

// ReconcileMailbox retira los datos de un buzon que la conciliacion encontro borrado en mail-directory, por
// el mismo camino y con las mismas garantias que el consumidor de mail.mailbox.deleted (PurgeMailbox), y
// devuelve cuantas libretas y calendarios retiro.
func (uc *UseCase) ReconcileMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (int, error) {
	removed, err := uc.PurgeMailbox(ctx, tenantID, mailboxID)
	if err != nil {
		return 0, err
	}
	return removed.Addressbooks + removed.Calendars, nil
}
