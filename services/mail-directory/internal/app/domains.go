package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type CreateDomainRequest struct {
	Domain             string
	Description        string
	BackupMX           bool
	RelayAllRecipients bool
	RelayUnknownOnly   bool
	RelayhostID        *uuid.UUID
	Limits             domain.DomainLimits
}

type UpdateDomainRequest struct {
	Description        *string
	Active             *bool
	BackupMX           *bool
	RelayAllRecipients *bool
	RelayUnknownOnly   *bool
	// RelayhostID con Set = true y valor nil desvincula el relayhost.
	RelayhostID       *uuid.UUID
	ClearRelayhost    bool
	MaxAliases        *int
	MaxMailboxes      *int
	DefaultQuotaBytes *int64
	MaxQuotaBytes     *int64
	QuotaBytes        *int64
}

func (r UpdateDomainRequest) empty() bool {
	return r.Description == nil && r.Active == nil && r.BackupMX == nil && r.RelayAllRecipients == nil &&
		r.RelayUnknownOnly == nil && r.RelayhostID == nil && !r.ClearRelayhost && r.MaxAliases == nil &&
		r.MaxMailboxes == nil && r.DefaultQuotaBytes == nil && r.MaxQuotaBytes == nil && r.QuotaBytes == nil
}

// ListDomains pagina los dominios de la empresa; filter.Search busca por subcadena del
// nombre sin distinguir mayusculas.
func (uc *UseCase) ListDomains(ctx context.Context, tenantID uuid.UUID, filter ports.DomainFilter, page ports.Page) (items []domain.Domain, total int64, err error) {
	if filter.Search, err = normalizeSearch(filter.Search); err != nil {
		return nil, 0, err
	}
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.domains.List(ctx, tenantID, filter, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetDomain(ctx context.Context, tenantID, id uuid.UUID) (d *domain.Domain, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		d, err = uc.domains.Get(ctx, tenantID, id)
		return err
	})
	return d, err
}

