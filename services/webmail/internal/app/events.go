package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// WatchInbox suscribe a los cambios de la bandeja del buzon de la sesion. El buzon sale de la sesion, nunca
// de la peticion.
func (s *Service) WatchInbox(ctx context.Context, sess domain.Session) (<-chan domain.MailboxChange, error) {
	if s.watcher == nil {
		return nil, domain.ErrEventsDisabled
	}
	changes, err := s.watcher.Watch(ctx, sess.Username)
	if err != nil {
		if errors.Is(err, domain.ErrTooManyStreams) {
			return nil, err
		}
		s.logger.Error("webmail: no se pudo vigilar la bandeja", zap.String("username", sess.Username), zap.Error(err))
		return nil, unavailable(err)
	}
	return changes, nil
}
