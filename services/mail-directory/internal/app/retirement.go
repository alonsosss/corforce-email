package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RetireTenant da de baja a la empresa en el directorio de la celda. La pide la saga de baja de
// organization por la ruta interna, mientras la empresa aun existe en el registro.
//
// Desactiva, no borra: la baja conserva los datos de la empresa durante la retencion (su base de
// empresa tambien se conserva) y el contenido de los buzones sigue en el almacenamiento de Dovecot.
// Apaga todo lo que recibe, reenvia o autentica, y borra las contrasenas SASL de terceros que
// Postfix guarda en claro (relayhosts y transportes), de las que no hay nada que retener. Cada
// dominio, dominio alias, buzon y alias que cambia se anuncia por la outbox en la misma
// transaccion: mail-security los saca de DOMAIN_MAP y retira sus claves DKIM, y el webmail cierra
// las sesiones de los buzones. La baja queda registrada en la celda: desde ella el directorio de
// la empresa no admite escrituras (writeTx). Idempotente: repetirla no cambia ni anuncia nada.
func (uc *UseCase) RetireTenant(ctx context.Context, tenantID uuid.UUID) (*domain.TenantRetirement, error) {
	out := &domain.TenantRetirement{TenantID: tenantID}
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.retirements.HoldExclusive(ctx, tenantID); err != nil {
			return err
		}
		retiredAt, err := uc.retirements.Mark(ctx, tenantID)
		if err != nil {
			return err
		}
		counts, err := uc.retirements.DeactivateSettings(ctx, tenantID)
		if err != nil {
			return err
		}

		domains, err := uc.retirements.DeactivateDomains(ctx, tenantID)
		if err != nil {
			return err
		}
		for i := range domains {
			if err := uc.events.DomainUpdated(ctx, &domains[i]); err != nil {
				return err
			}
		}
		aliasDomains, err := uc.retirements.DeactivateAliasDomains(ctx, tenantID)
		if err != nil {
			return err
		}
		for i := range aliasDomains {
			if err := uc.events.AliasDomainUpdated(ctx, &aliasDomains[i]); err != nil {
				return err
			}
		}
		mailboxes, err := uc.retirements.DeactivateMailboxes(ctx, tenantID)
		if err != nil {
			return err
		}
		// La baja solo apaga el buzon: el aviso lo dice (active), y con eso el webmail cierra sus
		// sesiones igual que con cualquier buzon que deja de poder entrar.
		for i := range mailboxes {
			if err := uc.events.MailboxUpdated(ctx, &mailboxes[i], []domain.MailboxAttr{domain.AttrActive}); err != nil {
				return err
			}
		}
		aliases, err := uc.retirements.DeactivateAliases(ctx, tenantID)
		if err != nil {
			return err
		}
		for i := range aliases {
			if err := uc.events.AliasUpdated(ctx, &aliases[i]); err != nil {
				return err
			}
		}

		counts.Domains, counts.AliasDomains = len(domains), len(aliasDomains)
		counts.Mailboxes, counts.Aliases = len(mailboxes), len(aliases)
		out.RetiredAt, out.Deactivated = retiredAt, counts
		return nil
	})
	if err != nil {
		return nil, err
	}
	uc.logger.Info("empresa dada de baja en el directorio de la celda",
		zap.String("tenant_id", tenantID.String()), zap.Time("retired_at", out.RetiredAt),
		zap.Int("dominios", out.Deactivated.Domains), zap.Int("dominios_alias", out.Deactivated.AliasDomains),
		zap.Int("buzones", out.Deactivated.Mailboxes), zap.Int("aliases", out.Deactivated.Aliases))
	return out, nil
}
