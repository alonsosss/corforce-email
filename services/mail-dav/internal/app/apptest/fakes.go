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

// colls son las colecciones (libretas o calendarios) de todos los buzones, con su registro de cambios.
type colls map[owner]map[string]*book

// Store guarda todo en memoria, acotado por (empresa, buzon) igual que las consultas del repositorio real.
// Implementa ports.Store y ports.CalendarStore.
type Store struct {
	mu        sync.Mutex
	books     colls
	calendars colls
	contacts  map[uuid.UUID]map[string]domain.Contact
	events    map[uuid.UUID]map[string]domain.Event
	Now       func() time.Time
}

func NewStore() *Store {
	return &Store{
		books: colls{}, calendars: colls{},
		contacts: map[uuid.UUID]map[string]domain.Contact{}, events: map[uuid.UUID]map[string]domain.Event{},
		Now: time.Now,
	}
}

func key(p domain.Principal) owner { return owner{p.TenantID, p.MailboxID} }

func (c colls) find(p domain.Principal, slug string) (*book, error) {
	b, ok := c[key(p)][slug]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return b, nil
}

func (c colls) list(p domain.Principal) []domain.Addressbook {
	out := []domain.Addressbook{}
	for _, b := range c[key(p)] {
		out = append(out, b.Addressbook)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

func (s *Store) create(c colls, p domain.Principal, in domain.Addressbook, max int, limit error) (domain.Addressbook, error) {
	if _, ok := c[key(p)][in.Slug]; ok {
		return domain.Addressbook{}, domain.ErrAlreadyExists
	}
	if len(c[key(p)]) >= max {
		return domain.Addressbook{}, limit
	}
	if c[key(p)] == nil {
		c[key(p)] = map[string]*book{}
	}
	in.TenantID, in.MailboxID = p.TenantID, p.MailboxID
	in.CreatedAt, in.UpdatedAt = s.Now(), s.Now()
	c[key(p)][in.Slug] = &book{Addressbook: in}
	return in, nil
}

// changesSince aplica las mismas reglas que el repositorio: los nombres que existen y cambiaron despues
// de seq (live) y los borrados (removed), o domain.ErrInvalidSyncToken.
func (b *book) changesSince(seq int64) (live, removed []string, err error) {
	if seq < b.ChangesFloor || seq > b.SyncSeq {
		return nil, nil, domain.ErrInvalidSyncToken
	}
	latest := map[string]bool{}
	for _, c := range b.changes {
		if c.seq > seq {
			latest[c.resource] = c.deleted
		}
	}
	for name, deleted := range latest {
		if deleted {
			removed = append(removed, name)
		} else {
			live = append(live, name)
		}
	}
	sort.Strings(removed)
	sort.Strings(live)
	return live, removed, nil
}

func (s *Store) ListAddressbooks(_ context.Context, p domain.Principal) ([]domain.Addressbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.books.list(p), nil
}

func (s *Store) GetAddressbook(_ context.Context, p domain.Principal, slug string) (domain.Addressbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.books.find(p, slug)
	if err != nil {
		return domain.Addressbook{}, err
	}
	return b.Addressbook, nil
}

func (s *Store) CreateAddressbook(_ context.Context, p domain.Principal, in domain.Addressbook, maxBooks int) (domain.Addressbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.create(s.books, p, in, maxBooks, domain.ErrAddressbookLimit)
	if err == nil {
		s.contacts[out.ID] = map[string]domain.Contact{}
	}
	return out, err
}

func (s *Store) DeleteAddressbook(_ context.Context, p domain.Principal, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.books.find(p, slug)
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
	b, err := s.books.find(p, slug)
	if err != nil {
		return domain.Addressbook{}, nil, err
	}
	return b.Addressbook, s.sorted(b.ID), nil
}

func (s *Store) GetContact(_ context.Context, p domain.Principal, slug, resource string) (domain.Contact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.books.find(p, slug)
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
	b, err := s.books.find(p, slug)
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
	b, err := s.books.find(p, slug)
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
	b, err := s.books.find(p, slug)
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
	b, err := s.books.find(p, slug)
	if err != nil {
		return domain.Addressbook{}, nil, nil, err
	}
	live, removed, err := b.changesSince(seq)
	if err != nil {
		return domain.Addressbook{}, nil, nil, err
	}
	var changed []domain.Contact
	for _, name := range live {
		if c, ok := s.contacts[b.ID][name]; ok {
			changed = append(changed, c)
		}
	}
	return b.Addressbook, changed, removed, nil
}

func (s *Store) ListCalendars(_ context.Context, p domain.Principal) ([]domain.Calendar, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calendars.list(p), nil
}

func (s *Store) GetCalendar(_ context.Context, p domain.Principal, slug string) (domain.Calendar, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.calendars.find(p, slug)
	if err != nil {
		return domain.Calendar{}, err
	}
	return b.Addressbook, nil
}

func (s *Store) CreateCalendar(_ context.Context, p domain.Principal, in domain.Calendar, maxCalendars int) (domain.Calendar, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.create(s.calendars, p, in, maxCalendars, domain.ErrCalendarLimit)
	if err == nil {
		s.events[out.ID] = map[string]domain.Event{}
	}
	return out, err
}

func (s *Store) DeleteCalendar(_ context.Context, p domain.Principal, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.calendars.find(p, slug)
	if err != nil {
		return err
	}
	delete(s.events, b.ID)
	delete(s.calendars[key(p)], slug)
	return nil
}

func (s *Store) DeleteMailboxCalendars(_ context.Context, p domain.Principal) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cals := s.calendars[key(p)]
	for _, c := range cals {
		delete(s.events, c.ID)
	}
	delete(s.calendars, key(p))
	return len(cals), nil
}

func (s *Store) sortedEvents(id uuid.UUID) []domain.Event {
	out := make([]domain.Event, 0, len(s.events[id]))
	for _, e := range s.events[id] {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ResourceName < out[j].ResourceName })
	return out
}

