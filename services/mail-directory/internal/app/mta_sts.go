package app

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ListMTASTS pagina los dominios de la empresa con su modo MTA-STS.
func (uc *UseCase) ListMTASTS(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.MTASTSState, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.mtaSTS.States(ctx, tenantID, page)
		for i := range items {
			items[i].AllowedModes = items[i].NextModes()
		}
		return err
	})
	return items, total, err
}

// GetMTASTS devuelve el modo del dominio: none si nunca activo la politica.
func (uc *UseCase) GetMTASTS(ctx context.Context, tenantID uuid.UUID, rawName string) (*domain.MTASTSState, error) {
	name, err := domain.NormalizeDomain(rawName)
	if err != nil {
		return nil, err
	}
	var state *domain.MTASTSState
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		d, err := uc.mtaSTSDomain(ctx, tenantID, name)
		if err != nil {
			return err
		}
		policy, err := uc.currentMTASTS(ctx, tenantID, name)
		if err != nil {
			return err
		}
		state = mtaSTSState(d, policy)
		return nil
	})
	return state, err
}

// SetMTASTSMode cambia el modo de la politica del dominio. Activarla la deja en testing; enforce
// solo se admite desde testing, con el dominio verificado y activo y con todos sus MX publicados
// apuntando a la plataforma, y volver a none pasa por testing (domain.MTASTSTransition). Repetir el
// modo actual no cambia nada ni renueva la version. Cada cambio real renueva el id de la politica.
func (uc *UseCase) SetMTASTSMode(ctx context.Context, tenantID uuid.UUID, rawName, rawMode string) (*domain.MTASTSState, error) {
	name, err := domain.NormalizeDomain(rawName)
	if err != nil {
		return nil, err
	}
	mode, err := domain.ParseMTASTSMode(rawMode)
	if err != nil {
		return nil, err
	}
	if mode == domain.MTASTSEnforce {
		if err := uc.checkEnforceReady(ctx, tenantID, name); err != nil {
			return nil, err
		}
	}
	var state *domain.MTASTSState
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		d, err := uc.mtaSTSDomain(ctx, tenantID, name)
		if err != nil {
			return err
		}
		policy, err := uc.currentMTASTS(ctx, tenantID, name)
		if err != nil {
			return err
		}
		if mode == domain.MTASTSEnforce {
			if err := enforceAllowed(d, policy); err != nil {
				return err
			}
		} else if err := domain.MTASTSTransition(policy.Mode, mode); err != nil {
			return err
		}
		if policy.Mode != mode {
			if policy.PolicyID == "" {
				policy = domain.NewMTASTSPolicy(tenantID, name, mode)
			} else {
				policy.Change(mode)
			}
			if err := uc.mtaSTS.Upsert(ctx, policy); err != nil {
				return err
			}
		}
		state = mtaSTSState(d, policy)
		return nil
	})
	return state, err
}

// checkEnforceReady hace antes de escribir las comprobaciones de enforce que incluyen salir de la
// base, porque el DNS no se consulta dentro de la transaccion: el dominio activo y el modo de
// partida se leen y se validan primero, para no consultar el DNS de un cambio que ya no procede.
// SetMTASTSMode repite las dos dentro de la transaccion de escritura.
func (uc *UseCase) checkEnforceReady(ctx context.Context, tenantID uuid.UUID, name string) error {
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		d, err := uc.mtaSTSDomain(ctx, tenantID, name)
		if err != nil {
			return err
		}
		policy, err := uc.currentMTASTS(ctx, tenantID, name)
		if err != nil {
			return err
		}
		return enforceAllowed(d, policy)
	})
	if err != nil {
		return err
	}
	published, err := uc.mx.LookupMX(ctx, name)
	if err != nil {
		uc.logger.Warn("no se pudo consultar el MX del dominio para MTA-STS", zap.String("domain", name), zap.Error(err))
		return domain.ErrMTASTSDNSUnavailable
	}
	if !domain.MXMatchesPlatform(published, uc.platformMX) {
		return domain.ErrMTASTSMXMismatch
	}
	return nil
}

// enforceAllowed son las condiciones de enforce que salen de la base: se llega desde testing y con
// el dominio verificado y activo.
func enforceAllowed(d *domain.Domain, current *domain.MTASTSPolicy) error {
	if err := domain.MTASTSTransition(current.Mode, domain.MTASTSEnforce); err != nil {
		return err
	}
	if !d.Active {
		return domain.ErrMTASTSDomainNotActive
	}
	return nil
}

// PublishedMTASTS devuelve el cuerpo de la politica de un dominio para los remitentes, sin
// empresa: domain.ErrNotFound si el dominio no es de la celda, no esta activo o no publica politica.
func (uc *UseCase) PublishedMTASTS(ctx context.Context, rawName string) (string, error) {
	name, err := domain.NormalizeDomain(rawName)
	if err != nil {
		return "", domain.ErrNotFound
	}
	policy, err := uc.mtaSTSPublisher.Published(ctx, name)
	if err != nil {
		return "", err
	}
	return policy.Body(uc.platformMX), nil
}

// mtaSTSDomain resuelve el dominio de la empresa: uno que el directorio no tiene (nunca se verifico
// ni se activo) es un recurso que no existe, no un error de validacion.
func (uc *UseCase) mtaSTSDomain(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	d, err := uc.ownDomain(ctx, tenantID, name)
	if errors.Is(err, domain.ErrDomainNotOwned) {
		return nil, domain.ErrNotFound
	}
	return d, err
}

// currentMTASTS es la politica del dominio, o una en none sin version si no tiene.
func (uc *UseCase) currentMTASTS(ctx context.Context, tenantID uuid.UUID, name string) (*domain.MTASTSPolicy, error) {
	policy, err := uc.mtaSTS.ByDomain(ctx, tenantID, name)
	if errors.Is(err, domain.ErrNotFound) {
		return &domain.MTASTSPolicy{TenantID: tenantID, Domain: name, Mode: domain.MTASTSNone}, nil
	}
	return policy, err
}

func mtaSTSState(d *domain.Domain, p *domain.MTASTSPolicy) *domain.MTASTSState {
	state := &domain.MTASTSState{Domain: d.Domain, DomainActive: d.Active, Mode: p.Mode, MaxAge: p.MaxAge, PolicyID: p.PolicyID}
	state.AllowedModes = state.NextModes()
	if !p.UpdatedAt.IsZero() {
		at := p.UpdatedAt
		state.UpdatedAt = &at
	}
	return state
}
