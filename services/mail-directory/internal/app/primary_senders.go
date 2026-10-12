package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// PrimarySenders devuelve los remitentes que el buzon fija en Principal, por direccion.
func (uc *UseCase) PrimarySenders(ctx context.Context, username string) ([]domain.PrimarySender, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	var out []domain.PrimarySender
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		out, err = uc.primarySenders.ListByUsername(ctx, tenantID, m.Username)
		return err
	})
	if out == nil {
		out = []domain.PrimarySender{}
	}
	return out, err
}

// AddPrimarySender fija una direccion en Principal. Repetirla no es un error y no cuenta contra el tope.
func (uc *UseCase) AddPrimarySender(ctx context.Context, username, address string) (*domain.PrimarySender, error) {
	normalized, err := domain.NormalizePrimarySenderAddress(address)
	if err != nil {
		return nil, err
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	s := &domain.PrimarySender{ID: uuid.New(), TenantID: tenantID, Address: normalized}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		s.Username = m.Username
		existing, err := uc.primarySenders.ListByUsername(ctx, tenantID, m.Username)
		if err != nil {
			return err
		}
		for i := range existing {
			if existing[i].Address == normalized {
				*s = existing[i]
				return nil
			}
		}
		if len(existing) >= domain.MaxPrimarySenders {
			return domain.ErrPrimarySenderLimit
		}
		return uc.primarySenders.Add(ctx, s)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// RemovePrimarySender quita una direccion de Principal. Quitar una que no estaba no es un error.
func (uc *UseCase) RemovePrimarySender(ctx context.Context, username, address string) error {
	normalized, err := domain.NormalizePrimarySenderAddress(address)
	if err != nil {
		return err
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return err
	}
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		return uc.primarySenders.Remove(ctx, tenantID, m.Username, normalized)
	})
}
