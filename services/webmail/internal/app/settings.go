package app

import (
	"context"
	"errors"
	"strings"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// Signature devuelve la firma del buzon de la sesion.
func (s *Service) Signature(ctx context.Context, sess domain.Session) (domain.Signature, error) {
	sig, err := s.signatures.Signature(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudo leer la firma", sess, err)
		return domain.Signature{}, unavailable(err)
	}
	return sig, nil
}

// SetSignature guarda la firma del buzon de la sesion. El HTML lo sanea el webmail con la misma
// politica que un mensaje saliente y de el sale la version en texto; el tope lo aplica el directorio.
func (s *Service) SetSignature(ctx context.Context, sess domain.Session, enabled bool, html string, onReplies bool) (domain.Signature, error) {
	in := domain.SignatureInput{Enabled: enabled, OnReplies: onReplies}
	if strings.TrimSpace(html) != "" {
		in.HTML, in.Text = s.sanitizer.Outgoing(html)
	}
	sig, err := s.signatures.SetSignature(ctx, sess.Username, in)
	if err != nil {
		return domain.Signature{}, s.directoryError("no se pudo guardar la firma", sess, err)
	}
	return sig, nil
}

// Filters devuelve las reglas y el reenvio del buzon de la sesion.
func (s *Service) Filters(ctx context.Context, sess domain.Session) (domain.MailFilters, error) {
	f, err := s.filters.Filters(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudieron leer las reglas", sess, err)
		return domain.MailFilters{}, unavailable(err)
	}
	return f, nil
}

// SetFilters reemplaza las reglas y el reenvio del buzon de la sesion. Las valida mail-directory,
// que genera el script Sieve: un dato que rechaza vuelve con su campo (rules[3].conditions[0].value).
func (s *Service) SetFilters(ctx context.Context, sess domain.Session, in domain.MailFiltersInput) (domain.MailFilters, error) {
	f, err := s.filters.SetFilters(ctx, sess.Username, in)
	if err != nil {
		return domain.MailFilters{}, s.directoryError("no se pudieron guardar las reglas", sess, err)
	}
	s.logger.Info("webmail: reglas del buzon cambiadas", zap.String("username", sess.Username),
		zap.Int("rules", len(in.Rules)), zap.Bool("forwarding", in.Forwarding.Enabled))
	return f, nil
}

// ChangePassword cambia la contrasena del buzon de la sesion. La actual se comprueba antes contra
// mail-auth, con la IP real del cliente para su freno de fuerza bruta: una contrasena actual mala
// es domain.ErrInvalidCredentials y no cambia ni revoca nada. El cambio lo hace mail-directory, con
// su politica y su evento; tras el cambio se revocan tambien aqui las sesiones del buzon, sin
// esperar al evento.
func (s *Service) ChangePassword(ctx context.Context, sess domain.Session, current, next, remoteIP string) error {
	if current == "" || len(current) > maxPasswordBytes {
		return domain.ErrInvalidCredentials
	}
	if next == "" {
		return domain.NewValidationError("new_password", "es obligatoria")
	}
	if len(next) > maxPasswordBytes {
		return domain.NewValidationError("new_password", "demasiado larga")
	}
	if next == current {
		return domain.NewValidationError("new_password", "debe ser distinta de la actual")
	}
	if _, err := s.auth.Verify(ctx, sess.Username, current, remoteIP); err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			s.logger.Info("webmail: cambio de contrasena con la actual incorrecta", zap.String("username", sess.Username), zap.String("remote_ip", remoteIP))
			return domain.ErrInvalidCredentials
		}
		s.logFailure("no se pudo verificar la contrasena actual", sess, err)
		return unavailable(err)
	}
	if err := s.passwords.SetPassword(ctx, sess.Username, next); err != nil {
		var verr *domain.ValidationError
		if errors.As(err, &verr) {
			return domain.NewValidationError("new_password", verr.Reason)
		}
		s.logFailure("no se pudo cambiar la contrasena", sess, err)
		return unavailable(err)
	}
	s.logger.Info("webmail: contrasena del buzon cambiada", zap.String("username", sess.Username), zap.String("remote_ip", remoteIP))
	if err := s.RevokeMailbox(context.WithoutCancel(ctx), sess.Username, s.clock()); err != nil {
		// El evento del directorio revoca igual; hasta entonces la sesion sigue abierta.
		s.logger.Warn("webmail: no se pudieron revocar las sesiones tras el cambio de contrasena", zap.String("username", sess.Username), zap.Error(err))
	}
	return nil
}

// directoryError deja pasar el rechazo de una regla del directorio y convierte lo demas en
// indisponibilidad.
func (s *Service) directoryError(msg string, sess domain.Session, err error) error {
	var verr *domain.ValidationError
	if errors.As(err, &verr) {
		return err
	}
	s.logFailure(msg, sess, err)
	return unavailable(err)
}
