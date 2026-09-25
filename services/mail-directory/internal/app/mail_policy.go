package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// errPolicyUnwired: main no cableo la politica de correo. Es un fallo de despliegue.
var errPolicyUnwired = errors.New("mail-directory: política de correo sin cablear")

// MailPolicy devuelve la politica de correo de la empresa; la de por defecto si nunca se cambio.
func (uc *UseCase) MailPolicy(ctx context.Context, tenantID uuid.UUID) (*domain.MailPolicy, error) {
	if uc.policies == nil {
		return nil, errPolicyUnwired
	}
	var out *domain.MailPolicy
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		p, err := uc.policyOf(ctx, tenantID)
		out = p
		return err
	})
	return out, err
}

// SetMailPolicy cambia la politica de la empresa. by es el usuario de la plataforma que la cambia. Con
// el reenvio externo prohibido, retira en la misma transaccion los destinos externos de los filtros ya
// guardados de todos los buzones y regenera su script: un script guardado seguiria reenviando. Se
// repasan tambien al volver a guardar la politica ya prohibida, para dejar limpio lo que hubiera.
// Devuelve cuantos buzones perdieron algun destino.
func (uc *UseCase) SetMailPolicy(ctx context.Context, tenantID, by uuid.UUID, externalForwardingAllowed bool) (*domain.MailPolicy, int, error) {
	if uc.policies == nil {
		return nil, 0, errPolicyUnwired
	}
	var (
		out     *domain.MailPolicy
		removed int
	)
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if err := uc.policies.Lock(ctx, tenantID, true); err != nil {
			return err
		}
		p, err := uc.policyOf(ctx, tenantID)
		if err != nil {
			return err
		}
		p.ExternalForwardingAllowed = externalForwardingAllowed
		if by != uuid.Nil {
			u := by
			p.UpdatedBy = &u
		}
		if err := uc.policies.Upsert(ctx, p); err != nil {
			return err
		}
		if !externalForwardingAllowed {
			if removed, err = uc.stripExternalForwarding(ctx, tenantID); err != nil {
				return err
			}
		}
		out = p
		return uc.events.MailPolicyUpdated(ctx, p, removed)
	})
	if err != nil {
		return nil, 0, err
	}
	uc.logger.Info("mail-directory: política de correo cambiada",
		zap.String("tenant_id", tenantID.String()), zap.String("by", by.String()),
		zap.Bool("external_forwarding_allowed", externalForwardingAllowed), zap.Int("removed_mailboxes", removed))
	return out, removed, nil
}

// stripExternalForwarding retira los destinos externos de los filtros guardados de la empresa y
// devuelve cuantos buzones cambiaron.
func (uc *UseCase) stripExternalForwarding(ctx context.Context, tenantID uuid.UUID) (int, error) {
	rows, err := uc.filters.ListByTenant(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	var addrs []string
	for i := range rows {
		addrs = append(addrs, rows[i].ForwardAddresses()...)
	}
	if len(addrs) == 0 {
		return 0, nil
	}
	owned, err := uc.ownedDomains(ctx, tenantID, addrs)
	if err != nil {
		return 0, err
	}
	removed := 0
	for i := range rows {
		f := &rows[i]
		changed, err := f.StripExternalForwarding(owned)
		if err != nil {
			return 0, err
		}
		if !changed {
			continue
		}
		if err := uc.filters.Upsert(ctx, f); err != nil {
			return 0, err
		}
		removed++
	}
	return removed, nil
}

// policyOf lee la politica de la empresa; sin fila, la de por defecto.
func (uc *UseCase) policyOf(ctx context.Context, tenantID uuid.UUID) (*domain.MailPolicy, error) {
	p, err := uc.policies.Get(ctx, tenantID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.NewMailPolicy(tenantID), nil
	}
	return p, err
}

// ownedDomains son, de los dominios de las direcciones, los que son de la empresa (propios o alias).
func (uc *UseCase) ownedDomains(ctx context.Context, tenantID uuid.UUID, addrs []string) (map[string]bool, error) {
	owned := map[string]bool{}
	names := domain.AddressDomains(addrs)
	if len(names) == 0 {
		return owned, nil
	}
	found, err := uc.domains.OwnedNames(ctx, tenantID, names)
	if err != nil {
		return nil, err
	}
	for _, n := range found {
		owned[n] = true
	}
	return owned, nil
}
