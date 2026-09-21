// Package apptest tiene los dobles con los que se prueban el caso de uso y el adaptador HTTP: un almacen
// en memoria que aplica las mismas reglas que el de Postgres (aislamiento por buzon, precondiciones,
// limites y registro de cambios) y un verificador de credenciales fijo.
package apptest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

// Account es un buzon de prueba con su contrasena.
type Account struct {
	Password  string
	Principal domain.Principal
}

// Auth acepta las contrasenas de sus cuentas y cuenta las llamadas, para comprobar que cada peticion
// pregunta a mail-auth.
type Auth struct {
	mu       sync.Mutex
	accounts map[string]Account
	Calls    int
	LastIP   string
	// Unavailable simula mail-auth caido.
	Unavailable bool
}

func NewAuth(accounts ...Account) *Auth {
	a := &Auth{accounts: map[string]Account{}}
	for _, acc := range accounts {
		a.accounts[acc.Principal.Username] = acc
	}
	return a
}

func (a *Auth) Authenticate(_ context.Context, username, password, remoteIP string) (domain.Principal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Calls++
	a.LastIP = remoteIP
	if a.Unavailable {
		return domain.Principal{}, domain.ErrUnavailable
	}
	name, ok := domain.NormalizeUsername(username)
	acc, found := a.accounts[name]
	if !ok || !found || acc.Password != password {
		return domain.Principal{}, domain.ErrInvalidCredentials
	}
	return acc.Principal, nil
}

// Binder no necesita base: deja el contexto como esta. Err simula una empresa cuya base no se puede abrir.
type Binder struct{ Err error }

func (b Binder) Bind(ctx context.Context, _ domain.Principal) (context.Context, error) {
	return ctx, b.Err
}

type owner struct{ tenant, mailbox uuid.UUID }

type book struct {
	domain.Addressbook
	changes []change
}

type change struct {
	seq      int64
	resource string
	deleted  bool
}

// Store guarda todo en memoria, acotado por (empresa, buzon) igual que las consultas del repositorio real.
type Store struct {
	mu       sync.Mutex
	books    map[owner]map[string]*book
	contacts map[uuid.UUID]map[string]domain.Contact
	Now      func() time.Time
}

func NewStore() *Store {
	return &Store{books: map[owner]map[string]*book{}, contacts: map[uuid.UUID]map[string]domain.Contact{}, Now: time.Now}
}

func key(p domain.Principal) owner { return owner{p.TenantID, p.MailboxID} }

func (s *Store) find(p domain.Principal, slug string) (*book, error) {
	b, ok := s.books[key(p)][slug]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return b, nil
}

func (s *Store) ListAddressbooks(_ context.Context, p domain.Principal) ([]domain.Addressbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.Addressbook{}
	for _, b := range s.books[key(p)] {
		out = append(out, b.Addressbook)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (s *Store) GetAddressbook(_ context.Context, p domain.Principal, slug string) (domain.Addressbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.find(p, slug)
	if err != nil {
		return domain.Addressbook{}, err
	}
	return b.Addressbook, nil
}

func (s *Store) CreateAddressbook(_ context.Context, p domain.Principal, in domain.Addressbook, maxBooks int) (domain.Addressbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.books[key(p)][in.Slug]; ok {
		return domain.Addressbook{}, domain.ErrAlreadyExists
	}
	if len(s.books[key(p)]) >= maxBooks {
		return domain.Addressbook{}, domain.ErrAddressbookLimit
	}
	if s.books[key(p)] == nil {
		s.books[key(p)] = map[string]*book{}
	}
	in.TenantID, in.MailboxID = p.TenantID, p.MailboxID
	in.CreatedAt, in.UpdatedAt = s.Now(), s.Now()
	s.books[key(p)][in.Slug] = &book{Addressbook: in}
	s.contacts[in.ID] = map[string]domain.Contact{}
	return in, nil
}

func (s *Store) DeleteAddressbook(_ context.Context, p domain.Principal, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.find(p, slug)
	if err != nil {
		return err
	}
	delete(s.contacts, b.ID)
	delete(s.books[key(p)], slug)
	return nil
}

func (s *Store) DeleteMailboxData(_ context.Context, p domain.Principal) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	books := s.books[key(p)]
	for _, b := range books {
		delete(s.contacts, b.ID)
	}
	delete(s.books, key(p))
	return len(books), nil
}

func (s *Store) sorted(id uuid.UUID) []domain.Contact {
	out := make([]domain.Contact, 0, len(s.contacts[id]))
	for _, c := range s.contacts[id] {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ResourceName < out[j].ResourceName })
	return out
}

