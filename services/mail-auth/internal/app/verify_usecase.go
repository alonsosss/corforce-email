package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-auth/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Deps struct {
	Repo      ports.MailboxRepository
	Passwords ports.PasswordVerifier
	// Throttle puede ser nil: el servicio arranca sin freno cuando Redis no responde.
	Throttle ports.Throttle
	Metrics  ports.Metrics
	Logger   *zap.Logger
}

// UseCase verifica credenciales de buzon para Dovecot y consulta el historial de inicios.
type UseCase struct {
	repo      ports.MailboxRepository
	passwords ports.PasswordVerifier
	throttle  ports.Throttle
	metrics   ports.Metrics
	logger    *zap.Logger
}

func New(d Deps) *UseCase {
	throttle := d.Throttle
	if throttle == nil {
		throttle = noThrottle{}
	}
	return &UseCase{repo: d.Repo, passwords: d.Passwords, throttle: throttle, metrics: d.Metrics, logger: d.Logger}
}

// Verify decide si la credencial abre la sesion. El orden de las comprobaciones es
// parte de la seguridad: la contrasena se compara SIEMPRE (contra el hash del buzon o
// contra uno ficticio) antes de mirar el estado del buzon, de modo que un usuario
// inexistente, uno inactivo y uno sin el protocolo tardan lo mismo y reciben el mismo
// 401. Solo se distinguen en las metricas y en el log.
func (uc *UseCase) Verify(ctx context.Context, req domain.VerifyRequest) domain.Result {
	username := strings.ToLower(strings.TrimSpace(req.Username))
	fields := []zap.Field{zap.String("username", username), zap.String("remote_ip", req.RemoteIP), zap.String("service", req.Service)}

	protocol, known := domain.ProtocolFromService(req.Service)
	if !known {
		uc.logger.Warn("mail-auth: servicio desconocido, se deniega", fields...)
		return uc.finish(req.Service, domain.ResultUnknownService)
	}

	if uc.throttle.Blocked(ctx, username, req.RemoteIP) {
		uc.logger.Warn("mail-auth: intento bloqueado por el freno de fuerza bruta", fields...)
		return uc.finish(req.Service, domain.ResultThrottled)
	}

	mailbox, err := uc.repo.FindByUsername(ctx, username)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		uc.logger.Error("mail-auth: no se pudo leer el buzon", append(fields, zap.Error(err))...)
		return uc.finish(req.Service, domain.ResultError)
	}
	if mailbox == nil {
		uc.passwords.Verify(uc.passwords.DummyHash(), req.Password)
		uc.throttle.Failure(ctx, username, req.RemoteIP)
		uc.logger.Info("mail-auth: credencial rechazada", fields...)
		return uc.finish(req.Service, domain.ResultBadPassword)
	}

	appPassword, matched, err := uc.matchPassword(ctx, mailbox, protocol, req.Password)
	if err != nil {
		uc.logger.Error("mail-auth: no se pudieron leer las contrasenas de aplicacion", append(fields, zap.Error(err))...)
		return uc.finish(req.Service, domain.ResultError)
	}
	if !matched {
		uc.throttle.Failure(ctx, username, req.RemoteIP)
		uc.logger.Info("mail-auth: credencial rechazada", fields...)
		return uc.finish(req.Service, domain.ResultBadPassword)
	}

	// Una contrasena correcta sobre un buzon que no puede entrar no es fuerza bruta:
	// no alimenta el freno, pero tampoco abre nada.
	if !mailbox.CanLogin() {
		uc.logger.Info("mail-auth: buzon sin inicio de sesion", append(fields, zap.Int16("active", mailbox.Active))...)
		return uc.finish(req.Service, domain.ResultInactive)
	}
	if !mailbox.Access.Allows(protocol) {
		uc.logger.Info("mail-auth: protocolo deshabilitado para el buzon", append(fields, zap.String("protocol", string(protocol)))...)
		return uc.finish(req.Service, domain.ResultNoAccess)
	}

	uc.throttle.Success(ctx, username, req.RemoteIP)
	uc.recordSuccess(ctx, mailbox, appPassword, req, fields)
	return uc.finish(req.Service, domain.ResultOK)
}

// matchPassword prueba la contrasena principal y, si no coincide, las de aplicacion
// acotadas al protocolo. Devuelve la contrasena de aplicacion usada, o nil si entro
// con la principal.
func (uc *UseCase) matchPassword(ctx context.Context, mailbox *domain.Mailbox, protocol domain.Protocol, password string) (*domain.AppPassword, bool, error) {
	if uc.passwords.Verify(mailbox.PasswordHash, password) {
		return nil, true, nil
	}
	candidates, err := uc.repo.ListAppPasswords(ctx, mailbox.ID, protocol)
	if err != nil {
		return nil, false, err
	}
	for i := range candidates {
		if uc.passwords.Verify(candidates[i].PasswordHash, password) {
			return &candidates[i], true, nil
		}
	}
	return nil, false, nil
}

// recordSuccess deja rastro del inicio. Un fallo al escribirlo se registra pero no
// cierra la puerta: la credencial ya se comprobo y negar el acceso por un fallo del
// rastro convertiria una degradacion de la base en una caida del correo.
func (uc *UseCase) recordSuccess(ctx context.Context, mailbox *domain.Mailbox, appPassword *domain.AppPassword, req domain.VerifyRequest, fields []zap.Field) {
	login := domain.Login{
		TenantID: mailbox.TenantID,
		Username: mailbox.Username,
		Service:  strings.ToLower(strings.TrimSpace(req.Service)),
		RemoteIP: req.RemoteIP,
		LoggedAt: time.Now(),
	}
	if appPassword != nil {
		id := appPassword.ID
		login.AppPasswordID = &id
		if err := uc.repo.TouchAppPassword(ctx, id); err != nil {
			uc.logger.Error("mail-auth: no se pudo anotar el uso de la contrasena de aplicacion",
				append(fields, zap.String("app_password_id", id.String()), zap.Error(err))...)
		}
		fields = append(fields, zap.String("app_password", appPassword.Name))
	}
	if err := uc.repo.RecordLogin(ctx, login); err != nil {
		uc.logger.Error("mail-auth: no se pudo registrar el inicio de sesion", append(fields, zap.Error(err))...)
	}
	if mailbox.ForcePasswordUpdate {
		// No bloquea: el usuario necesita entrar al webmail para cambiarla.
		fields = append(fields, zap.Bool("force_pw_update", true))
	}
	uc.logger.Info("mail-auth: inicio de sesion", fields...)
}

func (uc *UseCase) finish(service string, result domain.Result) domain.Result {
	if uc.metrics != nil {
		uc.metrics.Attempt(service, result)
	}
	return result
}

// RecentLogins devuelve los ultimos inicios de un buzon de la empresa indicada.
func (uc *UseCase) RecentLogins(ctx context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.Login, error) {
	return uc.repo.RecentLogins(ctx, tenantID, strings.ToLower(strings.TrimSpace(username)), limit)
}

// noThrottle es el freno cuando no hay Redis. El servicio sigue autenticando porque el
// freno de red lo aporta netfilter a partir de los fallos que Dovecot escribe en el log;
// aqui solo se pierde la segunda capa.
type noThrottle struct{}

func (noThrottle) Blocked(context.Context, string, string) bool { return false }
func (noThrottle) Failure(context.Context, string, string)      {}
func (noThrottle) Success(context.Context, string, string)      {}
