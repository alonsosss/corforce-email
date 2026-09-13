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
	Scores    []domain.SpamScore
	Lists     []domain.AddressListEntry
	Maps      []domain.SettingsMap
	UpdatedAt time.Time
	Footers   map[string]domain.DomainFooter
	FwdHosts  []domain.ForwardingHost
	RL        []domain.RateLimit
	Tags      []domain.MailboxTags
	QSettings map[uuid.UUID]domain.QuarantineSettings
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
func (q *Quarantine) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	for i, it := range q.Items {
		if it.TenantID == tenantID && it.ID == id {
			q.Items = append(q.Items[:i], q.Items[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

// Publisher implementa ports.EventPublisher y recuerda los subjects emitidos.
type Publisher struct{ Subjects []string }

func (p *Publisher) Publish(subject string, _ map[string]any) {
	p.Subjects = append(p.Subjects, subject)
}
