package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

// PurgeMailbox borra las libretas y los calendarios, con sus contactos y eventos y su registro de
// cambios, de un buzon que se dio de baja en mail-directory, y devuelve cuantos borro de cada tipo. Se identifica por el id del buzon y no
// por su nombre: uno recreado con el mismo nombre tiene otro id y conserva lo suyo.
//
// Corre con la misma identidad acotada que una peticion del buzon (empresa y buzon en la sesion),
// de modo que las politicas de fila valen tambien aqui. Es idempotente: sin datos no hace nada.
func (uc *UseCase) PurgeMailbox(ctx context.Context, tenantID, mailboxID uuid.UUID) (domain.PurgeResult, error) {
	if tenantID == uuid.Nil || mailboxID == uuid.Nil {
		return domain.PurgeResult{}, domain.ErrInvalidMailbox
	}
	p := domain.Principal{TenantID: tenantID, MailboxID: mailboxID}
	bound, err := uc.tenant.Bind(ctx, p)
	if err != nil {
		return domain.PurgeResult{}, err
	}
	books, err := uc.store.DeleteMailboxData(bound, p)
	if err != nil {
		return domain.PurgeResult{}, err
	}
	calendars, err := uc.calendars.DeleteMailboxCalendars(bound, p)
	if err != nil {
		return domain.PurgeResult{}, err
	}
	return domain.PurgeResult{Addressbooks: books, Calendars: calendars}, nil
}
