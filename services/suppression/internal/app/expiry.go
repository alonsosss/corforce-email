package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
)

// expiryPage es cuantas caducidades pendientes lee cada paso del barrido.
const expiryPage = 200

// ExpiryReport resume el anuncio de caducidades de una empresa.
type ExpiryReport struct {
	Announced int
}

// AnnounceExpired publica suppression.entry.expired por cada exclusion manual de la
// empresa cuya caducidad ya paso y aun no se anuncio. Todas se juzgan con el mismo
// instante, el del inicio de la pasada: la que caduca durante ella queda para la
// siguiente.
//
// Cada caducidad se anuncia en su propia transaccion, con el mismo orden de bloqueos que
// las altas y retiradas (la direccion y despues la fila): varias filas en una transaccion
// podrian cruzarse con una carga masiva, que bloquea filas en su propio orden. El evento
// se encola en la transaccion que anota la caducidad como anunciada, asi que existe si y
// solo si la marca existe: un fallo antes del commit no deja ni lo uno ni lo otro y la
// siguiente pasada lo repite, y otra replica que llegue a la vez no la reclama dos veces
// (ports.EntryRepository.ClaimExpiry).
func (uc *UseCase) AnnounceExpired(ctx context.Context, tenantID uuid.UUID) (ExpiryReport, error) {
	var rep ExpiryReport
	now := uc.now()
	var after *ports.ExpiryCursor
	for {
		page, err := uc.entries.ListExpiryPending(ctx, tenantID, now, after, expiryPage)
		if err != nil {
			return rep, err
		}
		for i := range page {
			announced, err := uc.announceExpiry(ctx, tenantID, page[i].ID, page[i].Email, now)
			if err != nil {
				return rep, err
			}
			if announced {
				rep.Announced++
			}
		}
		if len(page) < expiryPage {
			return rep, nil
		}
		last := page[len(page)-1]
		after = &ports.ExpiryCursor{ExpiresAt: *last.ExpiresAt, ID: last.ID}
	}
}

// announceExpiry reclama la caducidad de la causa id y encola su evento con las causas que
// le quedan vigentes a la direccion. Devuelve false si ya no habia nada que anunciar.
func (uc *UseCase) announceExpiry(ctx context.Context, tenantID, id uuid.UUID, email string, now time.Time) (bool, error) {
	announced := false
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.entries.LockAddress(ctx, tenantID, email); err != nil {
			return err
		}
		e, err := uc.entries.ClaimExpiry(ctx, tenantID, id, now)
		if errors.Is(err, domain.ErrEntryNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		reasons, err := uc.activeReasons(ctx, tenantID, e.Email, now)
		if err != nil {
			return err
		}
		if err := uc.events.EntryExpired(ctx, e, reasons); err != nil {
			return err
		}
		announced = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return announced, nil
}
