package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Verificacion en dos pasos del buzon (docs/Plan_Webmail_Seguridad.md). El webmail comprueba antes la
// contrasena del buzon con mail-auth; aqui se guarda el secreto, se comprueban los codigos y se lleva
// mail.mailboxes.mfa_enabled, que es lo que mail-auth lee. El secreto no sale nunca de este servicio.

// errMFAUnwired: main no cableo la verificacion en dos pasos. Es un fallo de despliegue.
var errMFAUnwired = errors.New("mail-directory: verificación en dos pasos sin cablear")

func (uc *UseCase) mfaWired() error {
	if uc.mfa == nil || uc.sealer == nil || uc.totp == nil {
		return errMFAUnwired
	}
	return nil
}

// MFAStatusByUsername devuelve el estado de la verificacion del buzon del webmail.
func (uc *UseCase) MFAStatusByUsername(ctx context.Context, username string) (domain.MFAStatus, error) {
	if err := uc.mfaWired(); err != nil {
		return domain.MFAStatus{}, err
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return domain.MFAStatus{}, err
	}
	var out domain.MFAStatus
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mfa.Get(ctx, tenantID, mailboxID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		out = m.Status()
		return nil
	})
	return out, err
}

// ActivateMFAByUsername activa la verificacion con el secreto que el webmail genero en su paso de
// preparacion y el primer codigo de la aplicacion del usuario, que demuestra que la aplicacion lo
// tiene. Hasta aqui nada se guardo. Devuelve los codigos de recuperacion en claro, una unica vez.
// El paso del codigo de activacion queda como el ultimo usado: no vale despues como segundo paso.
func (uc *UseCase) ActivateMFAByUsername(ctx context.Context, username, rawSecret, rawCode string) ([]string, error) {
	if err := uc.mfaWired(); err != nil {
		return nil, err
	}
	secret, err := domain.NormalizeTOTPSecret(rawSecret)
	if err != nil {
		return nil, err
	}
	code := domain.NormalizeMFACode(rawCode)
	if !domain.IsTOTPCode(code) {
		return nil, domain.ErrInvalidMFACode
	}
	step, ok := uc.totp.ValidateStep(secret, code, uc.now())
	if !ok {
		return nil, domain.ErrInvalidMFACode
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	codes, hashes, err := uc.newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	sealed, err := uc.sealer.Seal([]byte(secret), domain.MFASecretAAD(mailboxID))
	if err != nil {
		return nil, err
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		if m.MFAEnabled {
			return domain.ErrMFAAlreadyEnabled
		}
		now := uc.now().UTC()
		err = uc.mfa.Create(ctx, &domain.MailboxMFA{
			MailboxID: mailboxID, TenantID: tenantID, SecretEnc: sealed, RecoveryHashes: hashes, LastStep: step, EnabledAt: now,
		})
		if errors.Is(err, domain.ErrAlreadyExists) {
			return domain.ErrMFAAlreadyEnabled
		}
		if err != nil {
			return err
		}
		if err := uc.mailboxes.SetMFAEnabled(ctx, tenantID, mailboxID, true); err != nil {
			return err
		}
		m.MFAEnabled = true
		return uc.events.MailboxMFAEnabled(ctx, m, now)
	})
	if err != nil {
		return nil, err
	}
	uc.logger.Info("mail-directory: verificación en dos pasos activada",
		zap.String("tenant_id", tenantID.String()), zap.String("mailbox_id", mailboxID.String()))
	return codes, nil
}

// VerifyMFAByUsername comprueba un codigo del buzon: uno TOTP que no se haya usado antes o uno de
// recuperacion, que se gasta.
func (uc *UseCase) VerifyMFAByUsername(ctx context.Context, username, code string) (domain.MFAVerification, error) {
	var out domain.MFAVerification
	err := uc.withMFACode(ctx, username, code, func(ctx context.Context, _ *domain.Mailbox, v domain.MFAVerification) error {
		out = v
		return nil
	})
	return out, err
}

