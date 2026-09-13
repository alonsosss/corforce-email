package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/organization/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Indice global de los dominios de correo activos (Modelo_de_Datos_y_Celdas.md, 5.5).
//
// domain-service reclama un dominio antes de activarlo en el directorio de la celda de su
// empresa y lo suelta despues de desactivarlo. El indice guarda la empresa, no la celda: la
// celda del dominio es siempre la de su empresa. Con el, el gateway lleva el inicio de sesion
// del webmail a la celda del buzon, y un dominio no llega a activarse en dos celdas.

// ClaimMailDomain registra que el dominio es de la empresa y devuelve el nombre normalizado y la
// celda donde se activara. Es idempotente para la misma empresa. ErrMailDomainClaimed si otra
// empresa lo tiene activo; ErrTenantNotFound si la empresa no existe; ErrInvalidMailDomain si el
// nombre no es un dominio.
func (uc *OrganizationUseCase) ClaimMailDomain(ctx context.Context, tenantID uuid.UUID, raw string) (string, *domain.Cell, error) {
	name, err := domain.NormalizeMailDomain(raw)
	if err != nil {
		return "", nil, err
	}
	cell, err := uc.TenantCell(ctx, tenantID)
	if err != nil {
		return "", nil, err
	}
	if err := uc.mailDomains.Claim(ctx, name, tenantID); err != nil {
		if errors.Is(err, domain.ErrMailDomainClaimed) {
			uc.logger.Warn("indice de dominios: el dominio ya esta activo en otra empresa; no se activa",
				zap.String("domain", name), zap.String("tenant_id", tenantID.String()))
		}
		return "", nil, err
	}
	return name, cell, nil
}

// ReleaseMailDomain retira el dominio del indice si es de la empresa. Si no esta, o es de otra
// empresa, no hace nada: el estado pedido (la empresa no lo tiene) ya se cumple.
func (uc *OrganizationUseCase) ReleaseMailDomain(ctx context.Context, tenantID uuid.UUID, raw string) error {
	name, err := domain.NormalizeMailDomain(raw)
	if err != nil {
		return err
	}
	released, err := uc.mailDomains.Release(ctx, name, tenantID)
	if err != nil {
		return err
	}
	if released {
		uc.logger.Info("indice de dominios: dominio retirado",
			zap.String("domain", name), zap.String("tenant_id", tenantID.String()))
	}
	return nil
}

// MailDomainCell devuelve el nombre normalizado y la celda del dominio activo.
// ErrMailDomainNotFound si no esta en el indice o el nombre no es un dominio. La fila cae con su
// empresa, asi que una empresa o una celda que faltan son un registro incoherente y salen como
// error inesperado, nunca como "no esta".
func (uc *OrganizationUseCase) MailDomainCell(ctx context.Context, raw string) (string, *domain.Cell, error) {
	name, err := domain.NormalizeMailDomain(raw)
	if err != nil {
		return "", nil, domain.ErrMailDomainNotFound
	}
	tenantID, err := uc.mailDomains.TenantOf(ctx, name)
	if err != nil {
		return "", nil, err
	}
	cell, err := uc.TenantCell(ctx, tenantID)
	if err != nil {
		return "", nil, fmt.Errorf("dominio %s de la empresa %s, registro incoherente: %v", name, tenantID, err)
	}
	return name, cell, nil
}
