package app

import (
	"context"
	"fmt"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
)

// activateInDirectory reclama el dominio en el indice global de organization y solo entonces lo
// activa en el directorio de la celda de su empresa: un dominio activo para otra empresa
// (domain.ErrDomainClaimedElsewhere) no llega a activarse en ninguna celda, y sin respuesta de
// organization tampoco se activa (el barrido lo repite). Las dos llamadas son idempotentes.
func (uc *UseCase) activateInDirectory(ctx context.Context, d *domain.Domain) error {
	if err := uc.index.Claim(ctx, d.TenantID, d.Domain); err != nil {
		return fmt.Errorf("reclamar en el indice de dominios: %w", err)
	}
	return uc.mailDirectory.SetActivation(ctx, d.TenantID, d.Domain, true)
}

// retireFromDirectory desactiva el dominio en el directorio de la celda y, confirmado, lo suelta
// del indice global: soltarlo antes dejaria que otra empresa lo activara en otra celda mientras
// esta aun lo recibe. Las dos llamadas son idempotentes, y quien llama repite las dos hasta que
// ambas se confirman (la marca de desactivacion pendiente, o el borrado que no se completa).
func (uc *UseCase) retireFromDirectory(ctx context.Context, d *domain.Domain) error {
	if err := uc.mailDirectory.SetActivation(ctx, d.TenantID, d.Domain, false); err != nil {
		return err
	}
	if err := uc.index.Release(ctx, d.TenantID, d.Domain); err != nil {
		return fmt.Errorf("soltar del indice de dominios: %w", err)
	}
	return nil
}