// RegenerateRecoveryCodesByUsername sustituye los codigos de recuperacion del buzon por otros nuevos
// tras comprobar un codigo, y los devuelve en claro una unica vez.
func (uc *UseCase) RegenerateRecoveryCodesByUsername(ctx context.Context, username, code string) ([]string, error) {
	if err := uc.mfaWired(); err != nil {
		return nil, err
	}
	codes, hashes, err := uc.newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	err = uc.withMFACode(ctx, username, code, func(ctx context.Context, m *domain.Mailbox, _ domain.MFAVerification) error {
		return uc.mfa.ReplaceRecoveryCodes(ctx, m.TenantID, m.ID, hashes)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableMFAByUsername apaga la verificacion del buzon tras comprobar un codigo. El webmail ya
// comprobo la contrasena.
func (uc *UseCase) DisableMFAByUsername(ctx context.Context, username, code string) error {
	return uc.withMFACode(ctx, username, code, func(ctx context.Context, m *domain.Mailbox, _ domain.MFAVerification) error {
		return uc.disableMFA(ctx, m, domain.MFADisabledByUser, nil)
	})
}

// ResetMailboxMFA es el restablecimiento del administrador: el titular perdio su aplicacion y sus
// codigos. Apaga la verificacion y anuncia el cambio de credencial, con el que el webmail cierra las
// sesiones del buzon. actorID es el usuario de la plataforma que lo pide.
func (uc *UseCase) ResetMailboxMFA(ctx context.Context, tenantID, mailboxID, actorID uuid.UUID) error {
	if err := uc.mfaWired(); err != nil {
		return err
	}
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		var actor *uuid.UUID
		if actorID != uuid.Nil {
			actor = &actorID
		}
		if err := uc.disableMFA(ctx, m, domain.MFADisabledByAdmin, actor); err != nil {
			return err
		}
		return uc.events.MailboxCredentialsChanged(ctx, m, domain.CredentialMFA, []domain.MailboxAttr{domain.AttrMFA})
	})
	if err != nil {
		return err
	}
	uc.logger.Info("mail-directory: verificación en dos pasos restablecida por el administrador",
		zap.String("tenant_id", tenantID.String()), zap.String("mailbox_id", mailboxID.String()),
		zap.String("actor_id", actorID.String()))
	return nil
}

// disableMFA borra la fila y apaga mfa_enabled en la transaccion en curso. Un buzon sin verificacion
// es ErrMFANotEnabled.
func (uc *UseCase) disableMFA(ctx context.Context, m *domain.Mailbox, by string, actorID *uuid.UUID) error {
	deleted, err := uc.mfa.Delete(ctx, m.TenantID, m.ID)
	if err != nil {
		return err
	}
	if !deleted && !m.MFAEnabled {
		return domain.ErrMFANotEnabled
	}
	if err := uc.mailboxes.SetMFAEnabled(ctx, m.TenantID, m.ID, false); err != nil {
		return err
	}
	m.MFAEnabled = false
	return uc.events.MailboxMFADisabled(ctx, m, uc.now().UTC(), by, actorID)
}

// withMFACode comprueba el codigo del buzon del webmail y, si vale, corre fn en la misma transaccion.
// Un codigo que no vale deshace todo: tampoco se gasta el de recuperacion.
func (uc *UseCase) withMFACode(ctx context.Context, username, rawCode string, fn func(ctx context.Context, m *domain.Mailbox, v domain.MFAVerification) error) error {
	if err := uc.mfaWired(); err != nil {
		return err
	}
	code := domain.NormalizeMFACode(rawCode)
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return err
	}
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		v, err := uc.checkMFACode(ctx, m, code)
		if err != nil {
			return err
		}
		return fn(ctx, m, v)
	})
}

// checkMFACode acepta un codigo TOTP de un paso posterior al ultimo aceptado (y lo registra) o un
// codigo de recuperacion que quede (y lo gasta). Lo que no tiene la forma de ninguno no se consulta.
func (uc *UseCase) checkMFACode(ctx context.Context, m *domain.Mailbox, code string) (domain.MFAVerification, error) {
	state, err := uc.mfa.Get(ctx, m.TenantID, m.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.MFAVerification{}, domain.ErrMFANotEnabled
	}
	if err != nil {
		return domain.MFAVerification{}, err
	}
	switch {
	case domain.IsTOTPCode(code):
		secret, err := uc.sealer.Open(state.SecretEnc, domain.MFASecretAAD(m.ID))
		if err != nil {
			return domain.MFAVerification{}, err
		}
		step, ok := uc.totp.ValidateStep(string(secret), code, uc.now())
		if !ok || step <= state.LastStep {
			return domain.MFAVerification{}, domain.ErrInvalidMFACode
		}
		advanced, err := uc.mfa.AdvanceStep(ctx, m.TenantID, m.ID, step)
		if err != nil {
			return domain.MFAVerification{}, err
		}
		if !advanced {
			return domain.MFAVerification{}, domain.ErrInvalidMFACode
		}
		return domain.MFAVerification{Method: domain.MFAMethodTOTP, RecoveryRemaining: len(state.RecoveryHashes)}, nil
	case domain.IsRecoveryCode(code):
		remaining, ok, err := uc.mfa.ConsumeRecoveryCode(ctx, m.TenantID, m.ID, domain.HashRecoveryCode(code))
		if err != nil {
			return domain.MFAVerification{}, err
		}
		if !ok {
			return domain.MFAVerification{}, domain.ErrInvalidMFACode
		}
		uc.logger.Info("mail-directory: código de recuperación usado",
			zap.String("tenant_id", m.TenantID.String()), zap.String("mailbox_id", m.ID.String()), zap.Int("remaining", remaining))
		return domain.MFAVerification{Method: domain.MFAMethodRecovery, RecoveryRemaining: remaining}, nil
	default:
		return domain.MFAVerification{}, domain.ErrInvalidMFACode
	}
}

// newRecoveryCodes genera los codigos de recuperacion: en claro con guion para mostrarlos una vez, y
// sus hashes para guardarlos.
func (uc *UseCase) newRecoveryCodes() (codes, hashes []string, err error) {
	codes = make([]string, 0, domain.RecoveryCodeCount)
	hashes = make([]string, 0, domain.RecoveryCodeCount)
	seen := make(map[string]bool, domain.RecoveryCodeCount)
	for attempt := 0; len(codes) < domain.RecoveryCodeCount; attempt++ {
		if attempt >= 4*domain.RecoveryCodeCount {
			return nil, nil, errors.New("mail-directory: el generador repite códigos de recuperación")
		}
		code, err := uc.secrets.GenerateRecoveryCode()
		if err != nil {
			return nil, nil, err
		}
		if !domain.IsRecoveryCode(code) {
			return nil, nil, errors.New("mail-directory: código de recuperación generado con otra forma")
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, domain.FormatRecoveryCode(code))
		hashes = append(hashes, domain.HashRecoveryCode(code))
	}
	return codes, hashes, nil
}
