package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// QuickReplies devuelve las respuestas rapidas del buzon de la sesion con los topes del directorio.
func (s *Service) QuickReplies(ctx context.Context, sess domain.Session) (domain.QuickReplyList, error) {
	list, err := s.quickReplies.QuickReplies(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudieron leer las respuestas rapidas", sess, err)
		return domain.QuickReplyList{}, unavailable(err)
	}
	return list, nil
}

// CreateQuickReply guarda una respuesta nueva. El HTML se sanea con la politica de un mensaje saliente
// (las variables son texto y lo atraviesan intactas) y de el sale la version en texto.
func (s *Service) CreateQuickReply(ctx context.Context, sess domain.Session, in domain.QuickReplyInput) (domain.QuickReply, error) {
	q, err := s.sanitizedQuickReply(in)
	if err != nil {
		return domain.QuickReply{}, err
	}
	created, err := s.quickReplies.CreateQuickReply(ctx, sess.Username, q)
	if err != nil {
		return domain.QuickReply{}, s.quickReplyError("no se pudo guardar la respuesta rapida", sess, err)
	}
	s.logger.Info("webmail: respuesta rapida creada", zap.String("username", sess.Username), zap.String("quick_reply_id", created.ID))
	return created, nil
}

// UpdateQuickReply reemplaza el nombre y el contenido de una respuesta del buzon.
func (s *Service) UpdateQuickReply(ctx context.Context, sess domain.Session, id string, in domain.QuickReplyInput) (domain.QuickReply, error) {
	if err := domain.ValidateQuickReplyID(id); err != nil {
		return domain.QuickReply{}, err
	}
	q, err := s.sanitizedQuickReply(in)
	if err != nil {
		return domain.QuickReply{}, err
	}
	q.ID = id
	updated, err := s.quickReplies.UpdateQuickReply(ctx, sess.Username, q)
	if err != nil {
		return domain.QuickReply{}, s.quickReplyError("no se pudo cambiar la respuesta rapida", sess, err)
	}
	return updated, nil
}

// DeleteQuickReply borra una respuesta del buzon.
func (s *Service) DeleteQuickReply(ctx context.Context, sess domain.Session, id string) error {
	if err := domain.ValidateQuickReplyID(id); err != nil {
		return err
	}
	if err := s.quickReplies.DeleteQuickReply(ctx, sess.Username, id); err != nil {
		return s.quickReplyError("no se pudo borrar la respuesta rapida", sess, err)
	}
	return nil
}

func (s *Service) sanitizedQuickReply(in domain.QuickReplyInput) (domain.QuickReply, error) {
	if err := in.Validate(); err != nil {
		return domain.QuickReply{}, err
	}
	html, text := s.sanitizer.Outgoing(in.HTML)
	return domain.QuickReply{Name: in.Name, HTML: html, Text: text}, nil
}

func (s *Service) quickReplyError(msg string, sess domain.Session, err error) error {
	if errors.Is(err, domain.ErrQuickReplyNotFound) || errors.Is(err, domain.ErrQuickReplyExists) || errors.Is(err, domain.ErrQuickReplyLimit) {
		return err
	}
	return s.directoryError(msg, sess, err)
}
