package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

// PutFiltersRequest reemplaza las reglas y el reenvio del buzon enteros: lo que no llega se borra.
// Reauthenticated dice que el webmail acaba de volver a comprobar la contrasena (y el codigo, con
// verificacion en dos pasos): solo asi se admite un destino externo nuevo.
type PutFiltersRequest struct {
	Rules           []domain.FilterRule
	Forwarding      domain.Forwarding
	Reauthenticated bool
}

// FiltersByUsername devuelve las reglas y el reenvio del buzon del webmail; si nunca los guardo,
// vacios y apagados.
func (uc *UseCase) FiltersByUsername(ctx context.Context, username string) (*domain.MailboxFilters, error) {
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	var out *domain.MailboxFilters
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		f, err := uc.filters.ByUsername(ctx, tenantID, m.Username)
		if errors.Is(err, domain.ErrNotFound) {
			out = domain.NewMailboxFilters(tenantID, m.Username)
			return nil
		}
		out = f
		return err
	})
	return out, err
}

// PutFiltersByUsername valida, genera el script Sieve y guarda. La validacion necesita el nombre del
// buzon (el reenvio no puede apuntar a el), asi que va despues de localizarlo y antes de la
// transaccion: una entrada invalida no toma el candado de la empresa.
//
// Un destino externo es una direccion cuyo dominio no es propio ni alias de la empresa. Con el reenvio
// externo prohibido por la politica de la empresa no se guarda ninguno, activo o no. Si esta
// permitido, uno que empieza a recibir correo con este cambio exige Reauthenticated: una sesion
// robada no debe poder dejarse una copia de todo el correo fuera del buzon. Todo cambio del reenvio
// externo activo (o del interruptor del reenvio) se anuncia a auditoria en la misma transaccion.
func (uc *UseCase) PutFiltersByUsername(ctx context.Context, username string, req PutFiltersRequest) (*domain.MailboxFilters, error) {
	if uc.policies == nil {
		return nil, errPolicyUnwired
	}
	tenantID, mailboxID, err := uc.locate(ctx, username)
	if err != nil {
		return nil, err
	}
	login, _, err := domain.NormalizeEmail(username)
	if err != nil {
		return nil, err
	}
	f := &domain.MailboxFilters{ID: uuid.New(), TenantID: tenantID, Username: login, Rules: req.Rules, Forwarding: req.Forwarding}
	if err := f.Normalize(); err != nil {
		return nil, err
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		if m.Username != f.Username {
			return domain.ErrNotFound
		}
		if err := uc.policies.Lock(ctx, tenantID, false); err != nil {
			return err
		}
		policy, err := uc.policyOf(ctx, tenantID)
		if err != nil {
			return err
		}
		before, err := uc.filters.ByUsername(ctx, tenantID, m.Username)
		if errors.Is(err, domain.ErrNotFound) {
			before, err = domain.NewMailboxFilters(tenantID, m.Username), nil
		}
		if err != nil {
			return err
		}
		owned, err := uc.ownedDomains(ctx, tenantID, append(f.ForwardAddresses(), before.ForwardAddresses()...))
		if err != nil {
			return err
		}
		if external := domain.ExternalAddresses(f.ForwardAddresses(), owned); !policy.ExternalForwardingAllowed && len(external) > 0 {
			return &domain.ForwardingError{Kind: domain.ErrExternalForwardingDisabled, Addresses: external}
		}
		change, changed := domain.ExternalForwardingChange(before, f, owned)
		if len(change.ExternalAdded) > 0 && !req.Reauthenticated {
			return &domain.ForwardingError{Kind: domain.ErrReauthRequired, Addresses: change.ExternalAdded}
		}
		if err := uc.filters.Upsert(ctx, f); err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return uc.events.MailboxForwardingChanged(ctx, m, uc.now().UTC(), change)
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}
