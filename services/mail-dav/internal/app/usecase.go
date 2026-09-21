package app

import (
	"context"
	"errors"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-dav/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// DefaultSlug es la libreta que se crea sola la primera vez que un buzon descubre sus libretas.
const DefaultSlug = "contacts"

type Config struct {
	Limits                 domain.Limits
	DefaultAddressbookName string
}

type Deps struct {
	Auth   ports.Authenticator
	Tenant ports.TenantBinder
	Store  ports.Store
	Config Config
	Logger *zap.Logger
}

type UseCase struct {
	auth   ports.Authenticator
	tenant ports.TenantBinder
	store  ports.Store
	cfg    Config
	logger *zap.Logger
}

func New(d Deps) (*UseCase, error) {
	if err := d.Config.Limits.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(d.Config.DefaultAddressbookName) == "" {
		return nil, errors.New("falta el nombre de la libreta por defecto")
	}
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UseCase{auth: d.Auth, tenant: d.Tenant, store: d.Store, cfg: d.Config, logger: logger}, nil
}

func (uc *UseCase) Limits() domain.Limits { return uc.cfg.Limits }

// Authenticate verifica la credencial y devuelve el contexto ya ligado a la empresa y al buzon: desde
// aqui todo acceso a datos usa ese contexto.
func (uc *UseCase) Authenticate(ctx context.Context, username, password, remoteIP string) (context.Context, domain.Principal, error) {
	p, err := uc.auth.Authenticate(ctx, username, password, remoteIP)
	if err != nil {
		return ctx, domain.Principal{}, err
	}
	bound, err := uc.tenant.Bind(ctx, p)
	if err != nil {
		return ctx, domain.Principal{}, err
	}
	return bound, p, nil
}

// Addressbooks lista las libretas del buzon; si no tiene ninguna crea la de por defecto.
func (uc *UseCase) Addressbooks(ctx context.Context, p domain.Principal) ([]domain.Addressbook, error) {
	books, err := uc.store.ListAddressbooks(ctx, p)
	if err != nil || len(books) > 0 {
		return books, err
	}
	_, err = uc.store.CreateAddressbook(ctx, p, domain.Addressbook{
		ID: uuid.New(), TenantID: p.TenantID, MailboxID: p.MailboxID,
		Slug: DefaultSlug, DisplayName: uc.cfg.DefaultAddressbookName,
	}, uc.cfg.Limits.MaxAddressbooksPerMailbox)
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return nil, err
	}
	return uc.store.ListAddressbooks(ctx, p)
}

func (uc *UseCase) Addressbook(ctx context.Context, p domain.Principal, slug string) (domain.Addressbook, error) {
	if !domain.ValidSlug(slug) {
		return domain.Addressbook{}, domain.ErrNotFound
	}
	return uc.store.GetAddressbook(ctx, p, slug)
}

func (uc *UseCase) CreateAddressbook(ctx context.Context, p domain.Principal, slug, displayName, description string) (domain.Addressbook, error) {
	displayName, description = strings.TrimSpace(displayName), strings.TrimSpace(description)
	if !domain.ValidSlug(slug) {
		return domain.Addressbook{}, domain.ErrInvalidName
	}
	if displayName == "" {
		displayName = slug
	}
	if len([]rune(displayName)) > domain.MaxDisplayNameLength || len([]rune(description)) > domain.MaxDescriptionLength ||
		strings.ContainsAny(displayName+description, "\x00\r") {
		return domain.Addressbook{}, domain.ErrInvalidName
	}
	return uc.store.CreateAddressbook(ctx, p, domain.Addressbook{
		ID: uuid.New(), TenantID: p.TenantID, MailboxID: p.MailboxID,
		Slug: slug, DisplayName: displayName, Description: description,
	}, uc.cfg.Limits.MaxAddressbooksPerMailbox)
}

func (uc *UseCase) DeleteAddressbook(ctx context.Context, p domain.Principal, slug string) error {
	if !domain.ValidSlug(slug) {
		return domain.ErrNotFound
	}
	return uc.store.DeleteAddressbook(ctx, p, slug)
}

func (uc *UseCase) Contacts(ctx context.Context, p domain.Principal, slug string) (domain.Addressbook, []domain.Contact, error) {
	if !domain.ValidSlug(slug) {
		return domain.Addressbook{}, nil, domain.ErrNotFound
	}
	return uc.store.ListContacts(ctx, p, slug)
}

