package app

import (
	"context"
	"errors"
	"strings"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// inboxFolder es la unica carpeta en la que la pestana de la bandeja inteligente tiene sentido.
const inboxFolder = "INBOX"

// PrimarySenders devuelve las direcciones que el buzon fijo en Principal.
func (s *Service) PrimarySenders(ctx context.Context, sess domain.Session) ([]string, error) {
	list, err := s.primarySenders.PrimarySenders(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudieron leer los remitentes fijados en Principal", sess, err)
		return nil, unavailable(err)
	}
	return list, nil
}

// AddPrimarySender fija una direccion en Principal: sus mensajes caeran siempre en esa pestana.
func (s *Service) AddPrimarySender(ctx context.Context, sess domain.Session, address string) error {
	return s.primarySenderResult("no se pudo fijar el remitente en Principal", sess,
		s.primarySenders.AddPrimarySender(ctx, sess.Username, address))
}

// RemovePrimarySender devuelve una direccion a las reglas normales de la bandeja.
func (s *Service) RemovePrimarySender(ctx context.Context, sess domain.Session, address string) error {
	return s.primarySenderResult("no se pudo quitar el remitente de Principal", sess,
		s.primarySenders.RemovePrimarySender(ctx, sess.Username, address))
}

func (s *Service) primarySenderResult(msg string, sess domain.Session, err error) error {
	if err == nil {
		return nil
	}
	var invalid *domain.ValidationError
	if errors.Is(err, domain.ErrPrimarySenderLimit) || errors.As(err, &invalid) {
		return err
	}
	s.logFailure(msg, sess, err)
	return unavailable(err)
}

// primarySendersFor son los remitentes fijados que clasifican la bandeja de entrada. Si el directorio
// no responde se lista igual, sin ellos: la bandeja no depende de un ajuste de comodidad.
func (s *Service) primarySendersFor(ctx context.Context, sess domain.Session, folder string) []string {
	if !strings.EqualFold(folder, inboxFolder) {
		return nil
	}
	list, err := s.primarySenders.PrimarySenders(ctx, sess.Username)
	if err != nil {
		s.logger.Warn("webmail: la bandeja se lista sin los remitentes fijados en Principal",
			zap.String("username", sess.Username), zap.Error(err))
		return nil
	}
	return list
}

// classifierFor es el clasificador del buzon de la sesion con sus remitentes fijados.
func (s *Service) classifierFor(ctx context.Context, sess domain.Session, folder string) domain.Classifier {
	return domain.ClassifierFor(sess.Username).WithPrimarySenders(s.primarySendersFor(ctx, sess, folder))
}
