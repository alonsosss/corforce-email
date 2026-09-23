package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Identidades de Amazon SES. Un dominio que envia por SES (purpose sending o both) se da de alta en
// SES al verificarse, firmando con la misma clave DKIM y el mismo selector que los motores (BYODKIM),
// con su subdominio MAIL FROM y el conjunto transaccional como conjunto por defecto. Cada verificacion
// y cada barrido lo vuelven a dejar como debe estar y guardan lo que SES dice de el: solo con
// VerifiedForSendingStatus llega a transactional como apto para enviar.

// errSESNoIdentity: el dominio no tiene identidad en SES y no toca crearla (no esta verificado).
var errSESNoIdentity = errors.New("sin identidad en SES")

// sesEnabled dice si el servicio se cableo con la integracion de SES (credenciales presentes).
func (uc *UseCase) sesEnabled() bool {
	return uc.ses != nil && uc.sendingEvents != nil
}

// sendingReady dice si el dominio puede enviar por SES: verificado aqui, de envio y verificado en SES.
func sendingReady(d *domain.Domain) bool {
	return d.Status == domain.StatusVerified && d.Purpose.IncludesSending() && d.SES.VerifiedForSending()
}

// sendingReadiness es lo que los eventos del dominio dicen de su aptitud para enviar: nil con la
// integracion desactivada, porque entonces domain-service no sabe lo que SES acepta.
func (uc *UseCase) sendingReadiness(d *domain.Domain) *bool {
	if !uc.sesEnabled() {
		return nil
	}
	ready := sendingReady(d)
	return &ready
}

// sesSigningKey es la clave con la que SES debe firmar: la misma que firma en los motores. Mientras el
// TXT de la clave actual no se haya visto publicado y quede la anterior en gracia, la anterior; SES
// con un selector sin TXT dejaria la identidad pendiente y el dominio sin poder enviar. Tras una
// revocacion no queda anterior, asi que la clave revocada sale de SES al momento.
func (uc *UseCase) sesSigningKey(d *domain.Domain) (ports.DKIMKey, error) {
	enc, selector := d.DKIMPrivateKeyEnc, d.DKIMSelector
	if d.HasPreviousDKIM() && d.DKIMConfirmedAt == nil {
		enc, selector = d.DKIMPreviousPrivateKeyEnc, d.DKIMPreviousSelector
	}
	pem, err := uc.cipher.Decrypt(enc)
	if err != nil {
		return ports.DKIMKey{}, fmt.Errorf("descifrar clave DKIM de %s: %w", d.Domain, err)
	}
	return ports.DKIMKey{Selector: selector, PrivateKeyPEM: string(pem)}, nil
}

// syncSES deja la identidad del dominio en SES como debe estar y guarda lo que SES dice de ella, con el
// cerrojo de claves del dominio y su fila leida dentro: una rotacion o una revocacion simultanea no
// puede devolver a SES una clave que ya no es la suya. Solo un dominio verificado crea la identidad;
// uno que no lo esta solo la mantiene si ya existe. Si la aptitud para enviar cambia (o es la primera
// vez que se sabe), el evento se encola en la misma transaccion que el estado. Un fallo de SES se
// guarda como ultimo error, no deshace nada y se devuelve: el barrido lo repite. Deja en seen el
// estado nuevo.
func (uc *UseCase) syncSES(ctx context.Context, seen *domain.Domain) error {
	if !uc.sesEnabled() {
		return nil
	}
	var syncErr error
	err := uc.repo.WithDKIMLock(ctx, seen.TenantID, seen.ID, func(ctx context.Context, d *domain.Domain) error {
		if !d.Purpose.IncludesSending() {
			return nil
		}
		wasReady, known := sendingReady(d), d.SES.Checked()
		obs, err := uc.reconcileSESIdentity(ctx, d, d.Status == domain.StatusVerified)
		switch {
		case errors.Is(err, errSESNoIdentity):
			if d.SES == (domain.SESState{}) {
				return nil
			}
			d.SES = domain.SESState{}
		case err != nil:
			syncErr = err
			d.SES.LastError = domain.TruncateSESError(err.Error())
			if err := uc.repo.SaveSESState(ctx, d); err != nil {
				return err
			}
			seen.SES = d.SES
			return nil
		default:
			d.SES = obs.State(uc.now())
		}
		if err := uc.repo.SaveSESState(ctx, d); err != nil {
			return err
		}
		if ready := sendingReady(d); ready != wasReady || !known {
			if err := uc.sendingEvents.SendingStatusChanged(ctx, d, ready); err != nil {
				return fmt.Errorf("encolar domains.domain.sending_status_changed: %w", err)
			}
		}
		seen.SES = d.SES
		return nil
	})
	if err != nil {
		return err
	}
	return syncErr
}

