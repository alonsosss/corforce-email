package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
	defaultLogins   = 50
	maxLogins       = 500
)

// Deps agrupa los puertos por nombre: con tantos repositorios del mismo esquema, un
// constructor posicional invita a cruzar dos del mismo tipo.
type Deps struct {
	Tx           ports.Transactor
	Domains      ports.DomainRepository
	AliasDomains ports.AliasDomainRepository
	Mailboxes    ports.MailboxRepository
	AppPasswords ports.AppPasswordRepository
	Sieve        ports.SieveRepository
	Aliases      ports.AliasRepository
	SpamAliases  ports.SpamAliasRepository
	SenderACL    ports.SenderACLRepository
	Relayhosts   ports.RelayhostRepository
	Transports   ports.TransportRepository
	TLSPolicies  ports.TLSPolicyRepository
	RecipientMap ports.RecipientMapRepository
	BCCMaps      ports.BCCMapRepository
	Secrets      ports.Secrets
	Events       ports.EventPublisher
	Logger       *zap.Logger
}

type UseCase struct {
	tx           ports.Transactor
	domains      ports.DomainRepository
	aliasDomains ports.AliasDomainRepository
	mailboxes    ports.MailboxRepository
	appPasswords ports.AppPasswordRepository
	sieve        ports.SieveRepository
	aliases      ports.AliasRepository
	spamAliases  ports.SpamAliasRepository
	senderACL    ports.SenderACLRepository
	relayhosts   ports.RelayhostRepository
	transports   ports.TransportRepository
	tlsPolicies  ports.TLSPolicyRepository
	recipientMap ports.RecipientMapRepository
	bccMaps      ports.BCCMapRepository
	secrets      ports.Secrets
	events       ports.EventPublisher
	logger       *zap.Logger
}

func New(d Deps) *UseCase {
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UseCase{
		tx: d.Tx, domains: d.Domains, aliasDomains: d.AliasDomains, mailboxes: d.Mailboxes,
		appPasswords: d.AppPasswords, sieve: d.Sieve, aliases: d.Aliases, spamAliases: d.SpamAliases,
		senderACL: d.SenderACL, relayhosts: d.Relayhosts, transports: d.Transports,
		tlsPolicies: d.TLSPolicies, recipientMap: d.RecipientMap, bccMaps: d.BCCMaps,
		secrets: d.Secrets, events: d.Events, logger: logger,
	}
}

// Scope es quien actua: la empresa de la peticion y si ademas opera la plataforma (lo
// unico que habilita administrar rutas sin empresa).
type Scope struct {
	TenantID uuid.UUID
	Platform bool
}

func (s Scope) transportScope() ports.TransportScope {
	return ports.TransportScope{TenantID: s.TenantID, Platform: s.Platform}
}

// NormalizePage acota pagina y tamano y devuelve el desplazamiento correspondiente.
func NormalizePage(page, perPage int) (int, int, ports.Page) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = defaultPageSize
	}
	if perPage > maxPageSize {
		perPage = maxPageSize
	}
	return page, perPage, ports.Page{Offset: (page - 1) * perPage, Limit: perPage}
}

// normalizeSearch recorta el texto de busqueda de un listado y rechaza el que supera el
// tope.
func normalizeSearch(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if utf8.RuneCountInString(s) > domain.MaxSearchLength {
		return "", domain.ErrSearchTooLong
	}
	return s, nil
}

// ownDomain resuelve un dominio propio de la empresa por nombre.
func (uc *UseCase) ownDomain(ctx context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	d, err := uc.domains.GetByName(ctx, tenantID, name)
	if err == domain.ErrNotFound {
		return nil, domain.ErrDomainNotOwned
	}
	return d, err
}

// ownsDomainOrAlias indica si la empresa sirve ese dominio, propio o alias.
func (uc *UseCase) ownsDomainOrAlias(ctx context.Context, tenantID uuid.UUID, name string) error {
	if _, err := uc.domains.GetByName(ctx, tenantID, name); err == nil {
		return nil
	} else if err != domain.ErrNotFound {
		return err
	}
	ok, err := uc.aliasDomains.ExistsByName(ctx, tenantID, name)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrDomainNotOwned
	}
	return nil
}

// ownMailbox exige que la direccion sea un buzon de la empresa.
func (uc *UseCase) ownMailbox(ctx context.Context, tenantID uuid.UUID, username string) (*domain.Mailbox, error) {
	m, err := uc.mailboxes.GetByUsername(ctx, tenantID, username)
	if err == domain.ErrNotFound {
		return nil, domain.ErrMailboxNotOwned
	}
	return m, err
}

func (uc *UseCase) ownRelayhost(ctx context.Context, tenantID uuid.UUID, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	if _, err := uc.relayhosts.Get(ctx, tenantID, *id); err != nil {
		if err == domain.ErrNotFound {
			return domain.ErrRelayhostNotOwned
		}
		return err
	}
	return nil
}

// addressFree exige que la direccion no sea ya buzon, alias ni alias temporal.
func (uc *UseCase) addressFree(ctx context.Context, tenantID uuid.UUID, address string) error {
	taken, err := uc.mailboxes.AddressInUse(ctx, tenantID, address)
	if err != nil {
		return err
	}
	if taken {
		return domain.ErrAddressTaken
	}
	return nil
}
