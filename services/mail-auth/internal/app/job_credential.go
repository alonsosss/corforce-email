package app

import (
	"context"
	"errors"
	"net/netip"

	"github.com/alonsosss/corforce-email/services/mail-auth/internal/domain"
	"go.uber.org/zap"
)

// verifyJobCredential decide si la contrasena es la credencial de destino de un trabajo de migracion y
// abre el buzon username. Abre solo si se cumplen todas: la funcion esta configurada, la peticion viene de
// la red del ejecutor, mail-migration confirma que el token esta vivo y es de ese buzon (trabajo en curso
// con el lease vigente), y el buzon existe todavia, es el mismo del trabajo y puede iniciar sesion. No mira
// los flags *_access: el trabajo escribe por IMAP en un buzon que el administrador puede haber cerrado a
// los clientes, y la credencial ya esta acotada por buzon, trabajo y red.
func (uc *UseCase) verifyJobCredential(ctx context.Context, req domain.VerifyRequest, username string, fields []zap.Field) (domain.Result, *domain.Mailbox) {
	if uc.jobs == nil || len(uc.jobNets) == 0 {
		uc.logger.Warn("mail-auth: credencial de trabajo con la funcion sin configurar, se deniega", fields...)
		return domain.ResultJobCredentialsDisabled, nil
	}
	if uc.throttle.Blocked(ctx, username, req.RemoteIP) {
		uc.logger.Warn("mail-auth: intento bloqueado por el freno de fuerza bruta", fields...)
		return domain.ResultThrottled, nil
	}
	if !uc.fromJobNetwork(req.RemoteIP) {
		uc.throttle.Failure(ctx, username, req.RemoteIP)
		uc.logger.Warn("mail-auth: credencial de trabajo usada fuera de la red del ejecutor, se deniega", fields...)
		return domain.ResultForbiddenNetwork, nil
	}

	cred, err := uc.jobs.Verify(ctx, req.Password, username)
	switch {
	case errors.Is(err, domain.ErrJobCredentialRejected):
		uc.throttle.Failure(ctx, username, req.RemoteIP)
		uc.logger.Info("mail-auth: credencial de trabajo rechazada", fields...)
		return domain.ResultBadPassword, nil
	case err != nil:
		uc.logger.Error("mail-auth: no se pudo verificar la credencial de trabajo", append(fields, zap.Error(err))...)
		return domain.ResultError, nil
	}

	mailbox, err := uc.repo.FindByUsername(ctx, username)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		uc.logger.Error("mail-auth: no se pudo leer el buzon", append(fields, zap.Error(err))...)
		return domain.ResultError, nil
	}
	// Un buzon borrado, o borrado y recreado con el mismo nombre, no es el del trabajo: se compara por id y
	// por empresa, no solo por nombre.
	if mailbox == nil || mailbox.ID != cred.MailboxID || mailbox.TenantID != cred.TenantID {
		uc.throttle.Failure(ctx, username, req.RemoteIP)
		uc.logger.Info("mail-auth: la credencial de trabajo no corresponde al buzon", append(fields, zap.String("job_id", cred.JobID.String()))...)
		return domain.ResultBadPassword, nil
	}
	if !mailbox.CanLogin() {
		uc.logger.Info("mail-auth: buzon sin inicio de sesion", append(fields, zap.Int16("active", mailbox.Active))...)
		return domain.ResultInactive, nil
	}

	uc.throttle.Success(ctx, username, req.RemoteIP)
	uc.recordSuccess(ctx, mailbox, nil, domain.ProtocolIMAP, req, append(fields, zap.String("job_id", cred.JobID.String())))
	return domain.ResultOK, mailbox
}

func (uc *UseCase) fromJobNetwork(remoteIP string) bool {
	addr, err := netip.ParseAddr(remoteIP)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range uc.jobNets {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
