package app

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

// Los falsos guardan en memoria y solo implementan lo que ejercitan los casos de uso
// probados; el resto devuelve el valor vacio para satisfacer la interfaz.

// fakeTx imita la transaccion: un error dentro de fn deshace lo que escribieron los
// falsos (snapshot), como la base deshace la fila de negocio y la de outbox a la vez.
type fakeTx struct {
	calls      int
	active     bool
	rolledBack int
	snapshot   func() (restore func())
}

func (f *fakeTx) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	f.calls++
	var restore func()
	if f.snapshot != nil {
		restore = f.snapshot()
	}
	f.active = true
	err := fn(ctx)
	f.active = false
	if err != nil {
		f.rolledBack++
		if restore != nil {
			restore()
		}
	}
	return err
}

type fakeSecrets struct{}

func (fakeSecrets) HashPassword(plain string) (string, error) { return "hash(" + plain + ")", nil }
func (fakeSecrets) GenerateAppPassword() (string, error) {
	return "generada-en-servidor-de-32-chars!", nil
}

// fakeEvents hace de outbox: anota cada evento, apunta aparte los que se emitieron fuera
// de la transaccion y puede fallar como fallaria el INSERT de outbox.Enqueue.
type fakeEvents struct {
	tx       *fakeTx
	subjects []string
	outside  []string
	fail     error
	// credentials anota cada mail.mailbox.credentials_changed con su buzon y su credencial.
	credentials []credentialEvent
	// changed anota el changed de cada mail.mailbox.updated, en orden.
	changed [][]domain.MailboxAttr
}

type credentialEvent struct {
	username   string
	credential domain.Credential
	changed    []domain.MailboxAttr
}

func (f *fakeEvents) record(s string) error {
	if f.fail != nil {
		return f.fail
	}
	if f.tx == nil || !f.tx.active {
		f.outside = append(f.outside, s)
	}
	f.subjects = append(f.subjects, s)
	return nil
}
func (f *fakeEvents) AliasDomainUpdated(context.Context, *domain.AliasDomain) error {
	return f.record("mail.alias_domain.updated")
}
func (f *fakeEvents) DomainCreated(context.Context, *domain.Domain) error {
	return f.record("mail.domain.created")
}
func (f *fakeEvents) DomainUpdated(context.Context, *domain.Domain) error {
	return f.record("mail.domain.updated")
}
func (f *fakeEvents) DomainDeleted(context.Context, *domain.Domain) error {
	return f.record("mail.domain.deleted")
}
func (f *fakeEvents) DomainActivated(context.Context, *domain.Domain) error {
	return f.record("mail.domain.activated")
}
func (f *fakeEvents) AliasDomainCreated(context.Context, *domain.AliasDomain) error {
	return f.record("mail.alias_domain.created")
}
func (f *fakeEvents) AliasDomainDeleted(context.Context, *domain.AliasDomain) error {
	return f.record("mail.alias_domain.deleted")
}
func (f *fakeEvents) MailboxCreated(context.Context, *domain.Mailbox) error {
	return f.record("mail.mailbox.created")
}
func (f *fakeEvents) MailboxUpdated(_ context.Context, _ *domain.Mailbox, changed []domain.MailboxAttr) error {
	if err := f.record("mail.mailbox.updated"); err != nil {
		return err
	}
	f.changed = append(f.changed, changed)
	return nil
}
func (f *fakeEvents) MailboxDeleted(context.Context, *domain.Mailbox) error {
	return f.record("mail.mailbox.deleted")
}
func (f *fakeEvents) MailboxCredentialsChanged(_ context.Context, m *domain.Mailbox, c domain.Credential, changed []domain.MailboxAttr) error {
	if err := f.record("mail.mailbox.credentials_changed"); err != nil {
		return err
	}
	f.credentials = append(f.credentials, credentialEvent{username: m.Username, credential: c, changed: changed})
	return nil
}
func (f *fakeEvents) AliasCreated(context.Context, *domain.Alias) error {
	return f.record("mail.alias.created")
}
func (f *fakeEvents) AliasUpdated(context.Context, *domain.Alias) error {
	return f.record("mail.alias.updated")
}
func (f *fakeEvents) AliasDeleted(context.Context, *domain.Alias) error {
	return f.record("mail.alias.deleted")
}

// ── Dominios ──────────────────────────────────────────────────────────────────

