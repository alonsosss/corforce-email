// Package apptest contiene dobles en memoria de los puertos del servicio. Solo lo
// importan los tests (de app y de los adaptadores HTTP) para no duplicar los dobles.
package apptest

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
)

// Directory implementa ports.DirectoryReader en memoria.
type Directory struct {
	Mailboxes    map[string]domain.Mailbox
	Aliases      map[string]string
	AliasDomains map[string]string
	Domains      map[string]uuid.UUID
	BCC          map[string]string
	Internal     []domain.InternalAlias
	Lookups      int
}

func NewDirectory() *Directory {
	return &Directory{
		Mailboxes:    map[string]domain.Mailbox{},
		Aliases:      map[string]string{},
		AliasDomains: map[string]string{},
		Domains:      map[string]uuid.UUID{},
		BCC:          map[string]string{},
	}
}

func (f *Directory) AliasGoto(_ context.Context, address string) (string, bool, error) {
	f.Lookups++
	g, ok := f.Aliases[address]
	return g, ok, nil
}

func (f *Directory) AliasDomainTarget(_ context.Context, d string) (string, bool, error) {
	f.Lookups++
	t, ok := f.AliasDomains[d]
	return t, ok, nil
}

func (f *Directory) MailboxByUsername(_ context.Context, u string) (*domain.Mailbox, error) {
	f.Lookups++
	m, ok := f.Mailboxes[u]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &m, nil
}