// CreateDomain da de alta el dominio INACTIVO: recibe y envia cuando el servicio de
// dominios lo verifica y llama a la activacion interna.
func (uc *UseCase) CreateDomain(ctx context.Context, tenantID uuid.UUID, req CreateDomainRequest) (*domain.Domain, error) {
	name, err := domain.NormalizeDomain(req.Domain)
	if err != nil {
		return nil, err
	}
	if err := req.Limits.Validate(); err != nil {
		return nil, err
	}
	d := &domain.Domain{
		ID: uuid.New(), TenantID: tenantID, Domain: name, Description: req.Description,
		Active: false, BackupMX: req.BackupMX, RelayAllRecipients: req.RelayAllRecipients,
		RelayUnknownOnly: req.RelayUnknownOnly, RelayhostID: req.RelayhostID,
		MaxAliases: req.Limits.MaxAliases, MaxMailboxes: req.Limits.MaxMailboxes,
		DefaultQuotaBytes: req.Limits.DefaultQuotaBytes, MaxQuotaBytes: req.Limits.MaxQuotaBytes,
		QuotaBytes: req.Limits.QuotaBytes,
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if err := uc.ownRelayhost(ctx, tenantID, d.RelayhostID); err != nil {
			return err
		}
		if err := uc.nameFree(ctx, name); err != nil {
			return err
		}
		if err := uc.domains.Create(ctx, d); err != nil {
			return err
		}
		return uc.events.DomainCreated(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// nameFree rechaza un nombre que ya sea dominio o dominio alias en la celda, de esta o
// de otra empresa. La restriccion UNIQUE cubre la carrera.
func (uc *UseCase) nameFree(ctx context.Context, name string) error {
	inUse, err := uc.domains.NameInUse(ctx, name)
	if err != nil {
		return err
	}
	if inUse {
		return domain.ErrAlreadyExists
	}
	return nil
}

func (uc *UseCase) UpdateDomain(ctx context.Context, tenantID, id uuid.UUID, req UpdateDomainRequest) (*domain.Domain, error) {
	if req.empty() {
		return nil, domain.ErrNothingToUpdate
	}
	// Activar es consecuencia de verificar el dominio; por el API solo se puede apagar.
	if req.Active != nil && *req.Active {
		return nil, domain.ErrActivationNotAllowed
	}
	var d *domain.Domain
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		d, err = uc.domains.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		applyDomainUpdate(d, req)
		if err := uc.ownRelayhost(ctx, tenantID, d.RelayhostID); err != nil {
			return err
		}
		if err := domainLimits(d).Validate(); err != nil {
			return err
		}
		if err := uc.domains.Update(ctx, d); err != nil {
			return err
		}
		return uc.events.DomainUpdated(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func applyDomainUpdate(d *domain.Domain, req UpdateDomainRequest) {
	if req.Description != nil {
		d.Description = *req.Description
	}
	if req.Active != nil {
		d.Active = *req.Active
	}
	if req.BackupMX != nil {
		d.BackupMX = *req.BackupMX
	}
	if req.RelayAllRecipients != nil {
		d.RelayAllRecipients = *req.RelayAllRecipients
	}
	if req.RelayUnknownOnly != nil {
		d.RelayUnknownOnly = *req.RelayUnknownOnly
	}
	if req.ClearRelayhost {
		d.RelayhostID = nil
	} else if req.RelayhostID != nil {
		d.RelayhostID = req.RelayhostID
	}
	if req.MaxAliases != nil {
		d.MaxAliases = *req.MaxAliases
	}
	if req.MaxMailboxes != nil {
		d.MaxMailboxes = *req.MaxMailboxes
	}
	if req.DefaultQuotaBytes != nil {
		d.DefaultQuotaBytes = *req.DefaultQuotaBytes
	}
	if req.MaxQuotaBytes != nil {
		d.MaxQuotaBytes = *req.MaxQuotaBytes
	}
	if req.QuotaBytes != nil {
		d.QuotaBytes = *req.QuotaBytes
	}
}

func domainLimits(d *domain.Domain) domain.DomainLimits {
	return domain.DomainLimits{
		MaxAliases: d.MaxAliases, MaxMailboxes: d.MaxMailboxes, DefaultQuotaBytes: d.DefaultQuotaBytes,
		MaxQuotaBytes: d.MaxQuotaBytes, QuotaBytes: d.QuotaBytes,
	}
}

// DeleteDomain se niega mientras cuelguen buzones, aliases o dominios alias: borrarlos
// en cascada dejaria correo sin destino sin que nadie lo pidiera.
func (uc *UseCase) DeleteDomain(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		d, err := uc.domains.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		mailboxes, aliases, aliasDomains, err := uc.domains.Usage(ctx, tenantID, d.Domain)
		if err != nil {
			return err
		}
		if mailboxes > 0 || aliases > 0 || aliasDomains > 0 {
			return domain.ErrDomainInUse
		}
		if err := uc.domains.Delete(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.events.DomainDeleted(ctx, d)
	})
}

// SetDomainActivation es la llamada interna del servicio de dominios: crea el dominio si
// no existe (con los limites por defecto) y fija active. Idempotente. Una empresa dada de baja en
// la celda solo apaga un dominio que ya tiene: activar o dar de alta uno es ErrTenantRetired.
func (uc *UseCase) SetDomainActivation(ctx context.Context, tenantID uuid.UUID, rawName string, active bool) (*domain.Domain, error) {
	name, err := domain.NormalizeDomain(rawName)
	if err != nil {
		return nil, err
	}
	var d *domain.Domain
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		retired, err := uc.retirements.HoldShared(ctx, tenantID)
		if err != nil {
			return err
		}
		d, err = uc.domains.GetByName(ctx, tenantID, name)
		switch err {
		case nil:
			if active && retired {
				return domain.ErrTenantRetired
			}
			if d.Active != active {
				d.Active = active
				if err := uc.domains.Update(ctx, d); err != nil {
					return err
				}
			}
		case domain.ErrNotFound:
			if retired {
				return domain.ErrTenantRetired
			}
			if err := uc.nameFree(ctx, name); err != nil {
				return err
			}
			d = &domain.Domain{ID: uuid.New(), TenantID: tenantID, Domain: name, Active: active}
			if err := uc.domains.Create(ctx, d); err != nil {
				return err
			}
			if err := uc.events.DomainCreated(ctx, d); err != nil {
				return err
			}
		default:
			return err
		}
		// Tambien sin cambios: la llamada es idempotente y los consumidores leen el
		// estado real del directorio, no el del evento.
		return uc.events.DomainActivated(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// ── Dominios alias ────────────────────────────────────────────────────────────

type CreateAliasDomainRequest struct {
	AliasDomain  string
	TargetDomain string
	Active       *bool
}

type UpdateAliasDomainRequest struct {
	TargetDomain *string
	Active       *bool
}

func (uc *UseCase) ListAliasDomains(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.AliasDomain, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.aliasDomains.List(ctx, tenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) CreateAliasDomain(ctx context.Context, tenantID uuid.UUID, req CreateAliasDomainRequest) (*domain.AliasDomain, error) {
	alias, err := domain.NormalizeDomain(req.AliasDomain)
	if err != nil {
		return nil, err
	}
	target, err := domain.NormalizeDomain(req.TargetDomain)
	if err != nil {
		return nil, err
	}
	if alias == target {
		return nil, domain.ErrDomainIsOwnDomain
	}
	a := &domain.AliasDomain{ID: uuid.New(), TenantID: tenantID, AliasDomain: alias, TargetDomain: target, Active: true}
	if req.Active != nil {
		a.Active = *req.Active
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if _, err := uc.ownDomain(ctx, tenantID, target); err != nil {
			return err
		}
		if _, err := uc.domains.GetByName(ctx, tenantID, alias); err == nil {
			return domain.ErrDomainIsOwnDomain
		} else if err != domain.ErrNotFound {
			return err
		}
		if err := uc.nameFree(ctx, alias); err != nil {
			return err
		}
		if err := uc.aliasDomains.Create(ctx, a); err != nil {
			return err
		}
		return uc.events.AliasDomainCreated(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (uc *UseCase) UpdateAliasDomain(ctx context.Context, tenantID, id uuid.UUID, req UpdateAliasDomainRequest) (*domain.AliasDomain, error) {
	if req.TargetDomain == nil && req.Active == nil {
		return nil, domain.ErrNothingToUpdate
	}
	var a *domain.AliasDomain
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		a, err = uc.aliasDomains.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if req.TargetDomain != nil {
			target, err := domain.NormalizeDomain(*req.TargetDomain)
			if err != nil {
				return err
			}
			if target == a.AliasDomain {
				return domain.ErrDomainIsOwnDomain
			}
			if _, err := uc.ownDomain(ctx, tenantID, target); err != nil {
				return err
			}
			a.TargetDomain = target
		}
		if req.Active != nil {
			a.Active = *req.Active
		}
		if err := uc.aliasDomains.Update(ctx, a); err != nil {
			return err
		}
		return uc.events.AliasDomainUpdated(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (uc *UseCase) DeleteAliasDomain(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		a, err := uc.aliasDomains.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if err := uc.aliasDomains.Delete(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.events.AliasDomainDeleted(ctx, a)
	})
}
