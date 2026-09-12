package app

import (
	"context"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type CreateAliasRequest struct {
	Address        string
	Goto           string
	SenderAllowed  *bool
	Internal       bool
	Active         *int
	PrivateComment string
	PublicComment  string
}

type UpdateAliasRequest struct {
	Goto           *string
	SenderAllowed  *bool
	Internal       *bool
	Active         *int
	PrivateComment *string
	PublicComment  *string
}

func (r UpdateAliasRequest) empty() bool {
	return r.Goto == nil && r.SenderAllowed == nil && r.Internal == nil && r.Active == nil &&
		r.PrivateComment == nil && r.PublicComment == nil
}

func (uc *UseCase) ListAliases(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.Alias, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.aliases.List(ctx, tenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetAlias(ctx context.Context, tenantID, id uuid.UUID) (a *domain.Alias, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		a, err = uc.aliases.Get(ctx, tenantID, id)
		return err
	})
	return a, err
}

// CreateAlias admite direcciones completas o '@dominio' (catch-all) sobre un dominio propio
// o alias de la empresa. Los destinos pueden ser externos. Nunca pisa un buzon existente.
func (uc *UseCase) CreateAlias(ctx context.Context, tenantID uuid.UUID, req CreateAliasRequest) (*domain.Alias, error) {
	address, domainPart, err := domain.NormalizeAddress(req.Address)
	if err != nil {
		return nil, err
	}
	gotoList, err := domain.NormalizeGoto(req.Goto)
	if err != nil {
		return nil, err
	}
	active := domain.ActiveOn
	if req.Active != nil {
		active = *req.Active
	}
	if err := domain.ValidateActive(active); err != nil {
		return nil, err
	}
	a := &domain.Alias{
		ID: uuid.New(), TenantID: tenantID, Address: address, Goto: gotoList, Domain: domainPart,
		SenderAllowed: boolOr(req.SenderAllowed, true), Internal: req.Internal, Active: active,
		PrivateComment: strings.TrimSpace(req.PrivateComment), PublicComment: strings.TrimSpace(req.PublicComment),
	}
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.ownsDomainOrAlias(ctx, tenantID, domainPart); err != nil {
			return err
		}
		if d, err := uc.domains.GetByName(ctx, tenantID, domainPart); err == nil {
			count, err := uc.aliases.CountByDomain(ctx, tenantID, domainPart)
			if err != nil {
				return err
			}
			if err := domain.CheckLimit(d.MaxAliases, count, domain.ErrMaxAliasesReached); err != nil {
				return err
			}
		} else if err != domain.ErrNotFound {
			return err
		}
		if err := uc.addressFree(ctx, tenantID, address); err != nil {
			return err
		}
		return uc.aliases.Create(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	uc.publish("mail.alias.created", uc.events.AliasCreated(ctx, a))
	return a, nil
}

func (uc *UseCase) UpdateAlias(ctx context.Context, tenantID, id uuid.UUID, req UpdateAliasRequest) (*domain.Alias, error) {
	if req.empty() {
		return nil, domain.ErrNothingToUpdate
	}
	if req.Active != nil {
		if err := domain.ValidateActive(*req.Active); err != nil {
			return nil, err
		}
	}
	var gotoList string
	if req.Goto != nil {
		var err error
		if gotoList, err = domain.NormalizeGoto(*req.Goto); err != nil {
			return nil, err
		}
	}
	var a *domain.Alias
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		a, err = uc.aliases.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if req.Goto != nil {
			a.Goto = gotoList
		}
		a.SenderAllowed = boolOr(req.SenderAllowed, a.SenderAllowed)
		a.Internal = boolOr(req.Internal, a.Internal)
		if req.Active != nil {
			a.Active = *req.Active
		}
		if req.PrivateComment != nil {
			a.PrivateComment = strings.TrimSpace(*req.PrivateComment)
		}
		if req.PublicComment != nil {
			a.PublicComment = strings.TrimSpace(*req.PublicComment)
		}
		return uc.aliases.Update(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	uc.publish("mail.alias.updated", uc.events.AliasUpdated(ctx, a))
	return a, nil
}

func (uc *UseCase) DeleteAlias(ctx context.Context, tenantID, id uuid.UUID) error {
	var a *domain.Alias
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		a, err = uc.aliases.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		return uc.aliases.Delete(ctx, tenantID, id)
	})
	if err != nil {
		return err
	}
	uc.publish("mail.alias.deleted", uc.events.AliasDeleted(ctx, a))
	return nil
}

// ── Aliases temporales ────────────────────────────────────────────────────────

type CreateSpamAliasRequest struct {
	Address     string
	Goto        string
	Description string
	ValidUntil  *time.Time
	Permanent   bool
}

type UpdateSpamAliasRequest struct {
	Description *string
	ValidUntil  *time.Time
	Permanent   *bool
}

func (uc *UseCase) ListSpamAliases(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.SpamAlias, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.spamAliases.List(ctx, tenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetSpamAlias(ctx context.Context, tenantID, id uuid.UUID) (a *domain.SpamAlias, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		a, err = uc.spamAliases.Get(ctx, tenantID, id)
		return err
	})
	return a, err
}

func (uc *UseCase) CreateSpamAlias(ctx context.Context, tenantID uuid.UUID, req CreateSpamAliasRequest) (*domain.SpamAlias, error) {
	address, domainPart, err := domain.NormalizeEmail(req.Address)
	if err != nil {
		return nil, err
	}
	gotoAddr, _, err := domain.NormalizeEmail(req.Goto)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateSpamAliasValidity(req.ValidUntil, req.Permanent); err != nil {
		return nil, err
	}
	a := &domain.SpamAlias{
		ID: uuid.New(), TenantID: tenantID, Address: address, Goto: gotoAddr,
		Description: strings.TrimSpace(req.Description), ValidUntil: req.ValidUntil, Permanent: req.Permanent,
	}
	if a.Permanent {
		a.ValidUntil = nil
	}
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.ownsDomainOrAlias(ctx, tenantID, domainPart); err != nil {
			return err
		}
		if _, err := uc.ownMailbox(ctx, tenantID, gotoAddr); err != nil {
			return err
		}
		if err := uc.addressFree(ctx, tenantID, address); err != nil {
			return err
		}
		return uc.spamAliases.Create(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (uc *UseCase) UpdateSpamAlias(ctx context.Context, tenantID, id uuid.UUID, req UpdateSpamAliasRequest) (*domain.SpamAlias, error) {
	if req.Description == nil && req.ValidUntil == nil && req.Permanent == nil {
		return nil, domain.ErrNothingToUpdate
	}
	var a *domain.SpamAlias
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		a, err = uc.spamAliases.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if req.Description != nil {
			a.Description = strings.TrimSpace(*req.Description)
		}
		if req.ValidUntil != nil {
			a.ValidUntil = req.ValidUntil
		}
		if req.Permanent != nil {
			a.Permanent = *req.Permanent
		}
		if a.Permanent {
			a.ValidUntil = nil
		}
		if err := domain.ValidateSpamAliasValidity(a.ValidUntil, a.Permanent); err != nil {
			return err
		}
		return uc.spamAliases.Update(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (uc *UseCase) DeleteSpamAlias(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := uc.spamAliases.Get(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.spamAliases.Delete(ctx, tenantID, id)
	})
}
