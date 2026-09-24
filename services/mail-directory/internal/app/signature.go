package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// PutSignatureRequest reemplaza la firma del buzon entera.
type PutSignatureRequest struct {
	Enabled   bool
	HTML      string
	Text      string
	OnReplies bool
}

// SignatureByUsername devuelve la firma del buzon con el que el webmail inicio sesion; si nunca la
// guardo, una desactivada y vacia.
func (uc *UseCase) SignatureByUsername(ctx context.Context, username string) (*domain.MailboxSignature, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	var out *domain.MailboxSignature
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		s, err := uc.signatures.ByUsername(ctx, tenantID, m.Username)
		if errors.Is(err, domain.ErrNotFound) {
			out = domain.NewMailboxSignature(tenantID, m.Username)
			return nil
		}
		out = s
		return err
	})
	return out, err
}

// PutSignatureByUsername valida y guarda la firma. Se valida antes de abrir la transaccion.
func (uc *UseCase) PutSignatureByUsername(ctx context.Context, username string, req PutSignatureRequest) (*domain.MailboxSignature, error) {
	s := &domain.MailboxSignature{ID: uuid.New(), Enabled: req.Enabled, HTML: req.HTML, Text: req.Text, OnReplies: req.OnReplies}
	if err := s.Normalize(); err != nil {
		return nil, err
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	s.TenantID = tenantID
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		s.Username = m.Username
		return uc.signatures.Upsert(ctx, s)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}
