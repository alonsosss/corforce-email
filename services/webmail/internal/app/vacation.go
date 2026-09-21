package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// Vacation devuelve la respuesta automatica del buzon con el que se abrio la sesion. El buzon sale de
// la sesion, nunca de la peticion: nadie puede leer ni cambiar la de otro.
func (s *Service) Vacation(ctx context.Context, sess domain.Session) (domain.Vacation, error) {
	v, err := s.vacations.Vacation(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudo leer la respuesta automatica", sess, err)
		return domain.Vacation{}, unavailable(err)
	}
	return v, nil
}

// SetVacation reemplaza la respuesta automatica del buzon de la sesion. La regla la aplica
// mail-directory: un texto que rechaza vuelve como *domain.ValidationError y llega al usuario tal cual.
func (s *Service) SetVacation(ctx context.Context, sess domain.Session, in domain.VacationInput) (domain.Vacation, error) {
	v, err := s.vacations.SetVacation(ctx, sess.Username, in)
	if err != nil {
		var verr *domain.ValidationError
		if errors.As(err, &verr) {
			return domain.Vacation{}, err
		}
		s.logFailure("no se pudo guardar la respuesta automatica", sess, err)
		return domain.Vacation{}, unavailable(err)
	}
	return v, nil
}

func (s *Service) logFailure(msg string, sess domain.Session, err error) {
	s.logger.Error("webmail: "+msg, zap.String("username", sess.Username), zap.Error(err))
}
