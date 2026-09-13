package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
)

// suppressionSweepPage es cuantos contactos lee y consulta en bloque cada paso del barrido.
const suppressionSweepPage = 500

// SweepReport resume el barrido de una empresa.
type SweepReport struct {
	Checked int
	Changed int
}

// SweepSuppression contrasta con suppression el estado de todos los contactos de la
// empresa y corrige los que no coinciden. Cubre lo que no llego a aplicarse por evento: un
// evento de suppression perdido (tambien el anuncio de una caducidad,
// suppression.entry.expired), un contacto creado cuando su direccion ya estaba excluida y
// los contactos anteriores a los estados invalid y excluded.
//
// Cada pagina se consulta en bloque sin bloquear filas; solo el contacto cuyo estado
// cambiaria se vuelve a decidir como un evento, con su fila bloqueada y sus causas leidas
// despues del bloqueo, para no pisar un evento aplicado entre medias. Nunca revoca el
// consentimiento: sin el evento no se sabe si una baja vigente se acaba de pedir o ya la
// levanto un reconsentimiento, y se decide como ReconcileSuppression sin baja registrada.
func (uc *UseCase) SweepSuppression(ctx context.Context, tenantID uuid.UUID) (SweepReport, error) {
	var rep SweepReport
	if uc.suppression == nil {
		return rep, errors.New("contacts: sin lector del estado de suppression")
	}
	after := uuid.Nil
	for {
		page, err := uc.contacts.ListAfter(ctx, tenantID, after, suppressionSweepPage)
		if err != nil {
			return rep, err
		}
		if len(page) == 0 {
			return rep, nil
		}
		emails := make([]string, len(page))
		for i := range page {
			emails[i] = page[i].Email
		}
		active, err := uc.suppression.ActiveCausesOf(ctx, tenantID, emails)
		if err != nil {
			return rep, fmt.Errorf("consultar las causas vigentes en suppression: %w", err)
		}
		for i := range page {
			rep.Checked++
			probe := page[i]
			if !probe.ReconcileSuppression(domain.CausesOf(active[probe.Email]), false) {
				continue
			}
			changed, err := uc.reconcileContact(ctx, tenantID, probe.ID)
			if err != nil {
				return rep, err
			}
			if changed {
				rep.Changed++
			}
		}
		if len(page) < suppressionSweepPage {
			return rep, nil
		}
		after = page[len(page)-1].ID
	}
}

// reconcileContact vuelve a decidir el estado de un contacto con su fila bloqueada y las
// causas vigentes leidas despues del bloqueo. Devuelve si cambio.
func (uc *UseCase) reconcileContact(ctx context.Context, tenantID, id uuid.UUID) (bool, error) {
	changed := false
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		c, err := uc.contacts.GetForUpdate(ctx, tenantID, id)
		if errors.Is(err, domain.ErrContactNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		active, err := uc.suppression.ActiveCauses(ctx, tenantID, c.Email)
		if err != nil {
			return fmt.Errorf("consultar el estado vigente en suppression: %w", err)
		}
		if !c.ReconcileSuppression(domain.CausesOf(active), false) {
			return nil
		}
		changed = true
		return uc.saveStatus(ctx, c)
	})
	return changed, err
}
