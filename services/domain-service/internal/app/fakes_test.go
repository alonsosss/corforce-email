package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
)

// fakeRepo guarda dominios y comprobaciones en memoria, con la misma semantica de
// errores que el repositorio real. now hace de reloj de la base: created_at sale del mismo
// reloj que el caso de uso, o las ventanas de tiempo dependerian del dia en que se corre.
type fakeRepo struct {
	mu        sync.Mutex
	domains   map[uuid.UUID]*domain.Domain
	checks    []domain.DNSCheck
	rotations []domain.DKIMRotation
	updates   int
	now       func() time.Time
	// keyMu hace de cerrojo por dominio de WithDKIMLock (uno para todos basta en las pruebas).
	keyMu sync.Mutex
}

func newFakeRepo(now func() time.Time) *fakeRepo {
	return &fakeRepo{domains: make(map[uuid.UUID]*domain.Domain), now: now}
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
	d.CreatedAt, d.UpdatedAt = r.now(), r.now()
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

// Update copia solo el estado, como el repositorio real: las claves DKIM no se tocan.
func (r *fakeRepo) Update(_ context.Context, d *domain.Domain) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.domains[d.ID]
	if !ok {
		return domain.ErrDomainNotFound
	}
	r.updates++
	c := clone(stored)
	c.Purpose, c.Status, c.VerifiedAt, c.LastCheckedAt = d.Purpose, d.Status, d.VerifiedAt, d.LastCheckedAt
	c.DMARCPolicy, c.DirectoryDeactivationPending = d.DMARCPolicy, d.DirectoryDeactivationPending
	r.domains[d.ID] = c
	return nil
}

// WithDKIMLock deshace lo que fn escribio si devuelve error, como la transaccion real.
func (r *fakeRepo) WithDKIMLock(ctx context.Context, tenantID, id uuid.UUID, fn func(context.Context, *domain.Domain) error) error {
	r.keyMu.Lock()
	defer r.keyMu.Unlock()
	r.mu.Lock()
	domains := make(map[uuid.UUID]*domain.Domain, len(r.domains))
	for k, v := range r.domains {
		domains[k] = clone(v)
	}
	rotations := append([]domain.DKIMRotation(nil), r.rotations...)
	r.mu.Unlock()
	d, err := r.GetByID(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if err := fn(ctx, d); err != nil {
		r.mu.Lock()
		r.domains, r.rotations = domains, rotations
		r.mu.Unlock()
		return err
	}
	return nil
}

func (r *fakeRepo) SaveDKIMKeys(_ context.Context, d *domain.Domain, expectedSelector string, rotation *domain.DKIMRotation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.domains[d.ID]
	if !ok || stored.TenantID != d.TenantID {
		return domain.ErrDomainNotFound
	}
	if stored.DKIMSelector != expectedSelector {
		return domain.ErrDKIMKeysChanged
	}
	c := clone(stored)
	c.DKIMSelector, c.DKIMPrivateKeyEnc, c.DKIMPublicKey, c.DKIMKeyBits = d.DKIMSelector, d.DKIMPrivateKeyEnc, d.DKIMPublicKey, d.DKIMKeyBits
	c.DKIMPreviousSelector, c.DKIMPreviousPrivateKeyEnc, c.DKIMPreviousPublicKey = d.DKIMPreviousSelector, d.DKIMPreviousPrivateKeyEnc, d.DKIMPreviousPublicKey
	c.DKIMRotatedAt, c.DKIMPreviousSignedAt, c.DKIMConfirmedAt = d.DKIMRotatedAt, d.DKIMPreviousSignedAt, d.DKIMConfirmedAt
	c.DKIMRevocationPending = d.DKIMRevocationPending
	r.domains[d.ID] = c
	rot := *rotation
	rot.RevokedSelectors = append([]string(nil), rotation.RevokedSelectors...)
	r.rotations = append(r.rotations, rot)
	return nil
}

// withKeys aplica fn a la fila si su selector (actual o anterior, segun previous) sigue siendo
// selector: la misma condicion que las consultas reales.
func (r *fakeRepo) withKeys(tenantID, id uuid.UUID, selector string, previous bool, fn func(*domain.Domain)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.domains[id]
	if !ok || stored.TenantID != tenantID {
		return nil
	}
	current := stored.DKIMSelector
	if previous {
		current = stored.DKIMPreviousSelector
	}
	if current != selector {
		return nil
	}
	c := clone(stored)
	fn(c)
	r.domains[id] = c
	return nil
}

func (r *fakeRepo) ClearPreviousDKIM(_ context.Context, tenantID, id uuid.UUID, selector string) error {
	return r.withKeys(tenantID, id, selector, true, func(d *domain.Domain) { d.ClearPreviousDKIM() })
}

func (r *fakeRepo) MarkPreviousDKIMSigning(_ context.Context, tenantID, id uuid.UUID, selector string, at time.Time) error {
	return r.withKeys(tenantID, id, selector, true, func(d *domain.Domain) {
		if d.DKIMPreviousSignedAt == nil || at.After(*d.DKIMPreviousSignedAt) {
			d.DKIMPreviousSignedAt = &at
		}
	})
}

func (r *fakeRepo) ConfirmDKIM(_ context.Context, tenantID, id uuid.UUID, selector string, at time.Time) error {
	return r.withKeys(tenantID, id, selector, false, func(d *domain.Domain) {
		if d.DKIMConfirmedAt == nil {
			d.DKIMConfirmedAt = &at
		}
	})
}

func (r *fakeRepo) CompleteDKIMRevocation(_ context.Context, tenantID, id uuid.UUID, selector string) error {
	return r.withKeys(tenantID, id, selector, false, func(d *domain.Domain) { d.DKIMRevocationPending = false })
}

func (r *fakeRepo) ListDKIMRotations(_ context.Context, tenantID, domainID uuid.UUID, limit int) ([]domain.DKIMRotation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.DKIMRotation
	for i := len(r.rotations) - 1; i >= 0 && len(out) < limit; i-- {
		if rot := r.rotations[i]; rot.TenantID == tenantID && rot.DomainID == domainID {
			out = append(out, rot)
		}
	}
	return out, nil
}

func (r *fakeRepo) UsedDKIMSelectors(_ context.Context, tenantID, domainID uuid.UUID) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rot := range r.rotations {
		if rot.TenantID != tenantID || rot.DomainID != domainID {
			continue
		}
		out = append(out, rot.Selector)
		if rot.PreviousSelector != "" {
			out = append(out, rot.PreviousSelector)
		}
		out = append(out, rot.RevokedSelectors...)
	}
	return out, nil
}