// reconcileSESIdentity crea la identidad si falta (y create lo permite) y corrige lo que difiera: la
// clave DKIM, el MAIL FROM y el conjunto por defecto. Devuelve lo que SES dice de ella al terminar.
// Una identidad de otra empresa no se toca. Una creada a mano con las claves de SES (Easy DKIM) que
// ya envia conserva su DKIM: pasarla a BYODKIM la devolveria a pendiente y cortaria sus envios.
func (uc *UseCase) reconcileSESIdentity(ctx context.Context, d *domain.Domain, create bool) (domain.SESIdentityObservation, error) {
	key, err := uc.sesSigningKey(d)
	if err != nil {
		return domain.SESIdentityObservation{}, err
	}
	obs, err := uc.ses.GetIdentity(ctx, d.Domain)
	if errors.Is(err, domain.ErrSESIdentityNotFound) {
		if !create {
			return obs, errSESNoIdentity
		}
		if err := uc.ses.CreateIdentity(ctx, d.TenantID, d.Domain, key, uc.sesConfigSet); err != nil && !errors.Is(err, domain.ErrSESIdentityExists) {
			return obs, fmt.Errorf("crear la identidad en SES: %w", err)
		}
		obs, err = uc.ses.GetIdentity(ctx, d.Domain)
	}
	if err != nil {
		return obs, fmt.Errorf("leer la identidad en SES: %w", err)
	}
	changed := false
	switch {
	case obs.OwnedBy(d.TenantID):
	case obs.Adoptable(uc.sesConfigSet):
		if err := uc.ses.TagIdentity(ctx, d.TenantID, d.Domain); err != nil {
			return obs, fmt.Errorf("etiquetar la identidad adoptada en SES: %w", err)
		}
		uc.logger.Info("identidad de SES creada a mano adoptada por la empresa",
			zap.String("domain", d.Domain), zap.String("tenant_id", d.TenantID.String()))
		changed = true
	default:
		return obs, domain.ErrSESIdentityOwnedElsewhere
	}

	if !obs.SignsWith(key.Selector) {
		if obs.DKIMOrigin == domain.SESDKIMOriginExternal || !obs.VerifiedForSending {
			if err := uc.ses.SetDKIMKey(ctx, d.Domain, key); err != nil {
				return obs, fmt.Errorf("fijar la clave DKIM en SES: %w", err)
			}
			changed = true
		} else {
			uc.logger.Info("la identidad de SES firma con sus propias claves (Easy DKIM) y ya envia; se conserva",
				zap.String("domain", d.Domain), zap.String("tenant_id", d.TenantID.String()))
		}
	}
	mailFrom := domain.SESMailFromDomain(d.Domain)
	if obs.MailFromDomain != mailFrom || obs.BehaviorOnMXFailure != domain.SESBehaviorOnMXFailure {
		if err := uc.ses.SetMailFrom(ctx, d.Domain, mailFrom); err != nil {
			return obs, fmt.Errorf("fijar el MAIL FROM en SES: %w", err)
		}
		changed = true
	}
	if obs.ConfigurationSet != uc.sesConfigSet {
		if err := uc.ses.SetConfigurationSet(ctx, d.Domain, uc.sesConfigSet); err != nil {
			return obs, fmt.Errorf("fijar el conjunto por defecto en SES: %w", err)
		}
		changed = true
	}
	if changed {
		if obs, err = uc.ses.GetIdentity(ctx, d.Domain); err != nil {
			return obs, fmt.Errorf("leer la identidad en SES: %w", err)
		}
	}
	return obs, nil
}

// retireFromSES borra la identidad del dominio en SES solo si lleva la etiqueta de la empresa: una sin
// etiqueta puede ser de otro proyecto de la cuenta. No falla si SES ya no la tiene.
func (uc *UseCase) retireFromSES(ctx context.Context, d *domain.Domain) error {
	obs, err := uc.ses.GetIdentity(ctx, d.Domain)
	if errors.Is(err, domain.ErrSESIdentityNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("leer la identidad en SES: %w", err)
	}
	if !obs.OwnedBy(d.TenantID) {
		uc.logger.Warn("la identidad de SES del dominio no lleva la etiqueta de la empresa; no se borra",
			zap.String("domain", d.Domain), zap.String("tenant_id", d.TenantID.String()))
		return nil
	}
	if err := uc.ses.DeleteIdentity(ctx, d.Domain); err != nil {
		return fmt.Errorf("borrar la identidad en SES: %w", err)
	}
	return nil
}

// forgetSES olvida el estado de SES de un dominio que dejo de enviar y, si era apto, lo anuncia, en
// una transaccion con el cerrojo del dominio.
func (uc *UseCase) forgetSES(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.repo.WithDKIMLock(ctx, tenantID, id, func(ctx context.Context, d *domain.Domain) error {
		if d.SES == (domain.SESState{}) {
			return nil
		}
		wasReady := d.SES.VerifiedForSending() && d.Status == domain.StatusVerified
		d.SES = domain.SESState{}
		if err := uc.repo.SaveSESState(ctx, d); err != nil {
			return err
		}
		if wasReady {
			if err := uc.sendingEvents.SendingStatusChanged(ctx, d, false); err != nil {
				return fmt.Errorf("encolar domains.domain.sending_status_changed: %w", err)
			}
		}
		return nil
	})
}
