// Package apptest tiene los dobles en memoria de los puertos de la aplicacion, para las pruebas del
// caso de uso y de la API HTTP.
package apptest

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/ports"
	"github.com/google/uuid"
)

type Tx struct{}

func (Tx) Transact(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type Repo struct {
	mu        sync.Mutex
	Jobs      map[uuid.UUID]*domain.Job
	ExpireOut []domain.Job
}

func NewRepo() *Repo { return &Repo{Jobs: map[uuid.UUID]*domain.Job{}} }

func (r *Repo) Insert(_ context.Context, j *domain.Job, maxActive int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := 0
	for _, o := range r.Jobs {
		if o.TenantID != j.TenantID || !o.Status.Active() {
			continue
		}
		active++
		if o.MailboxID == j.MailboxID {
			return domain.ErrJobAlreadyActive
		}
	}
	if active >= maxActive {
		return domain.ErrTenantLimitReached
	}
	c := *j
	r.Jobs[j.ID] = &c
	return nil
}

func (r *Repo) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.Jobs[id]
	if !ok || j.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	c := *j
	return &c, nil
}

func (r *Repo) List(_ context.Context, tenantID uuid.UUID, f ports.ListFilter, p ports.Page) ([]domain.Job, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Job
	for _, j := range r.Jobs {
		if j.TenantID != tenantID || (f.MailboxID != nil && j.MailboxID != *f.MailboxID) || (f.Status != nil && j.Status != *f.Status) {
			continue
		}
		out = append(out, *j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	total := int64(len(out))
	if p.Offset >= len(out) {
		return nil, total, nil
	}
	end := p.Offset + p.Limit
	if end > len(out) {
		end = len(out)
	}
	return out[p.Offset:end], total, nil
}

func (r *Repo) CountActive(_ context.Context, tenantID uuid.UUID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, j := range r.Jobs {
		if j.TenantID == tenantID && j.Status.Active() {
			n++
		}
	}
	return n, nil
}

func (r *Repo) RequestCancel(_ context.Context, tenantID, id uuid.UUID, at time.Time) (*domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.Jobs[id]
	if !ok || j.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	if !j.Status.Active() {
		return nil, domain.ErrNotCancellable
	}
	if j.Status == domain.StatusPending {
		j.Status, j.SourcePasswordEnc, j.FinishedAt = domain.StatusCancelled, nil, &at
	}
	if j.CancelRequestedAt == nil {
		j.CancelRequestedAt = &at
	}
	c := *j
	return &c, nil
}

func (r *Repo) Claim(_ context.Context, tenantID uuid.UUID, p ports.ClaimParams) (*domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var pick *domain.Job
	for _, j := range r.Jobs {
		if j.TenantID == tenantID && j.Status == domain.StatusPending && (pick == nil || j.CreatedAt.Before(pick.CreatedAt)) {
			pick = j
		}
	}
	if pick == nil {
		return nil, nil
	}
	lease := p.LeaseID
	pick.Status, pick.Attempt, pick.LeaseID, pick.LeaseExpiresAt, pick.RunnerID = domain.StatusRunning, pick.Attempt+1, &lease, &p.LeaseUntil, p.RunnerID
	c := *pick
	return &c, nil
}

func (r *Repo) Heartbeat(_ context.Context, tenantID, id uuid.UUID, p ports.HeartbeatParams) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.Jobs[id]
	if !ok || j.TenantID != tenantID || j.Status != domain.StatusRunning || j.LeaseID == nil || *j.LeaseID != p.LeaseID {
		return false, domain.ErrLeaseLost
	}
	j.Phase, j.Progress, j.LeaseExpiresAt = p.Phase, p.Progress, &p.LeaseUntil
	return j.CancelRequestedAt != nil, nil
}

func (r *Repo) Finish(_ context.Context, tenantID, id uuid.UUID, p ports.FinishParams) (*domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.Jobs[id]
	if !ok || j.TenantID != tenantID || j.Status != domain.StatusRunning || j.LeaseID == nil || *j.LeaseID != p.LeaseID {
		return nil, domain.ErrLeaseLost
	}
	j.Status, j.Progress, j.LastError, j.FinishedAt, j.SourcePasswordEnc, j.LeaseID = p.Status, p.Progress, p.Error, &p.Now, nil, nil
	c := *j
	return &c, nil
}

func (r *Repo) ExpireLost(context.Context, uuid.UUID, time.Time, int) ([]domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.ExpireOut
	r.ExpireOut = nil
	return out, nil
}

func (r *Repo) DeleteByMailbox(_ context.Context, tenantID, mailboxID uuid.UUID) ([]domain.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Job
	for id, j := range r.Jobs {
		if j.TenantID == tenantID && j.MailboxID == mailboxID {
			out = append(out, *j)
			delete(r.Jobs, id)
		}
	}
	return out, nil
}

type Mailboxes struct {
	Ref ports.MailboxRef
	Err error
}

func (m Mailboxes) Lookup(_ context.Context, _, id uuid.UUID) (ports.MailboxRef, error) {
	if m.Err != nil {
		return ports.MailboxRef{}, m.Err
	}
	ref := m.Ref
	ref.ID = id
	return ref, nil
}

type Resolver struct {
	Addrs []netip.Addr
	Err   error
	Asked []string
}

func (r *Resolver) LookupAddrs(_ context.Context, host string) ([]netip.Addr, error) {
	r.Asked = append(r.Asked, host)
	return r.Addrs, r.Err
}

// Cipher no es criptografia: marca el dato para comprobar que lo guardado no es el claro.
type Cipher struct{ FailDecrypt bool }

var CipherMark = []byte("enc:")

func (c Cipher) Encrypt(p []byte) ([]byte, error) {
	return append(append([]byte{}, CipherMark...), p...), nil
}
func (c Cipher) Decrypt(d []byte) ([]byte, error) {
	if c.FailDecrypt || !bytes.HasPrefix(d, CipherMark) {
		return nil, errors.New("no descifra")
	}
	return d[len(CipherMark):], nil
}

type Tenants struct {
	IDs     []uuid.UUID
	ForErr  error
	Visited []uuid.UUID
}

func (t *Tenants) For(ctx context.Context, id uuid.UUID) (context.Context, error) {
	if t.ForErr != nil {
		return nil, t.ForErr
	}
	return ctx, nil
}

func (t *Tenants) ForEach(ctx context.Context, fn func(ctx context.Context, tenantID uuid.UUID) bool) error {
	for _, id := range t.IDs {
		t.Visited = append(t.Visited, id)
		if fn(ctx, id) {
			return nil
		}
	}
	return nil
}

type Events struct {
	mu   sync.Mutex
	Log  []string
	Fail error
}

func (e *Events) record(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Fail != nil {
		return e.Fail
	}
	e.Log = append(e.Log, name)
	return nil
}
func (e *Events) Created(_ context.Context, _ *domain.Job) error { return e.record("created") }
func (e *Events) Started(_ context.Context, _ *domain.Job) error { return e.record("started") }
func (e *Events) CancelRequested(_ context.Context, _ *domain.Job, _ uuid.UUID) error {
	return e.record("cancel_requested")
}
func (e *Events) Finished(_ context.Context, j *domain.Job, _ uuid.UUID) error {
	return e.record("finished:" + string(j.Status))
}
