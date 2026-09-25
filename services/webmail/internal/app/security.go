package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

// maxSecretBytes acota el secreto TOTP que devuelve el cliente al activar: uno de 160 bits en base32
// son 32 caracteres.
const maxSecretBytes = 128

// Security devuelve el estado de la verificacion en dos pasos y las contrasenas de aplicacion.
func (s *Service) Security(ctx context.Context, sess domain.Session) (domain.SecurityOverview, error) {
	status, err := s.security.MFAStatus(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudo leer la verificacion en dos pasos", sess, err)
		return domain.SecurityOverview{}, unavailable(err)
	}
	list, err := s.security.AppPasswords(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudieron leer las contrasenas de aplicacion", sess, err)
		return domain.SecurityOverview{}, unavailable(err)
	}
	return domain.SecurityOverview{MFA: status, AppPasswords: list}, nil
}

// PrepareMFA comprueba la contrasena actual y prepara un secreto nuevo sin guardarlo en el
// directorio. Solo su hash queda en el almacen del webmail, ligado al buzon y por MFASetupTTL:
// ActivateMFA solo acepta ese secreto, asi que activar exige de verdad la contrasena (una sesion
// robada no puede activar su propio secreto y dejar fuera al dueno).
func (s *Service) PrepareMFA(ctx context.Context, sess domain.Session, currentPassword, remoteIP string) (domain.MFASetup, error) {
	id, err := s.checkPassword(ctx, sess, currentPassword, remoteIP)
	if err != nil {
		return domain.MFASetup{}, err
	}
	if id.MFARequired {
		return domain.MFASetup{}, domain.ErrMFAAlreadyEnabled
	}
	secret, err := s.totp.NewSecret()
	if err != nil {
		return domain.MFASetup{}, unavailable(err)
	}
	if err := s.mfaChallenges.SaveSetup(ctx, sess.Username, secretHash(secret), s.cfg.MFASetupTTL); err != nil {
		s.logFailure("no se pudo guardar la preparacion de la verificacion", sess, err)
		return domain.MFASetup{}, unavailable(err)
	}
	return domain.MFASetup{Secret: secret, ProvisioningURI: s.totp.ProvisioningURI(secret, sess.Username)}, nil
}

// ActivateMFA activa la verificacion en dos pasos con el secreto preparado y un codigo de la
// aplicacion. Devuelve los codigos de recuperacion, que no se vuelven a mostrar. Un codigo que no
// vale no consume la preparacion: el usuario puede volver a intentarlo mientras dure.
func (s *Service) ActivateMFA(ctx context.Context, sess domain.Session, rawSecret, rawCode string) ([]string, error) {
	secret := strings.ToUpper(strings.TrimSpace(rawSecret))
	if secret == "" || len(secret) > maxSecretBytes {
		return nil, domain.NewValidationError("secret", "no es un secreto preparado")
	}
	code, ok := domain.NormalizeMFACode(rawCode)
	if !ok {
		return nil, domain.ErrInvalidMFACode
	}
	stored, err := s.mfaChallenges.SetupHash(ctx, sess.Username)
	if err != nil {
		if errors.Is(err, domain.ErrMFASetupExpired) {
			return nil, err
		}
		return nil, unavailable(err)
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(secretHash(secret))) != 1 {
		return nil, domain.ErrMFASetupExpired
	}
	codes, err := s.security.ActivateMFA(ctx, sess.Username, secret, code)
	if err != nil {
		return nil, s.securityError("no se pudo activar la verificacion en dos pasos", sess, err)
	}
	if err := s.mfaChallenges.DeleteSetup(ctx, sess.Username); err != nil {
		// La preparacion caduca sola y el directorio ya no admite otra activacion.
		s.logger.Warn("webmail: no se pudo borrar la preparacion de la verificacion", zap.String("username", sess.Username), zap.Error(err))
	}
	s.logger.Info("webmail: verificacion en dos pasos activada", zap.String("username", sess.Username))
	return codes, nil
}

// RegenerateRecoveryCodes sustituye los codigos de recuperacion; exige un codigo valido.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, sess domain.Session, rawCode string) ([]string, error) {
	code, err := requiredCode(rawCode)
	if err != nil {
		return nil, err
	}
	codes, err := s.security.RegenerateRecoveryCodes(ctx, sess.Username, code)
	if err != nil {
		return nil, s.securityError("no se pudieron regenerar los codigos de recuperacion", sess, err)
	}
	s.logger.Info("webmail: codigos de recuperacion regenerados", zap.String("username", sess.Username))
	return codes, nil
}

// DisableMFA desactiva la verificacion en dos pasos: exige la contrasena actual (mail-auth) y un
// codigo, que valida mail-directory en la misma llamada que desactiva. Validarlo antes por separado
// lo gastaria: cada codigo vale una sola vez.
func (s *Service) DisableMFA(ctx context.Context, sess domain.Session, reauth domain.Reauthentication, remoteIP string) error {
	id, err := s.checkPassword(ctx, sess, reauth.CurrentPassword, remoteIP)
	if err != nil {
		return err
	}
	if !id.MFARequired {
		return domain.ErrMFANotEnabled
	}
	code, err := requiredCode(reauth.Code)
	if err != nil {
		return err
	}
	if err := s.security.DisableMFA(ctx, sess.Username, code); err != nil {
		return s.securityError("no se pudo desactivar la verificacion en dos pasos", sess, err)
	}
	s.logger.Info("webmail: verificacion en dos pasos desactivada", zap.String("username", sess.Username), zap.String("remote_ip", remoteIP))
	return nil
}