type fakeDomains struct {
	items   []*domain.Domain
	updated int
	// foreign simula dominios de OTRA empresa: name_in_use los ve, GetByName no.
	foreign    []string
	lastFilter ports.DomainFilter
}

func (f *fakeDomains) List(_ context.Context, tenantID uuid.UUID, filter ports.DomainFilter, page ports.Page) ([]domain.Domain, int64, error) {
	f.lastFilter = filter
	var out []domain.Domain
	for _, d := range f.items {
		if d.TenantID == tenantID {
			out = append(out, *d)
		}
	}
	return out, int64(len(out)), nil
}

func (f *fakeDomains) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Domain, error) {
	for _, d := range f.items {
		if d.TenantID == tenantID && d.ID == id {
			return d, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDomains) GetByName(_ context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	for _, d := range f.items {
		if d.TenantID == tenantID && d.Domain == name {
			return d, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeDomains) Create(_ context.Context, d *domain.Domain) error {
	f.items = append(f.items, d)
	return nil
}

func (f *fakeDomains) Update(_ context.Context, d *domain.Domain) error { f.updated++; return nil }

func (f *fakeDomains) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	for i, d := range f.items {
		if d.TenantID == tenantID && d.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeDomains) NameInUse(_ context.Context, name string) (bool, error) {
	for _, d := range f.items {
		if d.Domain == name {
			return true, nil
		}
	}
	for _, n := range f.foreign {
		if n == name {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeDomains) Usage(context.Context, uuid.UUID, string) (int64, int64, int64, error) {
	return 0, 0, 0, nil
}

type fakeAliasDomains struct{ items []*domain.AliasDomain }

func (f *fakeAliasDomains) List(context.Context, uuid.UUID, ports.Page) ([]domain.AliasDomain, int64, error) {
	return nil, 0, nil
}
func (f *fakeAliasDomains) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.AliasDomain, error) {
	for _, a := range f.items {
		if a.TenantID == tenantID && a.ID == id {
			return a, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeAliasDomains) Create(_ context.Context, a *domain.AliasDomain) error {
	f.items = append(f.items, a)
	return nil
}
func (f *fakeAliasDomains) Update(context.Context, *domain.AliasDomain) error { return nil }
func (f *fakeAliasDomains) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	for i, a := range f.items {
		if a.TenantID == tenantID && a.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func (f *fakeAliasDomains) ExistsByName(_ context.Context, tenantID uuid.UUID, name string) (bool, error) {
	for _, a := range f.items {
		if a.TenantID == tenantID && a.AliasDomain == name {
			return true, nil
		}
	}
	return false, nil
}

// ── Buzones ───────────────────────────────────────────────────────────────────

type fakeMailboxes struct {
	items        []*domain.Mailbox
	aliases      *fakeAliases
	quotaDeleted []string
	lastFilter   ports.MailboxFilter
	lastTenant   uuid.UUID
	lastPage     ports.Page
	// deletions son las marcas de baja (username -> cuando), con el reloj now de la base simulada.
	deletions map[string]time.Time
	now       time.Time
}

func (f *fakeMailboxes) RecordDeletion(_ context.Context, m *domain.Mailbox) error {
	if f.deletions == nil {
		f.deletions = map[string]time.Time{}
	}
	f.deletions[m.Username] = f.now
	return nil
}

func (f *fakeMailboxes) DeletionPending(_ context.Context, username string, hold time.Duration) (bool, error) {
	at, ok := f.deletions[username]
	return ok && f.now.Sub(at) < hold, nil
}

func (f *fakeMailboxes) ExistingIDs(_ context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, id := range ids {
		for _, m := range f.items {
			if m.TenantID == tenantID && m.ID == id {
				out = append(out, id)
			}
		}
	}
	return out, nil
}

func (f *fakeMailboxes) List(_ context.Context, tenantID uuid.UUID, filter ports.MailboxFilter, page ports.Page) ([]domain.Mailbox, int64, error) {
	f.lastFilter, f.lastTenant, f.lastPage = filter, tenantID, page
	var out []domain.Mailbox
	for _, m := range f.items {
		if m.TenantID != tenantID || (filter.ActiveOnly && m.Active != 1) {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(m.Username+" "+m.DisplayName), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, *m)
	}
	return out, int64(len(out)), nil
}

func (f *fakeMailboxes) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Mailbox, error) {
	for _, m := range f.items {
		if m.TenantID == tenantID && m.ID == id {
			return m, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeMailboxes) GetByUsername(_ context.Context, tenantID uuid.UUID, username string) (*domain.Mailbox, error) {
	for _, m := range f.items {
		if m.TenantID == tenantID && m.Username == username {
			return m, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeMailboxes) Create(_ context.Context, m *domain.Mailbox) error {
	f.items = append(f.items, m)
	return nil
}

func (f *fakeMailboxes) Update(context.Context, *domain.Mailbox) error { return nil }
func (f *fakeMailboxes) UpdatePassword(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (f *fakeMailboxes) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	for i, m := range f.items {
		if m.TenantID == tenantID && m.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeMailboxes) CountByDomain(_ context.Context, tenantID uuid.UUID, name string) (int64, error) {
	var n int64
	for _, m := range f.items {
		if m.TenantID == tenantID && m.Domain == name {
			n++
		}
	}
	return n, nil
}

func (f *fakeMailboxes) QuotaSumByDomain(_ context.Context, tenantID uuid.UUID, name string, exclude uuid.UUID) (int64, error) {
	var sum int64
	for _, m := range f.items {
		if m.TenantID == tenantID && m.Domain == name && m.ID != exclude {
			sum += m.QuotaBytes
		}
	}
	return sum, nil
}

func (f *fakeMailboxes) Quota(context.Context, uuid.UUID, uuid.UUID) (*domain.QuotaUsage, error) {
	return &domain.QuotaUsage{}, nil
}

func (f *fakeMailboxes) DeleteQuotaUsage(_ context.Context, _ uuid.UUID, username string) error {
	f.quotaDeleted = append(f.quotaDeleted, username)
	return nil
}

func (f *fakeMailboxes) Logins(context.Context, uuid.UUID, string, int) ([]domain.SASLLogin, error) {
	return nil, nil
}

func (f *fakeMailboxes) AddressInUse(_ context.Context, tenantID uuid.UUID, address string) (bool, error) {
	for _, m := range f.items {
		if m.TenantID == tenantID && m.Username == address {
			return true, nil
		}
	}
	if f.aliases != nil {
		for _, a := range f.aliases.items {
			if a.TenantID == tenantID && a.Address == address {
				return true, nil
			}
		}
	}
	return false, nil
}

type fakeAppPasswords struct {
	items       []*domain.AppPassword
	deactivated int
	deleted     int
}

func (f *fakeAppPasswords) List(_ context.Context, tenantID, mailboxID uuid.UUID) ([]domain.AppPassword, error) {
	var out []domain.AppPassword
	for _, p := range f.items {
		if p.TenantID == tenantID && p.MailboxID == mailboxID {
			out = append(out, *p)
		}
	}
	return out, nil
}

// find devuelve la posicion de la contrasena, o -1.
func (f *fakeAppPasswords) find(tenantID, mailboxID, id uuid.UUID) int {
	for i, p := range f.items {
		if p.TenantID == tenantID && p.MailboxID == mailboxID && p.ID == id {
			return i
		}
	}
	return -1
}

// Get devuelve una copia, como la base: el caso de uso cambia la suya y solo Update la guarda.
func (f *fakeAppPasswords) Get(_ context.Context, tenantID, mailboxID, id uuid.UUID) (*domain.AppPassword, error) {
	i := f.find(tenantID, mailboxID, id)
	if i < 0 {
		return nil, domain.ErrNotFound
	}
	c := *f.items[i]
	return &c, nil
}
func (f *fakeAppPasswords) Create(_ context.Context, p *domain.AppPassword) error {
	f.items = append(f.items, p)
	return nil
}
func (f *fakeAppPasswords) Update(_ context.Context, p *domain.AppPassword) error {
	i := f.find(p.TenantID, p.MailboxID, p.ID)
	if i < 0 {
		return domain.ErrNotFound
	}
	c := *p
	f.items[i] = &c
	return nil
}
func (f *fakeAppPasswords) Delete(_ context.Context, tenantID, mailboxID, id uuid.UUID) error {
	i := f.find(tenantID, mailboxID, id)
	if i < 0 {
		return domain.ErrNotFound
	}
	f.items = append(f.items[:i], f.items[i+1:]...)
	return nil
}
func (f *fakeAppPasswords) DeleteByMailbox(context.Context, uuid.UUID, uuid.UUID) error {
	f.deleted++
	return nil
}
func (f *fakeAppPasswords) DeactivateByMailbox(_ context.Context, tenantID, mailboxID uuid.UUID) (int64, error) {
	f.deactivated++
	var n int64
	for _, p := range f.items {
		if p.TenantID == tenantID && p.MailboxID == mailboxID && p.Active {
			p.Active = false
			n++
		}
	}
	return n, nil
}

// fakeVacation guarda la respuesta automatica por empresa y buzon, como la restriccion UNIQUE.
type fakeVacation struct {
	items   map[string]*domain.VacationReply
	upserts int
	deleted []string
}

func vacationKey(tenantID uuid.UUID, username string) string {
	return tenantID.String() + "|" + username
}

func (f *fakeVacation) ByUsername(_ context.Context, tenantID uuid.UUID, username string) (*domain.VacationReply, error) {
	v, ok := f.items[vacationKey(tenantID, username)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *v
	return &c, nil
}

func (f *fakeVacation) Upsert(_ context.Context, v *domain.VacationReply) error {
	f.upserts++
	if f.items == nil {
		f.items = map[string]*domain.VacationReply{}
	}
	c := *v
	f.items[vacationKey(v.TenantID, v.Username)] = &c
	return nil
}

func (f *fakeVacation) DeleteByUsername(_ context.Context, tenantID uuid.UUID, username string) error {
	f.deleted = append(f.deleted, username)
	delete(f.items, vacationKey(tenantID, username))
	return nil
}

// fakeLocator resuelve el buzon por su nombre en toda la celda, como el rol de servicio.
type fakeLocator struct{ h *harness }

func (f *fakeLocator) Locate(_ context.Context, username string) (uuid.UUID, uuid.UUID, error) {
	for _, m := range f.h.mailboxes.items {
		if m.Username == username {
			return m.TenantID, m.ID, nil
		}
	}
	return uuid.Nil, uuid.Nil, domain.ErrNotFound
}

type fakeSieve struct{ deleted int }

func (f *fakeSieve) ByUsername(context.Context, uuid.UUID, string) ([]domain.SieveFilter, error) {
	return nil, nil
}
func (f *fakeSieve) Replace(context.Context, uuid.UUID, string, string, *domain.SieveFilter) error {
	return nil
}
func (f *fakeSieve) DeleteByUsername(context.Context, uuid.UUID, string) error {
	f.deleted++
	return nil
}

// ── Aliases ───────────────────────────────────────────────────────────────────

type fakeAliases struct{ items []*domain.Alias }

func (f *fakeAliases) List(context.Context, uuid.UUID, ports.Page) ([]domain.Alias, int64, error) {
	return nil, 0, nil
}
func (f *fakeAliases) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Alias, error) {
	for _, a := range f.items {
		if a.TenantID == tenantID && a.ID == id {
			return a, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeAliases) Create(_ context.Context, a *domain.Alias) error {
	f.items = append(f.items, a)
	return nil
}
func (f *fakeAliases) Update(context.Context, *domain.Alias) error { return nil }
func (f *fakeAliases) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	for i, a := range f.items {
		if a.TenantID == tenantID && a.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func (f *fakeAliases) CountByDomain(_ context.Context, tenantID uuid.UUID, name string) (int64, error) {
	var n int64
	for _, a := range f.items {
		if a.TenantID == tenantID && a.Domain == name {
			n++
		}
	}
	return n, nil
}

type fakeSpamAliases struct{ deletedByGoto int }

func (f *fakeSpamAliases) List(context.Context, uuid.UUID, ports.Page) ([]domain.SpamAlias, int64, error) {
	return nil, 0, nil
}
func (f *fakeSpamAliases) Get(context.Context, uuid.UUID, uuid.UUID) (*domain.SpamAlias, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeSpamAliases) Create(context.Context, *domain.SpamAlias) error    { return nil }
func (f *fakeSpamAliases) Update(context.Context, *domain.SpamAlias) error    { return nil }
func (f *fakeSpamAliases) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *fakeSpamAliases) DeleteByGoto(context.Context, uuid.UUID, string) error {
	f.deletedByGoto++
	return nil
}

type fakeSenderACL struct{ deletedByUser int }

func (f *fakeSenderACL) List(context.Context, uuid.UUID, ports.Page) ([]domain.SenderACL, int64, error) {
	return nil, 0, nil
}
func (f *fakeSenderACL) Get(context.Context, uuid.UUID, uuid.UUID) (*domain.SenderACL, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeSenderACL) Create(context.Context, *domain.SenderACL) error    { return nil }
func (f *fakeSenderACL) Update(context.Context, *domain.SenderACL) error    { return nil }
func (f *fakeSenderACL) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *fakeSenderACL) DeleteByLoggedInAs(context.Context, uuid.UUID, string) error {
	f.deletedByUser++
	return nil
}

type fakeRelayhosts struct{ items []*domain.Relayhost }

func (f *fakeRelayhosts) List(context.Context, uuid.UUID, ports.Page) ([]domain.Relayhost, int64, error) {
	return nil, 0, nil
}
func (f *fakeRelayhosts) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Relayhost, error) {
	for _, r := range f.items {
		if r.TenantID == tenantID && r.ID == id {
			return r, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeRelayhosts) Create(context.Context, *domain.Relayhost, string) error  { return nil }
func (f *fakeRelayhosts) Update(context.Context, *domain.Relayhost, *string) error { return nil }
func (f *fakeRelayhosts) Delete(context.Context, uuid.UUID, uuid.UUID) error       { return nil }

type fakeTransports struct{ items []*domain.Transport }

func (f *fakeTransports) List(context.Context, uuid.UUID, ports.Page) ([]domain.Transport, int64, error) {
	return nil, 0, nil
}
func (f *fakeTransports) Get(_ context.Context, scope ports.TransportScope, id uuid.UUID) (*domain.Transport, error) {
	for _, t := range f.items {
		if t.ID == id && (t.TenantID == nil || *t.TenantID == scope.TenantID) {
			return t, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeTransports) Create(_ context.Context, t *domain.Transport, _ string) error {
	f.items = append(f.items, t)
	return nil
}
func (f *fakeTransports) Update(context.Context, ports.TransportScope, *domain.Transport, *string) error {
	return nil
}
func (f *fakeTransports) Delete(context.Context, ports.TransportScope, uuid.UUID) error { return nil }

// ── Bajas de empresa ──────────────────────────────────────────────────────────

// fakeRetirements hace de mail.tenant_retirements y de las sentencias que apagan el directorio de
// una empresa, sobre los falsos del harness y con su reloj.
type fakeRetirements struct {
	h         *harness
	retired   map[uuid.UUID]time.Time
	now       time.Time
	shared    int
	exclusive int
	// settings es lo que apagaria la siguiente llamada en las tablas sin evento.
	settings domain.RetirementCounts
}

func (f *fakeRetirements) HoldShared(_ context.Context, tenantID uuid.UUID) (bool, error) {
	f.shared++
	_, retired := f.retired[tenantID]
	return retired, nil
}

func (f *fakeRetirements) HoldExclusive(context.Context, uuid.UUID) error {
	f.exclusive++
	return nil
}

func (f *fakeRetirements) Mark(_ context.Context, tenantID uuid.UUID) (time.Time, error) {
	if at, ok := f.retired[tenantID]; ok {
		return at, nil
	}
	f.retired[tenantID] = f.now
	return f.now, nil
}

func (f *fakeRetirements) DeactivateDomains(_ context.Context, tenantID uuid.UUID) ([]domain.Domain, error) {
	var out []domain.Domain
	for _, d := range f.h.domains.items {
		if d.TenantID == tenantID && d.Active {
			d.Active = false
			out = append(out, *d)
		}
	}
	return out, nil
}

func (f *fakeRetirements) DeactivateAliasDomains(_ context.Context, tenantID uuid.UUID) ([]domain.AliasDomain, error) {
	var out []domain.AliasDomain
	for _, a := range f.h.aliasDomains.items {
		if a.TenantID == tenantID && a.Active {
			a.Active = false
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeRetirements) DeactivateMailboxes(_ context.Context, tenantID uuid.UUID) ([]domain.Mailbox, error) {
	var out []domain.Mailbox
	for _, m := range f.h.mailboxes.items {
		if m.TenantID == tenantID && m.Active != domain.ActiveOff {
			m.Active = domain.ActiveOff
			out = append(out, *m)
		}
	}
	return out, nil
}

func (f *fakeRetirements) DeactivateAliases(_ context.Context, tenantID uuid.UUID) ([]domain.Alias, error) {
	var out []domain.Alias
	for _, a := range f.h.aliases.items {
		if a.TenantID == tenantID && a.Active != domain.ActiveOff {
			a.Active = domain.ActiveOff
			out = append(out, *a)
		}
	}
	return out, nil
}

func (f *fakeRetirements) DeactivateSettings(context.Context, uuid.UUID) (domain.RetirementCounts, error) {
	c := f.settings
	f.settings = domain.RetirementCounts{}
	return c, nil
}

type fakeMTASTS struct {
	items   map[string]*domain.MTASTSPolicy
	upserts int
	deleted []string
}

func (f *fakeMTASTS) ByDomain(_ context.Context, tenantID uuid.UUID, name string) (*domain.MTASTSPolicy, error) {
	p, ok := f.items[name]
	if !ok || p.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	c := *p
	return &c, nil
}

func (f *fakeMTASTS) States(context.Context, uuid.UUID, ports.Page) ([]domain.MTASTSState, int64, error) {
	return nil, 0, nil
}

func (f *fakeMTASTS) Upsert(_ context.Context, p *domain.MTASTSPolicy) error {
	f.upserts++
	if f.items == nil {
		f.items = map[string]*domain.MTASTSPolicy{}
	}
	p.UpdatedAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	c := *p
	f.items[p.Domain] = &c
	return nil
}

func (f *fakeMTASTS) DeleteByDomain(_ context.Context, tenantID uuid.UUID, name string) error {
	f.deleted = append(f.deleted, name)
	if p, ok := f.items[name]; ok && p.TenantID == tenantID {
		delete(f.items, name)
	}
	return nil
}

// fakeMTASTSPublisher sirve las filas de fakeMTASTS como la consulta publica: solo el dominio activo
// y con un modo distinto de none.
type fakeMTASTSPublisher struct{ h *harness }

func (f fakeMTASTSPublisher) Published(_ context.Context, name string) (*domain.MTASTSPolicy, error) {
	p, ok := f.h.mtaSTS.items[name]
	if !ok || !p.Mode.Published() {
		return nil, domain.ErrNotFound
	}
	for _, d := range f.h.domains.items {
		if d.Domain == name && d.TenantID == p.TenantID && d.Active {
			c := *p
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}

// fakeMX responde lo que se le fije para todo dominio y cuenta las consultas.
type fakeMX struct {
	hosts []string
	err   error
	calls int
}

func (f *fakeMX) LookupMX(context.Context, string) ([]string, error) {
	f.calls++
	return f.hosts, f.err
}

// harness cablea un caso de uso con todos los falsos; los repositorios que un test no
// necesita quedan en su valor vacio.
const platformMXForTests = "mx.plataforma.example"

type harness struct {
	uc           *UseCase
	tx           *fakeTx
	domains      *fakeDomains
	aliasDomains *fakeAliasDomains
	mailboxes    *fakeMailboxes
	appPasswords *fakeAppPasswords
	sieve        *fakeSieve
	vacation     *fakeVacation
	mtaSTS       *fakeMTASTS
	mx           *fakeMX
	aliases      *fakeAliases
	spamAliases  *fakeSpamAliases
	senderACL    *fakeSenderACL
	relayhosts   *fakeRelayhosts
	transports   *fakeTransports
	retirements  *fakeRetirements
	events       *fakeEvents
}

func newHarness() *harness {
	h := &harness{
		tx: &fakeTx{}, domains: &fakeDomains{}, aliasDomains: &fakeAliasDomains{}, aliases: &fakeAliases{},
		appPasswords: &fakeAppPasswords{}, sieve: &fakeSieve{}, vacation: &fakeVacation{}, mtaSTS: &fakeMTASTS{}, mx: &fakeMX{hosts: []string{platformMXForTests}}, spamAliases: &fakeSpamAliases{},
		senderACL: &fakeSenderACL{}, relayhosts: &fakeRelayhosts{}, transports: &fakeTransports{}, events: &fakeEvents{},
	}
	h.mailboxes = &fakeMailboxes{aliases: h.aliases, now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	locator := &fakeLocator{h: h}
	h.retirements = &fakeRetirements{h: h, retired: map[uuid.UUID]time.Time{}, now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	h.events.tx = h.tx
	h.tx.snapshot = h.snapshot
	h.uc = New(Deps{
		Tx: h.tx, Domains: h.domains, AliasDomains: h.aliasDomains, Mailboxes: h.mailboxes,
		AppPasswords: h.appPasswords, Sieve: h.sieve, Vacation: h.vacation, Locator: locator, Aliases: h.aliases, SpamAliases: h.spamAliases,
		SenderACL: h.senderACL, Relayhosts: h.relayhosts, Transports: h.transports, Retirements: h.retirements,
		MTASTS: h.mtaSTS, MTASTSPublic: fakeMTASTSPublisher{h: h}, MX: h.mx, PlatformMX: platformMXForTests,
		MailboxRecreateHold: testRecreateHold, Secrets: fakeSecrets{}, Events: h.events,
	})
	return h
}

// testRecreateHold es la retencion de una direccion recien borrada con la que corren las pruebas.
const testRecreateHold = 15 * time.Minute

func (h *harness) addDomain(tenantID uuid.UUID, name string, limits domain.DomainLimits) *domain.Domain {
	d := &domain.Domain{
		ID: uuid.New(), TenantID: tenantID, Domain: name, Active: true,
		MaxAliases: limits.MaxAliases, MaxMailboxes: limits.MaxMailboxes, DefaultQuotaBytes: limits.DefaultQuotaBytes,
		MaxQuotaBytes: limits.MaxQuotaBytes, QuotaBytes: limits.QuotaBytes,
	}
	h.domains.items = append(h.domains.items, d)
	return d
}

func (h *harness) addMailbox(tenantID uuid.UUID, username string, quota int64) *domain.Mailbox {
	at := strings.LastIndex(username, "@")
	m := &domain.Mailbox{
		ID: uuid.New(), TenantID: tenantID, Username: username, LocalPart: username[:at], Domain: username[at+1:],
		QuotaBytes: quota, Active: domain.ActiveOn,
	}
	h.mailboxes.items = append(h.mailboxes.items, m)
	return m
}

// addAppPassword da al buzon una contrasena de aplicacion activa con todos los protocolos;
// change la ajusta antes de guardarla.
func (h *harness) addAppPassword(m *domain.Mailbox, change func(*domain.AppPassword)) *domain.AppPassword {
	p := &domain.AppPassword{
		ID: uuid.New(), TenantID: m.TenantID, MailboxID: m.ID, Name: "movil", PasswordHash: "hash",
		IMAPAccess: true, POP3Access: true, SMTPAccess: true, SieveAccess: true, DAVAccess: true, Active: true,
	}
	if change != nil {
		change(p)
	}
	h.appPasswords.items = append(h.appPasswords.items, p)
	return p
}

// snapshot copia lo que escriben los casos de uso probados y devuelve como restaurarlo.
func (h *harness) snapshot() func() {
	domains, aliasDomains := cloneAll(h.domains.items), cloneAll(h.aliasDomains.items)
	mailboxes, aliases := cloneAll(h.mailboxes.items), cloneAll(h.aliases.items)
	appPasswords := cloneAll(h.appPasswords.items)
	vacation := map[string]*domain.VacationReply{}
	for k, v := range h.vacation.items {
		c := *v
		vacation[k] = &c
	}
	mtaSTS := map[string]*domain.MTASTSPolicy{}
	for k, v := range h.mtaSTS.items {
		c := *v
		mtaSTS[k] = &c
	}
	subjects := append([]string(nil), h.events.subjects...)
	credentials := append([]credentialEvent(nil), h.events.credentials...)
	retired := maps.Clone(h.retirements.retired)
	return func() {
		h.domains.items, h.aliasDomains.items = domains, aliasDomains
		h.mailboxes.items, h.aliases.items = mailboxes, aliases
		h.appPasswords.items = appPasswords
		h.vacation.items = vacation
		h.mtaSTS.items = mtaSTS
		h.events.subjects, h.events.credentials = subjects, credentials
		h.retirements.retired = retired
	}
}

func cloneAll[T any](items []*T) []*T {
	out := make([]*T, len(items))
	for i, it := range items {
		c := *it
		out[i] = &c
	}
	return out
}

func (h *harness) published(subject string) int {
	n := 0
	for _, s := range h.events.subjects {
		if s == subject {
			n++
		}
	}
	return n
}
