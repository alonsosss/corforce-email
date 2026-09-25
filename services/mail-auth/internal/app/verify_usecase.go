package app

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
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
	// Jobs verifica las credenciales de trabajo de migracion (service "migration"). Sin el, o sin
	// JobNetworks, esas credenciales se deniegan: el comportamiento anterior a la funcion.
	Jobs ports.JobCredentialVerifier
	// JobNetworks son las redes desde las que se acepta una credencial de trabajo: la del ejecutor.
	JobNetworks []netip.Prefix
	// Now es el reloj del registro de inicios; nil usa time.Now.
	Now func() time.Time
}

// UseCase verifica credenciales de buzon para Dovecot y consulta el historial de inicios.
type UseCase struct {
	repo      ports.MailboxRepository
	passwords ports.PasswordVerifier
	throttle  ports.Throttle
	metrics   ports.Metrics
	logger    *zap.Logger
	now       func() time.Time
	logins    *loginDedupe
	jobs      ports.JobCredentialVerifier
	jobNets   []netip.Prefix
}

func New(d Deps) *UseCase {
	throttle := d.Throttle
	if throttle == nil {
		throttle = noThrottle{}
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &UseCase{repo: d.Repo, passwords: d.Passwords, throttle: throttle, metrics: d.Metrics, logger: d.Logger,
		now: now, logins: newLoginDedupe(loginDedupeEntries, loginDedupeWindow), jobs: d.Jobs, jobNets: d.JobNetworks}
}

// Verify decide si la credencial abre la sesion. El orden de las comprobaciones es
// parte de la seguridad: la contrasena se compara SIEMPRE (contra el hash del buzon o
// contra uno ficticio) antes de mirar el estado del buzon, de modo que un usuario
// inexistente, uno inactivo y uno sin el protocolo tardan lo mismo y reciben el mismo
// 401. Solo se distinguen en las metricas y en el log.
//
// Una contrasena que no es la principal cuesta siempre dos rondas de comparacion, exista
// o no el buzon y tenga o no contrasenas de aplicacion: la segunda ronda compara todas las
// de aplicacion a la vez (o un hash ficticio si no hay ninguna). Si el buzon que existe con
// contrasenas de aplicacion tardara mas que el que no existe, el tiempo de respuesta
// diria que direcciones existen.
func (uc *UseCase) Verify(ctx context.Context, req domain.VerifyRequest) domain.Result {
	return uc.Authenticate(ctx, req).Result
}

// Authenticate es Verify con la identidad del buzon, que solo sale si la credencial abre la
// sesion.
func (uc *UseCase) Authenticate(ctx context.Context, req domain.VerifyRequest) domain.Verification {
	result, mailbox := uc.verify(ctx, req)
	v := domain.Verification{Result: uc.finish(req.Service, result)}
	if result.Authorized() && mailbox != nil {
		v.DisplayName = mailbox.DisplayName
		v.Username = mailbox.Username
		v.TenantID = mailbox.TenantID
		v.MailboxID = mailbox.ID
		v.MFARequired = mailbox.MFAEnabled
	}
	return v
}

func (uc *UseCase) verify(ctx context.Context, req domain.VerifyRequest) (domain.Result, *domain.Mailbox) {
	username := strings.ToLower(strings.TrimSpace(req.Username))
	fields := []zap.Field{zap.String("username", username), zap.String("remote_ip", req.RemoteIP), zap.String("service", req.Service)}

	if domain.IsJobCredentialService(req.Service) {
		return uc.verifyJobCredential(ctx, req, username, fields)
	}
	protocol, known := domain.ProtocolFromService(req.Service)
	if !known {
		uc.logger.Warn("mail-auth: servicio desconocido, se deniega", fields...)
		return domain.ResultUnknownService, nil
	}

	if uc.throttle.Blocked(ctx, username, req.RemoteIP) {
		uc.logger.Warn("mail-auth: intento bloqueado por el freno de fuerza bruta", fields...)
		return domain.ResultThrottled, nil
	}

	mailbox, err := uc.repo.FindByUsername(ctx, username)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		uc.logger.Error("mail-auth: no se pudo leer el buzon", append(fields, zap.Error(err))...)
		return domain.ResultError, nil
	}
	if mailbox == nil {
		uc.passwords.Verify(uc.passwords.DummyHash(), req.Password)
		uc.passwords.Verify(uc.passwords.DummyHash(), req.Password)
		uc.throttle.Failure(ctx, username, req.RemoteIP)
		uc.logger.Info("mail-auth: credencial rechazada", fields...)
		return domain.ResultBadPassword, nil
	}

	appPassword, matched, err := uc.matchPassword(ctx, mailbox, protocol, req.Password)
	if err != nil {
		uc.logger.Error("mail-auth: no se pudieron leer las contrasenas de aplicacion", append(fields, zap.Error(err))...)
		return domain.ResultError, nil
	}
	if !matched {
		uc.throttle.Failure(ctx, username, req.RemoteIP)
		uc.logger.Info("mail-auth: credencial rechazada", fields...)
		return domain.ResultBadPassword, nil
	}

	// Una contrasena correcta sobre un buzon que no puede entrar no es fuerza bruta:
	// no alimenta el freno, pero tampoco abre nada.
	if !mailbox.CanLogin() {
		uc.logger.Info("mail-auth: buzon sin inicio de sesion", append(fields, zap.Int16("active", mailbox.Active))...)
		return domain.ResultInactive, nil
	}
	if !mailbox.Access.Allows(protocol) {
		uc.logger.Info("mail-auth: protocolo deshabilitado para el buzon", append(fields, zap.String("protocol", string(protocol)))...)
		return domain.ResultNoAccess, nil
	}
	// Con verificacion en dos pasos la contrasena principal solo abre el webmail, que pide el codigo;
	// por cualquier otro protocolo se saltaria el segundo factor. Como un buzon que no puede entrar, no
	// alimenta el freno: la contrasena es correcta.
	if mailbox.MFAEnabled && appPassword == nil && protocol != domain.ProtocolWebmail {
		uc.logger.Info("mail-auth: contrasena principal con verificacion en dos pasos; hace falta una contrasena de aplicacion", fields...)
		return domain.ResultMFAAppPasswordRequired, nil
	}

	uc.throttle.Success(ctx, username, req.RemoteIP)
	uc.recordSuccess(ctx, mailbox, appPassword, protocol, req, fields)
	return domain.ResultOK, mailbox
}

