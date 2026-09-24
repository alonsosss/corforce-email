package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// QuickReplyInput es el nombre y el contenido de una respuesta rapida, con el HTML ya saneado.
type QuickReplyInput struct {
	Name string
	HTML string
	Text string
}

// QuickReplies devuelve las respuestas rapidas del buzon por nombre.
func (uc *UseCase) QuickReplies(ctx context.Context, username string) ([]domain.QuickReply, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	var out []domain.QuickReply
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		out, err = uc.quickReplies.ListByUsername(ctx, tenantID, m.Username)
		return err
	})
	if out == nil {
		out = []domain.QuickReply{}
	}
	return out, err
}

// CreateQuickReply guarda una respuesta rapida nueva, con el tope por buzon.
func (uc *UseCase) CreateQuickReply(ctx context.Context, username string, in QuickReplyInput) (*domain.QuickReply, error) {
	q := &domain.QuickReply{ID: uuid.New(), Name: in.Name, HTML: in.HTML, Text: in.Text}
	if err := q.Normalize(); err != nil {
		return nil, err
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	q.TenantID = tenantID
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		q.Username = m.Username
		n, err := uc.quickReplies.Count(ctx, tenantID, m.Username)
		if err != nil {
			return err
		}
		if n >= domain.MaxQuickReplies {
			return domain.ErrQuickReplyLimit
		}
		return uc.quickReplies.Create(ctx, q)
	})
	if err != nil {
		return nil, err
	}
	return q, nil
}

// UpdateQuickReply reemplaza el nombre y el contenido de una respuesta del buzon.
func (uc *UseCase) UpdateQuickReply(ctx context.Context, username string, id uuid.UUID, in QuickReplyInput) (*domain.QuickReply, error) {
	q := &domain.QuickReply{ID: id, Name: in.Name, HTML: in.HTML, Text: in.Text}
	if err := q.Normalize(); err != nil {
		return nil, err
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	q.TenantID = tenantID
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		q.Username = m.Username
		return uc.quickReplies.Update(ctx, q)
	})
	if err != nil {
		return nil, err
	}
	return q, nil
}

// DeleteQuickReply borra una respuesta del buzon.
func (uc *UseCase) DeleteQuickReply(ctx context.Context, username string, id uuid.UUID) error {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return err
	}
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		return uc.quickReplies.Delete(ctx, tenantID, m.Username, id)
	})
}
