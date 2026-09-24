package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// PutFiltersRequest reemplaza las reglas y el reenvio del buzon enteros: lo que no llega se borra.
type PutFiltersRequest struct {
	Rules      []domain.FilterRule
	Forwarding domain.Forwarding
}

// FiltersByUsername devuelve las reglas y el reenvio del buzon del webmail; si nunca los guardo,
// vacios y apagados.
func (uc *UseCase) FiltersByUsername(ctx context.Context, username string) (*domain.MailboxFilters, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	var out *domain.MailboxFilters
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		f, err := uc.filters.ByUsername(ctx, tenantID, m.Username)
		if errors.Is(err, domain.ErrNotFound) {
			out = domain.NewMailboxFilters(tenantID, m.Username)
			return nil
		}
		out = f
		return err
	})
	return out, err
}

// PutFiltersByUsername valida, genera el script Sieve y guarda. La validacion necesita el nombre del
// buzon (el reenvio no puede apuntar a el), asi que va despues de localizarlo y antes de la
// transaccion: una entrada invalida no toma el candado de la empresa.
func (uc *UseCase) PutFiltersByUsername(ctx context.Context, username string, req PutFiltersRequest) (*domain.MailboxFilters, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	login, _, err := domain.NormalizeEmail(username)
	if err != nil {
		return nil, err
	}
	f := &domain.MailboxFilters{ID: uuid.New(), TenantID: tenantID, Username: login, Rules: req.Rules, Forwarding: req.Forwarding}
	if err := f.Normalize(); err != nil {
		return nil, err
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		if m.Username != f.Username {
			return domain.ErrNotFound
		}
		return uc.filters.Upsert(ctx, f)
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}