// matchPassword prueba la contrasena principal y, si no coincide y el protocolo las
// admite, las de aplicacion acotadas al protocolo. Devuelve la contrasena de aplicacion
// usada, o nil si entro con la principal.
func (uc *UseCase) matchPassword(ctx context.Context, mailbox *domain.Mailbox, protocol domain.Protocol, password string) (*domain.AppPassword, bool, error) {
	if uc.passwords.Verify(mailbox.PasswordHash, password) {
		return nil, true, nil
	}
	var candidates []domain.AppPassword
	if protocol.AcceptsAppPasswords() {
		var err error
		if candidates, err = uc.repo.ListAppPasswords(ctx, mailbox.ID, protocol); err != nil {
			return nil, false, err
		}
	}
	if matched := uc.matchAppPasswords(candidates, password); matched != nil {
		return matched, true, nil
	}
	return nil, false, nil
}

// matchAppPasswords es la segunda ronda: compara la contrasena con todas las candidatas a la vez y
// espera a todas, de modo que la ronda dura una comparacion tenga el buzon una o varias. Sin
// candidatas (o si el protocolo no las admite) compara contra el hash ficticio, para que la ronda
// exista igual.
func (uc *UseCase) matchAppPasswords(candidates []domain.AppPassword, password string) *domain.AppPassword {
	if len(candidates) == 0 {
		uc.passwords.Verify(uc.passwords.DummyHash(), password)
		return nil
	}
	matched := make([]bool, len(candidates))
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			matched[i] = uc.passwords.Verify(candidates[i].PasswordHash, password)
		}(i)
	}
	wg.Wait()
	for i, ok := range matched {
		if ok {
			return &candidates[i]
		}
	}
	return nil
}

// recordSuccess deja rastro del inicio. Un fallo al escribirlo se registra pero no
// cierra la puerta: la credencial ya se comprobo y negar el acceso por un fallo del
// rastro convertiria una degradacion de la base en una caida del correo. Un protocolo
// que verifica cada peticion deja un registro por cliente y ventana, no uno por peticion.
func (uc *UseCase) recordSuccess(ctx context.Context, mailbox *domain.Mailbox, appPassword *domain.AppPassword, protocol domain.Protocol, req domain.VerifyRequest, fields []zap.Field) {
	login := domain.Login{
		TenantID: mailbox.TenantID,
		Username: mailbox.Username,
		Service:  strings.ToLower(strings.TrimSpace(req.Service)),
		RemoteIP: req.RemoteIP,
		LoggedAt: uc.now(),
	}
	if protocol.AuthenticatesEachRequest() {
		client := mailbox.ID.String() + "|" + login.Service + "|" + req.RemoteIP + "|"
		if appPassword != nil {
			client += appPassword.ID.String()
		}
		if !uc.logins.firstInWindow(client, login.LoggedAt) {
			return
		}
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
