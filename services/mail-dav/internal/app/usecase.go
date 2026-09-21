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
	Calendar               domain.CalendarLimits
	DefaultAddressbookName string
	DefaultCalendarName    string
}

type Deps struct {
	Auth      ports.Authenticator
	Tenant    ports.TenantBinder
	Store     ports.Store
	Calendars ports.CalendarStore
	Index     ports.MailboxIndex
	Config    Config
	Logger    *zap.Logger
}

type UseCase struct {
	auth      ports.Authenticator
	tenant    ports.TenantBinder
	store     ports.Store
	calendars ports.CalendarStore
	index     ports.MailboxIndex
	cfg       Config
	logger    *zap.Logger
}

func New(d Deps) (*UseCase, error) {
	if err := d.Config.Limits.Validate(); err != nil {
		return nil, err
	}
	if err := d.Config.Calendar.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(d.Config.DefaultAddressbookName) == "" {
		return nil, errors.New("falta el nombre de la libreta por defecto")
	}
	if strings.TrimSpace(d.Config.DefaultCalendarName) == "" {
		return nil, errors.New("falta el nombre del calendario por defecto")
	}
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UseCase{auth: d.Auth, tenant: d.Tenant, store: d.Store, calendars: d.Calendars, index: d.Index, cfg: d.Config, logger: logger}, nil
}

func (uc *UseCase) Limits() domain.Limits { return uc.cfg.Limits }

func (uc *UseCase) CalendarLimits() domain.CalendarLimits { return uc.cfg.Calendar }

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
	displayName, description, err := collectionNames(slug, displayName, description)
	if err != nil {
		return domain.Addressbook{}, err
	}
	return uc.store.CreateAddressbook(ctx, p, domain.Addressbook{
		ID: uuid.New(), TenantID: p.TenantID, MailboxID: p.MailboxID,
		Slug: slug, DisplayName: displayName, Description: description,
	}, uc.cfg.Limits.MaxAddressbooksPerMailbox)
}

// collectionNames valida y normaliza lo que el cliente da al crear una libreta o un calendario.
func collectionNames(slug, displayName, description string) (string, string, error) {
	displayName, description = strings.TrimSpace(displayName), strings.TrimSpace(description)
	if !domain.ValidSlug(slug) {
		return "", "", domain.ErrInvalidName
	}
	if displayName == "" {
		displayName = slug
	}
	if len([]rune(displayName)) > domain.MaxDisplayNameLength || len([]rune(description)) > domain.MaxDescriptionLength ||
		strings.ContainsAny(displayName+description, "\x00\r") {
		return "", "", domain.ErrInvalidName
	}
	return displayName, description, nil
}

func (uc *UseCase) DeleteAddressbook(ctx context.Context, p domain.Principal, slug string) error {
	if !domain.ValidSlug(slug) {
		return domain.ErrNotFound
	}
	return uc.store.DeleteAddressbook(ctx, p, slug)
}

// Contacts lista los contactos de la libreta; sin withData solo con sus metadatos y su tamano.
func (uc *UseCase) Contacts(ctx context.Context, p domain.Principal, slug string, withData bool) (domain.Addressbook, []domain.Contact, error) {
	if !domain.ValidSlug(slug) {
		return domain.Addressbook{}, nil, domain.ErrNotFound
	}
	return uc.store.ListContacts(ctx, p, slug, uc.cfg.Limits.Read(withData))
}

func (uc *UseCase) Contact(ctx context.Context, p domain.Principal, slug, resource string) (domain.Contact, error) {
	if !domain.ValidSlug(slug) || !domain.ValidResourceName(resource) {
		return domain.Contact{}, domain.ErrNotFound
	}
	return uc.store.GetContact(ctx, p, slug, resource)
}

// ContactsByName atiende addressbook-multiget: los nombres que no son validos o no existen se
// omiten, y quien llama los informa como 404.
func (uc *UseCase) ContactsByName(ctx context.Context, p domain.Principal, slug string, resources []string, withData bool) ([]domain.Contact, error) {
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
	return uc.store.GetContacts(ctx, p, slug, valid, uc.cfg.Limits.Read(withData))
}

// Query atiende addressbook-query: recorre los contactos de la libreta de uno en uno y conserva solo los
// que cumplen el filtro, de modo que la memoria es la de las coincidencias (acotadas por MaxReadBytes) y
// no la de la libreta. Un vCard guardado que ya no se pueda leer no aparece en el resultado, y la
// consulta se detiene cuando el contexto se cancela.
func (uc *UseCase) Query(ctx context.Context, p domain.Principal, slug string, filter domain.Filter, limit int) (domain.Addressbook, []domain.Contact, error) {
	if !domain.ValidSlug(slug) {
		return domain.Addressbook{}, nil, domain.ErrNotFound
	}
	out := []domain.Contact{}
	matched := 0
	book, err := uc.store.EachContact(ctx, p, slug, func(c domain.Contact) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		card, err := domain.ParseStoredVCard(c.VCard)
		if err != nil {
			uc.logger.Warn("mail-dav: vCard guardado ilegible, se omite de la consulta",
				zap.String("contact_id", c.ID.String()), zap.Error(err))
			return true, nil
		}
		if !filter.Matches(card) {
			return true, nil
		}
		if matched += c.Size; matched > uc.cfg.Limits.MaxReadBytes {
			return false, domain.ErrResultTooLarge
		}
		out = append(out, c)
		return limit <= 0 || len(out) < limit, nil
	})
	if err != nil {
		return book, nil, err
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
	created, err = uc.store.PutContact(ctx, p, slug, c, cond, uc.cfg.Limits.Write(uc.cfg.Limits.MaxContactsPerMailbox))
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

// Sync atiende sync-collection. Con token vacio devuelve todos los contactos y ninguna baja. Sin withData
// los contactos llevan solo sus metadatos y su tamano.
func (uc *UseCase) Sync(ctx context.Context, p domain.Principal, slug, token string, withData bool) (SyncResult, error) {
	if !domain.ValidSlug(slug) {
		return SyncResult{}, domain.ErrNotFound
	}
	bookID, seq, initial, err := domain.ParseSyncToken(token)
	if err != nil {
		return SyncResult{}, err
	}
	if initial {
		book, contacts, err := uc.store.ListContacts(ctx, p, slug, uc.cfg.Limits.Read(withData))
		if err != nil {
			return SyncResult{}, err
		}
		return SyncResult{Token: domain.SyncToken(book.ID, book.SyncSeq), Changed: contacts}, nil
	}
	book, changed, removed, err := uc.store.ChangesSince(ctx, p, slug, seq, uc.cfg.Limits.Read(withData))
	if err != nil {
		return SyncResult{}, err
	}
	if book.ID != bookID {
		return SyncResult{}, domain.ErrInvalidSyncToken
	}
	return SyncResult{Token: domain.SyncToken(book.ID, book.SyncSeq), Changed: changed, Removed: removed}, nil
}
