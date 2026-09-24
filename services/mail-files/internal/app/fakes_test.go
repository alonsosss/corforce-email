package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-files/internal/ports"
	"github.com/google/uuid"
)

// eicar es la cadena de prueba estandar de los antivirus. El escaner falso la detecta como lo hace
// clamd; la prueba real contra el protocolo esta en adapters/clamav.
const eicar = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

type memRepo struct {
	mu        sync.Mutex
	files     map[uuid.UUID]domain.File
	deletedAt map[uuid.UUID]time.Time
	failNext  error
}

func newMemRepo() *memRepo {
	return &memRepo{files: map[uuid.UUID]domain.File{}, deletedAt: map[uuid.UUID]time.Time{}}
}

func live(f domain.File, now time.Time) bool {
	return !f.ObjectDeleted && (f.Status == domain.StatusPending || f.Status == domain.StatusReady) &&
		f.ExpiresAt.After(now) && f.Downloads < f.MaxDownloads
}

func (r *memRepo) usage(o domain.Owner, now time.Time) domain.Usage {
	var u domain.Usage
	for _, f := range r.files {
		if f.TenantID != o.TenantID || !live(f, now) {
			continue
		}
		u.TenantBytes += f.SizeBytes
		if f.MailboxID == o.MailboxID {
			u.MailboxBytes += f.SizeBytes
			u.MailboxActive++
		}
	}
	return u
}

func (r *memRepo) CreatePending(_ context.Context, f domain.File, p domain.Policy, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	if err := p.Admits(r.usage(domain.Owner{TenantID: f.TenantID, MailboxID: f.MailboxID}, now), f.SizeBytes); err != nil {
		return err
	}
	f.Status = domain.StatusPending
	r.files[f.ID] = f
	return nil
}

func (r *memRepo) update(tenant, id uuid.UUID, fn func(*domain.File) error) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.files[id]
	if !ok || f.TenantID != tenant {
		return domain.File{}, domain.ErrNotFound
	}
	if err := fn(&f); err != nil {
		return domain.File{}, err
	}
	r.files[id] = f
	return f, nil
}

func (r *memRepo) MarkReady(_ context.Context, tenant, id uuid.UUID) (domain.File, error) {
	return r.update(tenant, id, func(f *domain.File) error {
		if f.Status != domain.StatusPending {
			return domain.ErrUnavailable
		}
		f.Status = domain.StatusReady
		return nil
	})
}

func (r *memRepo) MarkFailed(_ context.Context, tenant, id uuid.UUID) error {
	_, err := r.update(tenant, id, func(f *domain.File) error {
		if f.Status == domain.StatusPending {
			f.Status = domain.StatusFailed
		}
		return nil
	})
	return err
}

func (r *memRepo) ListByMailbox(_ context.Context, o domain.Owner, limit int) ([]domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.File
	for _, f := range r.files {
		if f.TenantID == o.TenantID && f.MailboxID == o.MailboxID {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *memRepo) Usage(_ context.Context, o domain.Owner, now time.Time) (domain.Usage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage(o, now), nil
}

func (r *memRepo) Revoke(_ context.Context, o domain.Owner, id uuid.UUID, now time.Time) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.files[id]
	if !ok || f.TenantID != o.TenantID || f.MailboxID != o.MailboxID {
		return domain.File{}, domain.ErrNotFound
	}
	if f.Status != domain.StatusPending && f.Status != domain.StatusReady {
		return domain.File{}, domain.ErrNotRevocable
	}
	f.Status, f.RevokedAt = domain.StatusRevoked, &now
	r.files[id] = f
	return f, nil
}

func (r *memRepo) Get(_ context.Context, tenant, id uuid.UUID) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.files[id]
	if !ok || f.TenantID != tenant {
		return domain.File{}, domain.ErrNotFound
	}
	return f, nil
}

func (r *memRepo) ClaimDownload(_ context.Context, tenant, id uuid.UUID, now time.Time) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.files[id]
	if !ok || f.TenantID != tenant || f.Status != domain.StatusReady || f.ObjectDeleted || !f.ExpiresAt.After(now) || f.Downloads >= f.MaxDownloads {
		return domain.File{}, domain.ErrLinkInvalid
	}
	f.Downloads++
	f.LastDownloadAt = &now
	r.files[id] = f
	return f, nil
}