// CreateAppPassword crea una contrasena de aplicacion. Exige siempre la contrasena actual y, con
// verificacion en dos pasos, un codigo: una sesion robada no debe poder dejarse un acceso IMAP
// permanente.
func (s *Service) CreateAppPassword(ctx context.Context, sess domain.Session, in domain.AppPasswordInput, reauth domain.Reauthentication, remoteIP string) (domain.CreatedAppPassword, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return domain.CreatedAppPassword{}, domain.NewValidationError("name", "es obligatorio")
	}
	if !in.Access.Any() {
		return domain.CreatedAppPassword{}, domain.NewValidationError("protocols", "elige al menos un protocolo")
	}
	if err := s.reauthenticate(ctx, sess, reauth, remoteIP); err != nil {
		return domain.CreatedAppPassword{}, err
	}
	created, err := s.security.CreateAppPassword(ctx, sess.Username, in)
	if err != nil {
		return domain.CreatedAppPassword{}, s.securityError("no se pudo crear la contrasena de aplicacion", sess, err)
	}
	s.logger.Info("webmail: contrasena de aplicacion creada", zap.String("username", sess.Username),
		zap.String("id", created.ID), zap.String("remote_ip", remoteIP))
	return created, nil
}

// DeleteAppPassword borra una contrasena de aplicacion del buzon. Quitar un acceso no exige
// reautenticacion.
func (s *Service) DeleteAppPassword(ctx context.Context, sess domain.Session, id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if !domain.ValidUUID(id) {
		return domain.ErrAppPasswordNotFound
	}
	if err := s.security.DeleteAppPassword(ctx, sess.Username, id); err != nil {
		return s.securityError("no se pudo borrar la contrasena de aplicacion", sess, err)
	}
	s.logger.Info("webmail: contrasena de aplicacion borrada", zap.String("username", sess.Username), zap.String("id", id))
	return nil
}

// reauthenticate confirma la identidad del usuario de la sesion antes de una accion sensible: la
// contrasena actual contra mail-auth (con la IP real para su freno de fuerza bruta) y, si el buzon
// tiene la verificacion en dos pasos activa (mail-auth lo dice en la misma respuesta), un codigo.
func (s *Service) reauthenticate(ctx context.Context, sess domain.Session, reauth domain.Reauthentication, remoteIP string) error {
	id, err := s.checkPassword(ctx, sess, reauth.CurrentPassword, remoteIP)
	if err != nil {
		return err
	}
	if !id.MFARequired {
		return nil
	}
	code, err := requiredCode(reauth.Code)
	if err != nil {
		return err
	}
	_, err = s.security.VerifyMFA(ctx, sess.Username, code)
	switch {
	case err == nil, errors.Is(err, domain.ErrMFANotEnabled):
		// Restablecida entre la comprobacion de la contrasena y la del codigo: basta la contrasena.
		return nil
	case errors.Is(err, domain.ErrInvalidMFACode):
		s.logger.Info("webmail: reautenticacion con codigo incorrecto", zap.String("username", sess.Username), zap.String("remote_ip", remoteIP))
		return err
	default:
		s.logFailure("no se pudo validar el codigo de verificacion", sess, err)
		return unavailable(err)
	}
}

// checkPassword comprueba la contrasena actual del buzon de la sesion. Una incorrecta es
// domain.ErrInvalidCredentials, igual que en el cambio de contrasena. Con verificacion en dos pasos
// mail-auth responde exito con MFARequired: la contrasena es correcta.
func (s *Service) checkPassword(ctx context.Context, sess domain.Session, password, remoteIP string) (domain.Identity, error) {
	if password == "" || len(password) > maxPasswordBytes {
		return domain.Identity{}, domain.ErrInvalidCredentials
	}
	id, err := s.auth.Verify(ctx, sess.Username, password, remoteIP)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			s.logger.Info("webmail: contrasena actual incorrecta", zap.String("username", sess.Username), zap.String("remote_ip", remoteIP))
			return domain.Identity{}, domain.ErrInvalidCredentials
		}
		s.logFailure("no se pudo verificar la contrasena actual", sess, err)
		return domain.Identity{}, unavailable(err)
	}
	return id, nil
}

// requiredCode exige un codigo en una accion que lo necesita: sin el, domain.ErrMFARequired; con
// una forma imposible, domain.ErrInvalidMFACode.
func requiredCode(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", domain.ErrMFARequired
	}
	code, ok := domain.NormalizeMFACode(raw)
	if !ok {
		return "", domain.ErrInvalidMFACode
	}
	return code, nil
}

// securityError deja pasar los rechazos del directorio que el usuario puede corregir y convierte lo
// demas en indisponibilidad.
func (s *Service) securityError(msg string, sess domain.Session, err error) error {
	for _, known := range []error{domain.ErrInvalidMFACode, domain.ErrMFAAlreadyEnabled, domain.ErrMFANotEnabled,
		domain.ErrAppPasswordNotFound, domain.ErrAppPasswordLimit} {
		if errors.Is(err, known) {
			return err
		}
	}
	return s.directoryError(msg, sess, err)
}

// secretHash es lo que el almacen guarda de un secreto preparado: nunca el secreto.
func secretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
