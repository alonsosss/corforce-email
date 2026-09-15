package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"go.uber.org/zap"
)

// SessionRevoker retira de Dovecot lo que el directorio ya no permite. Con cada evento de buzon
// vacia su cache de autenticacion y, si el buzon ya no puede entrar o su credencial cambio, cierra
// sus sesiones abiertas (domain.SessionActionFor). Es idempotente: repetirlo vacia una cache ya
// vacia y no encuentra sesiones que cerrar.
type SessionRevoker struct {
	dir     ports.DirectoryReader
	engine  ports.EngineSessions
	metrics ports.SessionRevocationMetrics
	logger  *zap.Logger
}

type SessionRevokerDeps struct {
	Directory ports.DirectoryReader
	Engine    ports.EngineSessions
	Metrics   ports.SessionRevocationMetrics
	Logger    *zap.Logger
}

func NewSessionRevoker(d SessionRevokerDeps) *SessionRevoker {
	uc := &SessionRevoker{dir: d.Directory, engine: d.Engine, metrics: d.Metrics, logger: d.Logger}
	if uc.metrics == nil {
		uc.metrics = noSessionMetrics{}
	}
	if uc.logger == nil {
		uc.logger = zap.NewNop()
	}
	return uc
}

// noSessionMetrics descarta las metricas de la revocacion cuando no se inyectan (pruebas).
type noSessionMetrics struct{}

func (noSessionMetrics) SessionsRevoked(domain.SessionAction)                    {}
func (noSessionMetrics) SessionRevocationFailed(domain.SessionRevocationFailure) {}

// Revoke aplica en Dovecot lo que exige el cambio del buzon. Solo una actualizacion necesita el
// estado real del directorio: un borrado o un cambio de credencial cierran sus sesiones siempre.
// Un error deja el cambio sin aplicar y lo cuenta; quien llama lo reintenta.
func (uc *SessionRevoker) Revoke(ctx context.Context, change domain.MailboxChange, username string) error {
	var current *domain.Mailbox
	if change == domain.MailboxUpdated {
		m, err := uc.dir.MailboxByUsername(ctx, username)
		switch {
		case errors.Is(err, domain.ErrNotFound):
		case err != nil:
			uc.metrics.SessionRevocationFailed(domain.RevocationDirectory)
			return fmt.Errorf("estado del buzon en el directorio: %w", err)
		default:
			current = m
		}
	}
	action := domain.SessionActionFor(change, current)
	if err := uc.engine.ForgetCredentials(ctx, username, action == domain.SessionKick); err != nil {
		uc.metrics.SessionRevocationFailed(domain.RevocationFailureOf(err))
		return err
	}
	uc.metrics.SessionsRevoked(action)
	uc.logger.Info("credencial del buzon retirada de Dovecot", zap.String("username", username),
		zap.String("change", string(change)), zap.String("action", string(action)))
	return nil
}