func (s *Store) ListContacts(_ context.Context, p domain.Principal, slug string) (domain.Addressbook, []domain.Contact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.find(p, slug)
	if err != nil {
		return domain.Addressbook{}, nil, err
	}
	return b.Addressbook, s.sorted(b.ID), nil
}

func (s *Store) GetContact(_ context.Context, p domain.Principal, slug, resource string) (domain.Contact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.find(p, slug)
	if err != nil {
		return domain.Contact{}, err
	}
	c, ok := s.contacts[b.ID][resource]
	if !ok {
		return domain.Contact{}, domain.ErrNotFound
	}
	return c, nil
}

func (s *Store) GetContacts(_ context.Context, p domain.Principal, slug string, resources []string) ([]domain.Contact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.find(p, slug)
	if err != nil {
		return nil, err
	}
	var out []domain.Contact
	for _, name := range resources {
		if c, ok := s.contacts[b.ID][name]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) mailboxContacts(p domain.Principal) int {
	n := 0
	for _, b := range s.books[key(p)] {
		n += len(s.contacts[b.ID])
	}
	return n
}

func (s *Store) record(b *book, resource string, deleted bool, maxChanges int) {
	b.SyncSeq++
	b.UpdatedAt = s.Now()
	b.changes = append(b.changes, change{seq: b.SyncSeq, resource: resource, deleted: deleted})
	if floor := b.SyncSeq - int64(maxChanges); floor > b.ChangesFloor {
		b.ChangesFloor = floor
		kept := b.changes[:0]
		for _, c := range b.changes {
			if c.seq > floor {
				kept = append(kept, c)
			}
		}
		b.changes = kept
	}
}

func (s *Store) PutContact(_ context.Context, p domain.Principal, slug string, c domain.Contact, cond domain.Precondition, maxContacts, maxChanges int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.find(p, slug)
	if err != nil {
		return false, err
	}
	existing, exists := s.contacts[b.ID][c.ResourceName]
	var current *string
	if exists {
		current = &existing.ETag
	}
	if err := cond.Check(current); err != nil {
		return false, err
	}
	if exists && existing.ETag == c.ETag {
		return false, nil
	}
	for name, other := range s.contacts[b.ID] {
		if other.UID == c.UID && name != c.ResourceName {
			return false, &domain.UIDConflictError{Resource: name}
		}
	}
	if !exists && s.mailboxContacts(p) >= maxContacts {
		return false, domain.ErrContactLimit
	}
	c.AddressbookID, c.TenantID, c.MailboxID = b.ID, p.TenantID, p.MailboxID
	c.CreatedAt, c.UpdatedAt = s.Now(), s.Now()
	if exists {
		c.ID, c.CreatedAt = existing.ID, existing.CreatedAt
	}
	s.contacts[b.ID][c.ResourceName] = c
	s.record(b, c.ResourceName, false, maxChanges)
	return !exists, nil
}

func (s *Store) DeleteContact(_ context.Context, p domain.Principal, slug, resource string, cond domain.Precondition, maxChanges int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.find(p, slug)
	if err != nil {
		return err
	}
	existing, ok := s.contacts[b.ID][resource]
	if !ok {
		return domain.ErrNotFound
	}
	if err := cond.Check(&existing.ETag); err != nil {
		return err
	}
	delete(s.contacts[b.ID], resource)
	s.record(b, resource, true, maxChanges)
	return nil
}

func (s *Store) ChangesSince(_ context.Context, p domain.Principal, slug string, seq int64) (domain.Addressbook, []domain.Contact, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.find(p, slug)
	if err != nil {
		return domain.Addressbook{}, nil, nil, err
	}
	if seq < b.ChangesFloor || seq > b.SyncSeq {
		return domain.Addressbook{}, nil, nil, domain.ErrInvalidSyncToken
	}
	latest := map[string]bool{}
	for _, c := range b.changes {
		if c.seq > seq {
			latest[c.resource] = c.deleted
		}
	}
	var changed []domain.Contact
	var removed []string
	for name, deleted := range latest {
		if deleted {
			removed = append(removed, name)
		} else if c, ok := s.contacts[b.ID][name]; ok {
			changed = append(changed, c)
		}
	}
	sort.Strings(removed)
	sort.Slice(changed, func(i, j int) bool { return changed[i].ResourceName < changed[j].ResourceName })
	return b.Addressbook, changed, removed, nil
}