func (r *fakeRepo) ListPendingDKIMRevocation(_ context.Context, tenantID uuid.UUID) ([]*domain.Domain, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Domain
	for _, d := range r.domains {
		if d.TenantID == tenantID && d.DKIMRevocationPending {
			out = append(out, clone(d))
		}
	}
	return out, nil
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
		if d.TenantID == tenantID && d.HasPreviousDKIM() && d.PreviousDKIMSigningEnd().Before(rotatedBefore) {
			out = append(out, clone(d))
		}
	}
	return out, nil
}

func (r *fakeRepo) ListPendingDeactivation(_ context.Context, tenantID uuid.UUID) ([]*domain.Domain, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Domain
	for _, d := range r.domains {
		if d.TenantID == tenantID && d.DirectoryDeactivationPending {
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
	for _, rec := range uc.ExpectedRecords(context.Background(), d) {
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
	// seq, si no es nil, anota el orden de las llamadas junto a las del indice.
	seq *[]string
}

func (f *fakeDirectory) SetActivation(_ context.Context, _ uuid.UUID, name string, active bool) error {
	if f.seq != nil {
		*f.seq = append(*f.seq, fmt.Sprintf("activar %s %v", name, active))
	}
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, activation{domain: name, active: active})
	return nil
}

// fakeIndex es el indice global de dominios de organization: un dominio es de una sola empresa.
type fakeIndex struct {
	owners     map[string]uuid.UUID
	claimErr   error
	releaseErr error
	seq        *[]string
}

func newFakeIndex() *fakeIndex { return &fakeIndex{owners: map[string]uuid.UUID{}} }

func (f *fakeIndex) Claim(_ context.Context, tenantID uuid.UUID, name string) error {
	if f.seq != nil {
		*f.seq = append(*f.seq, "reclamar "+name)
	}
	if f.claimErr != nil {
		return f.claimErr
	}
	if owner, ok := f.owners[name]; ok && owner != tenantID {
		return domain.ErrDomainClaimedElsewhere
	}
	f.owners[name] = tenantID
	return nil
}

func (f *fakeIndex) Release(_ context.Context, tenantID uuid.UUID, name string) error {
	if f.seq != nil {
		*f.seq = append(*f.seq, "soltar "+name)
	}
	if f.releaseErr != nil {
		return f.releaseErr
	}
	if f.owners[name] == tenantID {
		delete(f.owners, name)
	}
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

// fakePublisher hace de publicador de NATS y de outbox de claves (ports.KeyEvents).
type fakePublisher struct {
	subjects  []string
	rotations []domain.DKIMRotation
	err       error
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
func (f *fakePublisher) DKIMRotated(_ context.Context, _ *domain.Domain, r *domain.DKIMRotation) error {
	if err := f.record("domains.domain.dkim_rotated"); err != nil {
		return err
	}
	f.rotations = append(f.rotations, *r)
	return nil
}
func (f *fakePublisher) DKIMRevoked(_ context.Context, _ *domain.Domain, r *domain.DKIMRotation) error {
	if err := f.record("domains.domain.dkim_revoked"); err != nil {
		return err
	}
	f.rotations = append(f.rotations, *r)
	return nil
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
