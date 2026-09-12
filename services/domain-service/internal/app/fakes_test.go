package app

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
)

// fakeRepo guarda dominios y comprobaciones en memoria, con la misma semantica de
// errores que el repositorio real.
type fakeRepo struct {
	mu      sync.Mutex
	domains map[uuid.UUID]*domain.Domain
	checks  []domain.DNSCheck
	updates int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{domains: make(map[uuid.UUID]*domain.Domain)}
}

func clone(d *domain.Domain) *domain.Domain {
	c := *d
	return &c
}

func (r *fakeRepo) Create(_ context.Context, d *domain.Domain) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.domains {
		if existing.TenantID == d.TenantID && existing.Domain == d.Domain {
			return domain.ErrDomainAlreadyExists
		}
	}
	d.CreatedAt, d.UpdatedAt = time.Now(), time.Now()
	r.domains[d.ID] = clone(d)
	return nil
}

func (r *fakeRepo) GetByID(_ context.Context, tenantID, id uuid.UUID) (*domain.Domain, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.domains[id]
	if !ok || d.TenantID != tenantID {
		return nil, domain.ErrDomainNotFound
	}
	return clone(d), nil
}

func (r *fakeRepo) GetByName(_ context.Context, tenantID uuid.UUID, name string) (*domain.Domain, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.domains {
		if d.TenantID == tenantID && d.Domain == name {
			return clone(d), nil
		}
	}
	return nil, domain.ErrDomainNotFound
}

func (r *fakeRepo) List(_ context.Context, tenantID uuid.UUID, offset, limit int) ([]*domain.Domain, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Domain
	for _, d := range r.domains {
		if d.TenantID == tenantID {
			out = append(out, clone(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	total := int64(len(out))
	if offset > len(out) {
		return nil, total, nil
	}
	out = out[offset:]
	if limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

func (r *fakeRepo) Update(_ context.Context, d *domain.Domain) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.domains[d.ID]; !ok {
		return domain.ErrDomainNotFound
	}
	r.updates++
	r.domains[d.ID] = clone(d)
	return nil
}

func (r *fakeRepo) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.domains[id]
	if !ok || d.TenantID != tenantID {
		return domain.ErrDomainNotFound
	}
	delete(r.domains, id)
	return nil
}

func (r *fakeRepo) ListForRecheck(_ context.Context, tenantID uuid.UUID, pendingSince time.Time) ([]*domain.Domain, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Domain
	for _, d := range r.domains {
		if d.TenantID != tenantID {
			continue
		}
		if d.Status == domain.StatusVerified || (d.Status == domain.StatusPending && !d.CreatedAt.Before(pendingSince)) {
			out = append(out, clone(d))
		}
	}
	return out, nil
}

func (r *fakeRepo) ListWithExpiredPreviousDKIM(_ context.Context, tenantID uuid.UUID, rotatedBefore time.Time) ([]*domain.Domain, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Domain
	for _, d := range r.domains {
		if d.TenantID == tenantID && d.HasPreviousDKIM() && d.DKIMRotatedAt.Before(rotatedBefore) {
			out = append(out, clone(d))
		}
	}
	return out, nil
}

func (r *fakeRepo) SaveChecks(_ context.Context, checks []domain.DNSCheck) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, checks...)
	return nil
}

func (r *fakeRepo) LatestChecks(_ context.Context, tenantID, domainID uuid.UUID) ([]domain.DNSCheck, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	latest := make(map[domain.RecordKind]domain.DNSCheck)
	for _, c := range r.checks {
		if c.TenantID != tenantID || c.DomainID != domainID {
			continue
		}
		if prev, ok := latest[c.Record]; !ok || c.CheckedAt.After(prev.CheckedAt) {
			latest[c.Record] = c
		}
	}
	out := make([]domain.DNSCheck, 0, len(latest))
	for _, c := range latest {
		out = append(out, c)
	}
	return out, nil
}

func (r *fakeRepo) PruneChecks(_ context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var kept []domain.DNSCheck
	var pruned int64
	for _, c := range r.checks {
		if c.TenantID == tenantID && c.CheckedAt.Before(before) {
			pruned++
			continue
		}
		kept = append(kept, c)
	}
	r.checks = kept
	return pruned, nil
}

// fakeDNS responde por nombre. Un nombre ausente es "sin registros"; errs simula un
// resolver que no responde.
type fakeDNS struct {
	txt  map[string][]string
	mx   map[string][]domain.MXRecord
	errs map[string]error
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{txt: map[string][]string{}, mx: map[string][]domain.MXRecord{}, errs: map[string]error{}}
}

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	return f.txt[name], nil
}

func (f *fakeDNS) LookupMX(_ context.Context, name string) ([]domain.MXRecord, error) {
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	return f.mx[name], nil
}

// publishZone deja en el DNS falso todos los registros esperados del dominio.
func (f *fakeDNS) publishZone(uc *UseCase, d *domain.Domain) {
	for _, rec := range uc.ExpectedRecords(d) {
		switch rec.Type {
		case "MX":
			f.mx[rec.Host] = []domain.MXRecord{{Host: uc.platform.MXHostname, Priority: 10}}
		default:
			f.txt[rec.Host] = []string{rec.Value}
		}
	}
}

type activation struct {
	domain string
	active bool
}

type fakeDirectory struct {
	calls []activation
	err   error
}

func (f *fakeDirectory) SetActivation(_ context.Context, _ uuid.UUID, name string, active bool) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, activation{domain: name, active: active})
	return nil
}

type published struct {
	domain string
	keys   []ports.DKIMKey
}

type fakeSecurity struct {
	published []published
	retired   []string
	deleted   []string
	err       error
}

func (f *fakeSecurity) PublishDKIM(_ context.Context, _ uuid.UUID, name string, keys []ports.DKIMKey) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, published{domain: name, keys: keys})
	return nil
}

func (f *fakeSecurity) RetireDKIM(_ context.Context, _ uuid.UUID, name, selector string) error {
	if f.err != nil {
		return f.err
	}
	f.retired = append(f.retired, name+"/"+selector)
	return nil
}

func (f *fakeSecurity) DeleteDKIM(_ context.Context, _ uuid.UUID, name string) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeSecurity) lastPublished() published {
	if len(f.published) == 0 {
		return published{}
	}
	return f.published[len(f.published)-1]
}

type fakePublisher struct {
	subjects []string
	err      error
}

func (f *fakePublisher) record(subject string) error {
	if f.err != nil {
		return f.err
	}
	f.subjects = append(f.subjects, subject)
	return nil
}

func (f *fakePublisher) DomainCreated(context.Context, *domain.Domain) error {
	return f.record("domains.domain.created")
}
func (f *fakePublisher) DomainVerified(context.Context, *domain.Domain) error {
	return f.record("domains.domain.verified")
}
func (f *fakePublisher) DomainFailed(context.Context, *domain.Domain) error {
	return f.record("domains.domain.failed")
}
func (f *fakePublisher) DomainDeleted(context.Context, *domain.Domain) error {
	return f.record("domains.domain.deleted")
}
func (f *fakePublisher) DKIMRotated(context.Context, *domain.Domain) error {
	return f.record("domains.domain.dkim_rotated")
}

func (f *fakePublisher) count(subject string) int {
	n := 0
	for _, s := range f.subjects {
		if s == subject {
			n++
		}
	}
	return n
}

var errDown = errors.New("servicio caido")