func (uc *UseCase) Contact(ctx context.Context, p domain.Principal, slug, resource string) (domain.Contact, error) {
	if !domain.ValidSlug(slug) || !domain.ValidResourceName(resource) {
		return domain.Contact{}, domain.ErrNotFound
	}
	return uc.store.GetContact(ctx, p, slug, resource)
}

// ContactsByName atiende addressbook-multiget: los nombres que no son validos o no existen se
// omiten, y quien llama los informa como 404.
func (uc *UseCase) ContactsByName(ctx context.Context, p domain.Principal, slug string, resources []string) ([]domain.Contact, error) {
	if !domain.ValidSlug(slug) {
		return nil, domain.ErrNotFound
	}
	valid := make([]string, 0, len(resources))
	for _, r := range resources {
		if domain.ValidResourceName(r) {
			valid = append(valid, r)
		}
	}
	if len(valid) == 0 {
		if _, err := uc.store.GetAddressbook(ctx, p, slug); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return uc.store.GetContacts(ctx, p, slug, valid)
}

// Query atiende addressbook-query: filtra en memoria los contactos de la libreta, que estan
// acotados por buzon. Un vCard guardado que ya no se pueda leer no aparece en el resultado.
func (uc *UseCase) Query(ctx context.Context, p domain.Principal, slug string, filter domain.Filter, limit int) (domain.Addressbook, []domain.Contact, error) {
	book, all, err := uc.Contacts(ctx, p, slug)
	if err != nil {
		return book, nil, err
	}
	out := make([]domain.Contact, 0, len(all))
	for _, c := range all {
		card, err := domain.ParseStoredVCard(c.VCard)
		if err != nil {
			uc.logger.Warn("mail-dav: vCard guardado ilegible, se omite de la consulta",
				zap.String("contact_id", c.ID.String()), zap.Error(err))
			continue
		}
		if filter.Matches(card) {
			out = append(out, c)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return book, out, nil
}

// Put crea o reemplaza el contacto. created distingue el 201 del 204.
func (uc *UseCase) Put(ctx context.Context, p domain.Principal, slug, resource, raw string, cond domain.Precondition) (etag string, created bool, err error) {
	if !domain.ValidSlug(slug) {
		return "", false, domain.ErrNotFound
	}
	c, err := domain.NewContact(resource, raw, uc.cfg.Limits)
	if err != nil {
		return "", false, err
	}
	c.ID, c.TenantID, c.MailboxID = uuid.New(), p.TenantID, p.MailboxID
	created, err = uc.store.PutContact(ctx, p, slug, c, cond, uc.cfg.Limits.MaxContactsPerMailbox, uc.cfg.Limits.MaxChangesRetained)
	if err != nil {
		return "", false, err
	}
	return c.ETag, created, nil
}

func (uc *UseCase) Delete(ctx context.Context, p domain.Principal, slug, resource string, cond domain.Precondition) error {
	if !domain.ValidSlug(slug) || !domain.ValidResourceName(resource) {
		return domain.ErrNotFound
	}
	return uc.store.DeleteContact(ctx, p, slug, resource, cond, uc.cfg.Limits.MaxChangesRetained)
}

// SyncResult es la respuesta de sync-collection: lo que cambio, lo que se borro y el token nuevo.
type SyncResult struct {
	Token   string
	Changed []domain.Contact
	Removed []string
}

// Sync atiende sync-collection. Con token vacio devuelve todos los contactos y ninguna baja.
func (uc *UseCase) Sync(ctx context.Context, p domain.Principal, slug, token string) (SyncResult, error) {
	if !domain.ValidSlug(slug) {
		return SyncResult{}, domain.ErrNotFound
	}
	bookID, seq, initial, err := domain.ParseSyncToken(token)
	if err != nil {
		return SyncResult{}, err
	}
	if initial {
		book, contacts, err := uc.store.ListContacts(ctx, p, slug)
		if err != nil {
			return SyncResult{}, err
		}
		return SyncResult{Token: domain.SyncToken(book.ID, book.SyncSeq), Changed: contacts}, nil
	}
	book, changed, removed, err := uc.store.ChangesSince(ctx, p, slug, seq)
	if err != nil {
		return SyncResult{}, err
	}
	if book.ID != bookID {
		return SyncResult{}, domain.ErrInvalidSyncToken
	}
	return SyncResult{Token: domain.SyncToken(book.ID, book.SyncSeq), Changed: changed, Removed: removed}, nil
}
