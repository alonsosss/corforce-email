package app

import (
	"context"
	"sort"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

// fakeSignatures y fakeFilters guardan una fila por empresa y buzon, como la restriccion UNIQUE.
type fakeSignatures struct {
	items   map[string]*domain.MailboxSignature
	deleted []string
}

func (f *fakeSignatures) ByUsername(_ context.Context, tenantID uuid.UUID, username string) (*domain.MailboxSignature, error) {
	s, ok := f.items[vacationKey(tenantID, username)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *s
	return &c, nil
}

func (f *fakeSignatures) Upsert(_ context.Context, s *domain.MailboxSignature) error {
	if f.items == nil {
		f.items = map[string]*domain.MailboxSignature{}
	}
	s.UpdatedAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	c := *s
	f.items[vacationKey(s.TenantID, s.Username)] = &c
	return nil
}

func (f *fakeSignatures) DeleteByUsername(_ context.Context, tenantID uuid.UUID, username string) error {
	f.deleted = append(f.deleted, username)
	delete(f.items, vacationKey(tenantID, username))
	return nil
}

type fakeFilters struct {
	items   map[string]*domain.MailboxFilters
	upserts int
	deleted []string
}

func (f *fakeFilters) ByUsername(_ context.Context, tenantID uuid.UUID, username string) (*domain.MailboxFilters, error) {
	v, ok := f.items[vacationKey(tenantID, username)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *v
	return &c, nil
}

func (f *fakeFilters) Upsert(_ context.Context, v *domain.MailboxFilters) error {
	f.upserts++
	if f.items == nil {
		f.items = map[string]*domain.MailboxFilters{}
	}
	c := *v
	f.items[vacationKey(v.TenantID, v.Username)] = &c
	return nil
}

func (f *fakeFilters) DeleteByUsername(_ context.Context, tenantID uuid.UUID, username string) error {
	f.deleted = append(f.deleted, username)
	delete(f.items, vacationKey(tenantID, username))
	return nil
}

// fakeScheduled imita mail.scheduled_sends con la hora del arnes; Claim sigue las mismas reglas que
// el SQL (sin el bloqueo, que solo se prueba contra Postgres).
type fakeScheduled struct {
	items   map[uuid.UUID]*domain.ScheduledSend
	now     func() time.Time
	deleted []string
}

var _ ports.ScheduledSendRepository = (*fakeScheduled)(nil)

func (f *fakeScheduled) Create(_ context.Context, s *domain.ScheduledSend) error {
	if f.items == nil {
		f.items = map[uuid.UUID]*domain.ScheduledSend{}
	}
	for _, other := range f.items {
		if other.Username == s.Username && other.MessageID == s.MessageID &&
			(other.Status == domain.ScheduledPending || other.Status == domain.ScheduledSending) {
			return domain.ErrAlreadyExists
		}
	}
	s.CreatedAt, s.UpdatedAt = f.now(), f.now()
	c := *s
	f.items[s.ID] = &c
	return nil
}

func (f *fakeScheduled) ListByUsername(_ context.Context, tenantID uuid.UUID, username string, limit int) ([]domain.ScheduledSend, error) {
	var out []domain.ScheduledSend
	for _, s := range f.items {
		if s.TenantID == tenantID && s.Username == username &&
			(s.Status == domain.ScheduledPending || s.Status == domain.ScheduledSending || s.Status == domain.ScheduledFailed) {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SendAt.Before(out[j].SendAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeScheduled) CountPending(_ context.Context, tenantID uuid.UUID, username string) (int, error) {
	n := 0
	for _, s := range f.items {
		if s.TenantID == tenantID && s.Username == username && (s.Status == domain.ScheduledPending || s.Status == domain.ScheduledSending) {
			n++
		}
	}
	return n, nil
}

func (f *fakeScheduled) GetForUpdate(_ context.Context, tenantID uuid.UUID, username string, id uuid.UUID) (*domain.ScheduledSend, error) {
	s, ok := f.items[id]
	if !ok || s.TenantID != tenantID || s.Username != username {
		return nil, domain.ErrNotFound
	}
	c := *s
	return &c, nil
}

func (f *fakeScheduled) Reschedule(_ context.Context, tenantID, id uuid.UUID, sendAt time.Time) (*domain.ScheduledSend, error) {
	s, ok := f.items[id]
	if !ok || s.TenantID != tenantID || s.Status != domain.ScheduledPending {
		return nil, domain.ErrNotFound
	}
	s.SendAt = sendAt
	c := *s
	return &c, nil
}

func (f *fakeScheduled) Cancel(_ context.Context, tenantID, id uuid.UUID) error {
	s, ok := f.items[id]
	if !ok || s.TenantID != tenantID {
		return domain.ErrNotFound
	}
	s.Status, s.LeaseUntil = domain.ScheduledCanceled, nil
	return nil
}

func (f *fakeScheduled) DeleteByUsername(_ context.Context, tenantID uuid.UUID, username string) error {
	f.deleted = append(f.deleted, username)
	for id, s := range f.items {
		if s.TenantID == tenantID && s.Username == username {
			delete(f.items, id)
		}
	}
	return nil
}

func (f *fakeScheduled) Claim(_ context.Context, p domain.ClaimParams, maxAttempts int, _ time.Duration) ([]domain.ScheduledSend, error) {
	now := f.now()
	var due []*domain.ScheduledSend
	for _, s := range f.items {
		expired := s.Status == domain.ScheduledSending && s.LeaseUntil != nil && s.LeaseUntil.Before(now)
		if expired && s.Attempts >= maxAttempts {
			s.Status, s.LeaseUntil, s.LastError = domain.ScheduledFailed, nil, domain.ScheduledLeaseExpiredError
			continue
		}
		if (s.Status == domain.ScheduledPending && !s.SendAt.After(now)) || expired {
			due = append(due, s)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].SendAt.Before(due[j].SendAt) })
	if len(due) > p.Limit {
		due = due[:p.Limit]
	}
	out := []domain.ScheduledSend{}
	for _, s := range due {
		lease := now.Add(p.Lease)
		s.Status, s.Attempts, s.LeaseUntil = domain.ScheduledSending, s.Attempts+1, &lease
		out = append(out, *s)
	}
	return out, nil
}

func (f *fakeScheduled) ClaimedForUpdate(_ context.Context, id uuid.UUID) (*domain.ScheduledSend, error) {
	s, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *s
	return &c, nil
}

func (f *fakeScheduled) Close(_ context.Context, id uuid.UUID, t domain.ScheduledTransition) (*domain.ScheduledSend, error) {
	s, ok := f.items[id]
	if !ok || s.Status != domain.ScheduledSending {
		return nil, domain.ErrNotFound
	}
	s.Status, s.LastError, s.LeaseUntil = t.Status, t.Error, nil
	now := f.now()
	if t.Status == domain.ScheduledPending {
		s.SendAt = now.Add(t.RetryAfter)
	}
	if t.Status == domain.ScheduledSent {
		s.SentAt = &now
	}
	c := *s
	return &c, nil
}