// ListEvents descarta por la ventana igual que la consulta real: un evento sin fin conocido nunca se descarta.
func (s *Store) ListEvents(_ context.Context, p domain.Principal, slug string, w domain.EventWindow) (domain.Calendar, []domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.calendars.find(p, slug)
	if err != nil {
		return domain.Calendar{}, nil, err
	}
	out := []domain.Event{}
	for _, e := range s.sortedEvents(b.ID) {
		if w.End != nil && !e.FirstStart.Before(*w.End) {
			continue
		}
		if w.Start != nil && e.LastEnd != nil && e.LastEnd.Before(*w.Start) {
			continue
		}
		out = append(out, e)
	}
	return b.Addressbook, out, nil
}

func (s *Store) GetEvent(_ context.Context, p domain.Principal, slug, resource string) (domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.calendars.find(p, slug)
	if err != nil {
		return domain.Event{}, err
	}
	e, ok := s.events[b.ID][resource]
	if !ok {
		return domain.Event{}, domain.ErrNotFound
	}
	return e, nil
}

func (s *Store) GetEvents(_ context.Context, p domain.Principal, slug string, resources []string) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.calendars.find(p, slug)
	if err != nil {
		return nil, err
	}
	var out []domain.Event
	for _, name := range resources {
		if e, ok := s.events[b.ID][name]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Store) mailboxEvents(p domain.Principal) int {
	n := 0
	for _, b := range s.calendars[key(p)] {
		n += len(s.events[b.ID])
	}
	return n
}

func (s *Store) PutEvent(_ context.Context, p domain.Principal, slug string, e domain.Event, cond domain.Precondition, maxEvents, maxChanges int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.calendars.find(p, slug)
	if err != nil {
		return false, err
	}
	existing, exists := s.events[b.ID][e.ResourceName]
	var current *string
	if exists {
		current = &existing.ETag
	}
	if err := cond.Check(current); err != nil {
		return false, err
	}
	if exists && existing.ETag == e.ETag {
		return false, nil
	}
	for name, other := range s.events[b.ID] {
		if other.UID == e.UID && name != e.ResourceName {
			return false, &domain.UIDConflictError{Resource: name}
		}
	}
	if !exists && s.mailboxEvents(p) >= maxEvents {
		return false, domain.ErrEventLimit
	}
	e.CalendarID, e.TenantID, e.MailboxID = b.ID, p.TenantID, p.MailboxID
	e.CreatedAt, e.UpdatedAt = s.Now(), s.Now()
	if exists {
		e.ID, e.CreatedAt = existing.ID, existing.CreatedAt
	}
	s.events[b.ID][e.ResourceName] = e
	s.record(b, e.ResourceName, false, maxChanges)
	return !exists, nil
}

func (s *Store) DeleteEvent(_ context.Context, p domain.Principal, slug, resource string, cond domain.Precondition, maxChanges int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.calendars.find(p, slug)
	if err != nil {
		return err
	}
	existing, ok := s.events[b.ID][resource]
	if !ok {
		return domain.ErrNotFound
	}
	if err := cond.Check(&existing.ETag); err != nil {
		return err
	}
	delete(s.events[b.ID], resource)
	s.record(b, resource, true, maxChanges)
	return nil
}

func (s *Store) EventChangesSince(_ context.Context, p domain.Principal, slug string, seq int64) (domain.Calendar, []domain.Event, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.calendars.find(p, slug)
	if err != nil {
		return domain.Calendar{}, nil, nil, err
	}
	live, removed, err := b.changesSince(seq)
	if err != nil {
		return domain.Calendar{}, nil, nil, err
	}
	var changed []domain.Event
	for _, name := range live {
		if e, ok := s.events[b.ID][name]; ok {
			changed = append(changed, e)
		}
	}
	return b.Addressbook, changed, removed, nil
}
