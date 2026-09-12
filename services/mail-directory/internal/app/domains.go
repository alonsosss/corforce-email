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

func (uc *UseCase) ListDomains(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.Domain, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.domains.List(ctx, tenantID, page)
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
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.ownRelayhost(ctx, tenantID, d.RelayhostID); err != nil {
			return err
		}
		if err := uc.nameFree(ctx, name); err != nil {
			return err
		}
		return uc.domains.Create(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	uc.publish("mail.domain.created", uc.events.DomainCreated(ctx, d))
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
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
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
		return uc.domains.Update(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	uc.publish("mail.domain.updated", uc.events.DomainUpdated(ctx, d))
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
	var d *domain.Domain
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		d, err = uc.domains.Get(ctx, tenantID, id)
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
		return uc.domains.Delete(ctx, tenantID, id)
	})
	if err != nil {
		return err
	}
	uc.publish("mail.domain.deleted", uc.events.DomainDeleted(ctx, d))
	return nil
}

// SetDomainActivation es la llamada interna del servicio de dominios: crea el dominio si
// no existe (con los limites por defecto) y fija active. Idempotente.
func (uc *UseCase) SetDomainActivation(ctx context.Context, tenantID uuid.UUID, rawName string, active bool) (*domain.Domain, error) {
	name, err := domain.NormalizeDomain(rawName)
	if err != nil {
		return nil, err
	}
	var d *domain.Domain
	created := false
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		d, err = uc.domains.GetByName(ctx, tenantID, name)
		switch err {
		case nil:
			if d.Active == active {
				return nil
			}
			d.Active = active
			return uc.domains.Update(ctx, d)
		case domain.ErrNotFound:
			if err := uc.nameFree(ctx, name); err != nil {
				return err
			}
			d = &domain.Domain{ID: uuid.New(), TenantID: tenantID, Domain: name, Active: active}
			created = true
			return uc.domains.Create(ctx, d)
		default:
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	if created {
		uc.publish("mail.domain.created", uc.events.DomainCreated(ctx, d))
	}
	uc.publish("mail.domain.activated", uc.events.DomainActivated(ctx, d))
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
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
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
		return uc.aliasDomains.Create(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	uc.publish("mail.alias_domain.created", uc.events.AliasDomainCreated(ctx, a))
	return a, nil
}

func (uc *UseCase) UpdateAliasDomain(ctx context.Context, tenantID, id uuid.UUID, req UpdateAliasDomainRequest) (*domain.AliasDomain, error) {
	if req.TargetDomain == nil && req.Active == nil {
		return nil, domain.ErrNothingToUpdate
	}
	var a *domain.AliasDomain
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
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
		return uc.aliasDomains.Update(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (uc *UseCase) DeleteAliasDomain(ctx context.Context, tenantID, id uuid.UUID) error {
	var a *domain.AliasDomain
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		a, err = uc.aliasDomains.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		return uc.aliasDomains.Delete(ctx, tenantID, id)
	})
	if err != nil {
		return err
	}
	uc.publish("mail.alias_domain.deleted", uc.events.AliasDomainDeleted(ctx, a))
	return nil
}