// Domains son los dominios activos. Un dominio alias cuenta como activo solo si su
// destino lo esta, igual que la consulta real sobre las vistas.
func (f *Directory) ActiveDomains(ctx context.Context) ([]string, error) {
	out := make([]string, 0, len(f.Domains)+len(f.AliasDomains))
	for d := range f.Domains {
		out = append(out, d)
	}
	for d := range f.AliasDomains {
		if ok, _ := f.DomainActive(ctx, d); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *Directory) DomainActive(_ context.Context, d string) (bool, error) {
	if _, ok := f.Domains[d]; ok {
		return true, nil
	}
	target, ok := f.AliasDomains[d]
	if !ok {
		return false, nil
	}
	_, ok = f.Domains[target]
	return ok, nil
}

func (f *Directory) ObjectOwnedBy(_ context.Context, tenantID uuid.UUID, object string) (bool, error) {
	if m, ok := f.Mailboxes[object]; ok {
		return m.TenantID == tenantID, nil
	}
	if t, ok := f.Domains[object]; ok {
		return t == tenantID, nil
	}
	return false, nil
}

func (f *Directory) DomainOwner(_ context.Context, d string) (uuid.UUID, bool, error) {
	t, ok := f.Domains[d]
	return t, ok, nil
}

func (f *Directory) AliasDomainsOf(_ context.Context, target string) ([]string, error) {
	var out []string
	for a, t := range f.AliasDomains {
		if t == target {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *Directory) AliasesTargeting(_ context.Context, username string) ([]string, error) {
	var out []string
	for a, g := range f.Aliases {
		for _, dest := range strings.Split(g, ",") {
			if dest == username {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

func (f *Directory) InternalAliases(_ context.Context) ([]domain.InternalAlias, error) {
	return append([]domain.InternalAlias(nil), f.Internal...), nil
}

func (f *Directory) BCCDestination(_ context.Context, kind, localDest string) (string, bool, error) {
	d, ok := f.BCC[kind+"|"+localDest]
	return d, ok, nil
}

// PolicyReader implementa ports.PolicyReader en memoria.
type PolicyReader struct {
	Scores       []domain.SpamScore
	Lists        []domain.AddressListEntry
	Maps         []domain.SettingsMap
	UpdatedAt    time.Time
	Footers      map[string]domain.DomainFooter
	FwdHosts     []domain.ForwardingHost
	RL           []domain.RateLimit
	Tags         []domain.MailboxTags
	SMTP         []domain.SMTPAccess
	QSettings    map[uuid.UUID]domain.QuarantineSettings
	FirewallNets []domain.FirewallNetwork
	FwOptions    *domain.FirewallOptions
}

func (f *PolicyReader) AllSMTPAccess(context.Context) ([]domain.SMTPAccess, error) { return f.SMTP, nil }
func (f *PolicyReader) DeleteSMTPAccessByUsername(_ context.Context, username string) error {
	kept := f.SMTP[:0]
	for _, a := range f.SMTP {
		if a.Username != username {
			kept = append(kept, a)
		}
	}
	f.SMTP = kept
	return nil
}
func (f *PolicyReader) AllFirewallNetworks(context.Context) ([]domain.FirewallNetwork, error) {
	return append([]domain.FirewallNetwork(nil), f.FirewallNets...), nil
}
func (f *PolicyReader) FirewallOptions(context.Context) (*domain.FirewallOptions, error) {
	if f.FwOptions == nil {
		return nil, domain.ErrNotFound
	}
	o := *f.FwOptions
	return &o, nil
}

func NewPolicyReader() *PolicyReader {
	return &PolicyReader{Footers: map[string]domain.DomainFooter{}, QSettings: map[uuid.UUID]domain.QuarantineSettings{}}
}

func (f *PolicyReader) AllSpamScores(context.Context) ([]domain.SpamScore, error) { return f.Scores, nil }
func (f *PolicyReader) AllAddressLists(context.Context) ([]domain.AddressListEntry, error) {
	return f.Lists, nil
}
func (f *PolicyReader) AllActiveSettingsMaps(context.Context) ([]domain.SettingsMap, error) {
	return f.Maps, nil
}
func (f *PolicyReader) PolicyUpdatedAt(context.Context) (time.Time, error) { return f.UpdatedAt, nil }
func (f *PolicyReader) FooterByDomain(_ context.Context, d string) (*domain.DomainFooter, error) {
	ft, ok := f.Footers[d]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &ft, nil
}
func (f *PolicyReader) AllForwardingHosts(context.Context) ([]domain.ForwardingHost, error) {
	return f.FwdHosts, nil
}
func (f *PolicyReader) AllRateLimits(context.Context) ([]domain.RateLimit, error) { return f.RL, nil }
func (f *PolicyReader) AllMailboxTags(context.Context) ([]domain.MailboxTags, error) {
	return f.Tags, nil
}
func (f *PolicyReader) AllQuarantineSettings(context.Context) ([]domain.QuarantineSettings, error) {
	out := make([]domain.QuarantineSettings, 0, len(f.QSettings))
	for _, s := range f.QSettings {
		out = append(out, s)
	}
	return out, nil
}
func (f *PolicyReader) QuarantineSettingsFor(_ context.Context, id uuid.UUID) (*domain.QuarantineSettings, error) {
	s, ok := f.QSettings[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &s, nil
}
func (f *PolicyReader) DeleteMailboxTagsByUsername(context.Context, string) error { return nil }

// Store implementa ports.EngineStore en memoria.
type Store struct {
	mu     sync.Mutex
	Hashes map[string]map[string]string
	Values map[string]string
	Lists  map[string][]string
	Down   bool
}

func NewStore() *Store {
	return &Store{Hashes: map[string]map[string]string{}, Values: map[string]string{}, Lists: map[string][]string{}}
}

func (s *Store) Ping(context.Context) error {
	if s.Down {
		return domain.ErrRedisUnavailable
	}
	return nil
}

func (s *Store) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Values[key]
	return v, ok, nil
}

func (s *Store) Keys(_ context.Context, pattern string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range []map[string]bool{keysOf(s.Hashes), keysOf(s.Values), keysOf(s.Lists)} {
		for k := range m {
			if ok, _ := filepath.Match(pattern, k); ok {
				out = append(out, k)
			}
		}
	}
	return out, nil
}

func keysOf[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func (s *Store) Del(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.Hashes, k)
		delete(s.Values, k)
		delete(s.Lists, k)
	}
	return nil
}

func (s *Store) HSet(_ context.Context, key, field, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Hashes[key] == nil {
		s.Hashes[key] = map[string]string{}
	}
	s.Hashes[key][field] = value
	return nil
}

func (s *Store) HDel(_ context.Context, key string, fields ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range fields {
		delete(s.Hashes[key], f)
	}
	return nil
}

func (s *Store) HGet(_ context.Context, key, field string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Hashes[key][field]
	return v, ok, nil
}

func (s *Store) HGetAll(_ context.Context, key string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.Hashes[key] {
		out[k] = v
	}
	return out, nil
}

func (s *Store) HKeys(_ context.Context, key, pattern string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for f := range s.Hashes[key] {
		if ok, _ := filepath.Match(pattern, f); ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *Store) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Values[key] = value
	return nil
}

func (s *Store) LPushTrim(_ context.Context, key, value string, maxLen int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Lists[key] = append([]string{value}, s.Lists[key]...)
	if int64(len(s.Lists[key])) > maxLen {
		s.Lists[key] = s.Lists[key][:maxLen]
	}
	return nil
}

// Quarantine implementa ports.QuarantineRepository en memoria.
type Quarantine struct {
	Items []domain.QuarantineItem
}

func (q *Quarantine) Insert(_ context.Context, it *domain.QuarantineItem) error {
	if it.ID == uuid.Nil {
		it.ID = uuid.New()
	}
	q.Items = append(q.Items, *it)
	return nil
}

func (q *Quarantine) PruneRcpt(_ context.Context, tenantID uuid.UUID, rcpt string, keep int) (int64, error) {
	var kept []domain.QuarantineItem
	count := 0
	var pruned int64
	for i := len(q.Items) - 1; i >= 0; i-- {
		it := q.Items[i]
		if it.TenantID == tenantID && it.Rcpt == rcpt {
			count++
			if count > keep {
				pruned++
				continue
			}
		}
		kept = append([]domain.QuarantineItem{it}, kept...)
	}
	q.Items = kept
	return pruned, nil
}

func (q *Quarantine) PruneAged(context.Context, uuid.UUID, int) (int64, error) { return 0, nil }
func (q *Quarantine) List(_ context.Context, tenantID uuid.UUID, _ domain.QuarantineFilter) ([]domain.QuarantineItem, int64, error) {
	var out []domain.QuarantineItem
	for _, it := range q.Items {
		if it.TenantID == tenantID {
			out = append(out, it)
		}
	}
	return out, int64(len(out)), nil
}
func (q *Quarantine) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.QuarantineItem, error) {
	for _, it := range q.Items {
		if it.TenantID == tenantID && it.ID == id {
			return &it, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (q *Quarantine) GetMessage(ctx context.Context, tenantID, id uuid.UUID) ([]byte, error) {
	it, err := q.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	return it.Msg, nil
}
func (q *Quarantine) LockForRelease(ctx context.Context, tenantID, id uuid.UUID) (*domain.QuarantineItem, error) {
	return q.Get(ctx, tenantID, id)
}

func (q *Quarantine) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	for i, it := range q.Items {
		if it.TenantID == tenantID && it.ID == id {
			q.Items = append(q.Items[:i], q.Items[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

// Tx implementa las dos transacciones (TransactRLS y Transact) en memoria: marca si hay
// una abierta y, si fn falla, deshace lo que capturo Snapshot, como la base deshace la
// fila de negocio y la de la outbox a la vez.
type Tx struct {
	Active     bool
	Calls      int
	RolledBack int
	Snapshot   func() (restore func())
}

func (t *Tx) run(ctx context.Context, fn func(ctx context.Context) error) error {
	t.Calls++
	var restore func()
	if t.Snapshot != nil {
		restore = t.Snapshot()
	}
	t.Active = true
	err := fn(ctx)
	t.Active = false
	if err != nil {
		t.RolledBack++
		if restore != nil {
			restore()
		}
	}
	return err
}

func (t *Tx) TransactRLS(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.run(ctx, fn)
}

func (t *Tx) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.run(ctx, fn)
}

// Publisher implementa ports.EventPublisher: anota los subjects, aparte los emitidos fuera
// de una transaccion, y puede fallar como fallaria el INSERT de la outbox.
type Publisher struct {
	Tx       *Tx
	Subjects []string
	Outside  []string
	Fail     error
}

func (p *Publisher) record(subject string) error {
	if p.Fail != nil {
		return p.Fail
	}
	if p.Tx == nil || !p.Tx.Active {
		p.Outside = append(p.Outside, subject)
	}
	p.Subjects = append(p.Subjects, subject)
	return nil
}

func (p *Publisher) QuarantineStored(context.Context, *domain.QuarantineItem) error {
	return p.record(domain.SubjectQuarantineStored)
}

func (p *Publisher) QuarantineReleased(context.Context, *domain.QuarantineItem, string) error {
	return p.record(domain.SubjectQuarantineReleased)
}

// Documents implementa ports.DocumentStamps en memoria. Varios casos de uso pueden
// compartirlo como si fueran replicas sobre la misma base.
type Documents struct {
	mu     sync.Mutex
	Stamps map[string]domain.DocumentStamp
	Saves  int
}

func NewDocuments() *Documents { return &Documents{Stamps: map[string]domain.DocumentStamp{}} }

func (d *Documents) LockDocument(_ context.Context, document string) (*domain.DocumentStamp, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.Stamps[document]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (d *Documents) SaveDocument(_ context.Context, s domain.DocumentStamp) (domain.DocumentStamp, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Saves++
	d.Stamps[s.Document] = s
	return s, nil
}

// Firewall implementa ports.FirewallRepository escribiendo en el PolicyReader en memoria,
// que es donde lo leen el caso de uso y la reconciliacion.
type Firewall struct{ Reader *PolicyReader }

func (f *Firewall) CreateFirewallNetwork(_ context.Context, n *domain.FirewallNetwork) error {
	for _, existing := range f.Reader.FirewallNets {
		if existing.Network == n.Network {
			return domain.ErrAlreadyExists
		}
	}
	n.ID = uuid.New()
	n.CreatedAt = time.Now()
	f.Reader.FirewallNets = append(f.Reader.FirewallNets, *n)
	return nil
}

func (f *Firewall) DeleteFirewallNetwork(_ context.Context, id uuid.UUID) (*domain.FirewallNetwork, error) {
	for i, n := range f.Reader.FirewallNets {
		if n.ID == id {
			f.Reader.FirewallNets = append(f.Reader.FirewallNets[:i], f.Reader.FirewallNets[i+1:]...)
			return &n, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *Firewall) UpsertFirewallOptions(_ context.Context, o *domain.FirewallOptions) error {
	saved := *o
	f.Reader.FwOptions = &saved
	return nil
}