func (r *memRepo) MarkObjectDeleted(_ context.Context, tenant, id uuid.UUID) error {
	_, err := r.update(tenant, id, func(f *domain.File) error {
		f.ObjectDeleted = true
		r.deletedAt[id] = time.Now()
		return nil
	})
	return err
}

func (r *memRepo) ExpireDue(_ context.Context, tenant uuid.UUID, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for id, f := range r.files {
		if f.TenantID == tenant && f.Status == domain.StatusReady && !f.ExpiresAt.After(now) {
			f.Status = domain.StatusExpired
			r.files[id] = f
			n++
		}
	}
	return n, nil
}

func (r *memRepo) FailStalePending(_ context.Context, tenant uuid.UUID, before time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for id, f := range r.files {
		if f.TenantID == tenant && f.Status == domain.StatusPending && f.CreatedAt.Before(before) {
			f.Status = domain.StatusFailed
			r.files[id] = f
			n++
		}
	}
	return n, nil
}

func (r *memRepo) DueForDeletion(_ context.Context, tenant uuid.UUID, now time.Time, grace time.Duration, limit int) ([]domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	settled := now.Add(-grace)
	var out []domain.File
	for _, f := range r.files {
		if f.TenantID != tenant || f.ObjectDeleted {
			continue
		}
		exhausted := f.Status == domain.StatusReady && f.Downloads >= f.MaxDownloads && f.LastDownloadAt != nil && !f.LastDownloadAt.After(settled)
		if f.Status == domain.StatusRevoked || f.Status == domain.StatusFailed ||
			(f.Status == domain.StatusExpired && !f.ExpiresAt.After(settled)) || exhausted {
			out = append(out, f)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *memRepo) PurgeHistory(_ context.Context, tenant uuid.UUID, before time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for id, f := range r.files {
		if at, ok := r.deletedAt[id]; ok && f.TenantID == tenant && at.Before(before) {
			delete(r.files, id)
			n++
		}
	}
	return n, nil
}

type binder struct {
	unknown map[uuid.UUID]bool
	err     error
}

func (b *binder) Bind(ctx context.Context, tenant, _ uuid.UUID) (context.Context, error) {
	if b.unknown[tenant] {
		return ctx, domain.ErrTenantUnknown
	}
	return ctx, b.err
}

type tenants []uuid.UUID

func (t tenants) ForEach(ctx context.Context, fn func(context.Context, uuid.UUID)) error {
	for _, id := range t {
		fn(ctx, id)
	}
	return nil
}

type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	putErr  error
	deleted []string
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (s *memStore) Put(_ context.Context, key string, r io.Reader, size int64) error {
	if s.putErr != nil {
		return s.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("put: %d bytes y se declararon %d", len(data), size)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	return nil
}

func (s *memStore) Open(_ context.Context, key string, size int64) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if int64(len(data)) != size {
		return nil, domain.ErrUnavailable
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *memStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	s.deleted = append(s.deleted, key)
	return nil
}

type fakeScanner struct {
	down    bool
	scanned int
}

func (s *fakeScanner) Scan(_ context.Context, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.scanned++
	if s.down {
		return domain.ErrScanUnavailable
	}
	if strings.Contains(string(data), eicar) {
		return fmt.Errorf("%w: Eicar-Signature", domain.ErrInfected)
	}
	return nil
}

type memSpool struct {
	open int
}

func (s *memSpool) Create() (ports.SpoolFile, error) {
	s.open++
	return &memSpoolFile{spool: s}, nil
}

type memSpoolFile struct {
	buf   bytes.Buffer
	spool *memSpool
}

func (f *memSpoolFile) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *memSpoolFile) Rewind() (io.Reader, error) {
	return bytes.NewReader(f.buf.Bytes()), nil
}
func (f *memSpoolFile) Discard() error {
	f.spool.open--
	return nil
}

// failingReader corta la subida a mitad.
type failingReader struct{ sent bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "parte"), nil
	}
	return 0, errors.New("conexion cortada")
}
