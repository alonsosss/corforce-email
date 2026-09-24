package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

// fakeReminders imita mail.mailbox_reminders con la hora del arnes; Claim sigue las reglas del SQL
// (sin el bloqueo, que solo se prueba contra Postgres).
type fakeReminders struct {
	items map[uuid.UUID]*domain.Reminder
	now   func() time.Time
}

var _ ports.ReminderRepository = (*fakeReminders)(nil)

func reminderActive(r *domain.Reminder) bool {
	return r.Status == domain.ReminderPending || r.Status == domain.ReminderRunning
}

func (f *fakeReminders) Create(_ context.Context, r *domain.Reminder) error {
	if f.items == nil {
		f.items = map[uuid.UUID]*domain.Reminder{}
	}
	for _, other := range f.items {
		if other.Username == r.Username && other.Kind == r.Kind && r.MessageID != "" && other.MessageID == r.MessageID && reminderActive(other) {
			return domain.ErrAlreadyExists
		}
	}
	r.CreatedAt, r.UpdatedAt = f.now(), f.now()
	c := *r
	f.items[r.ID] = &c
	return nil
}

func (f *fakeReminders) ListByUsername(_ context.Context, tenantID uuid.UUID, username, kind string, limit int) ([]domain.Reminder, error) {
	var out []domain.Reminder
	for _, r := range f.items {
		if r.TenantID == tenantID && r.Username == username && r.Kind == kind && (reminderActive(r) || r.Status == domain.ReminderFailed) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DueAt.Before(out[j].DueAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeReminders) CountActive(_ context.Context, tenantID uuid.UUID, username string) (int, error) {
	n := 0
	for _, r := range f.items {
		if r.TenantID == tenantID && r.Username == username && reminderActive(r) {
			n++
		}
	}
	return n, nil
}

func (f *fakeReminders) GetForUpdate(_ context.Context, tenantID uuid.UUID, username string, id uuid.UUID) (*domain.Reminder, error) {
	r, ok := f.items[id]
	if !ok || r.TenantID != tenantID || r.Username != username {
		return nil, domain.ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeReminders) Reschedule(_ context.Context, tenantID, id uuid.UUID, due time.Time) (*domain.Reminder, error) {
	r, ok := f.items[id]
	if !ok || r.TenantID != tenantID || r.Status != domain.ReminderPending {
		return nil, domain.ErrNotFound
	}
	r.DueAt = due
	c := *r
	return &c, nil
}

func (f *fakeReminders) Cancel(_ context.Context, tenantID, id uuid.UUID) error {
	r, ok := f.items[id]
	if !ok || r.TenantID != tenantID {
		return domain.ErrNotFound
	}
	r.Status, r.LeaseUntil = domain.ReminderCanceled, nil
	return nil
}

func (f *fakeReminders) DeleteByUsername(_ context.Context, tenantID uuid.UUID, username string) error {
	for id, r := range f.items {
		if r.TenantID == tenantID && r.Username == username {
			delete(f.items, id)
		}
	}
	return nil
}

func (f *fakeReminders) Claim(_ context.Context, p domain.ClaimParams, maxAttempts int, _ time.Duration) ([]domain.Reminder, error) {
	now := f.now()
	var due []*domain.Reminder
	for _, r := range f.items {
		expired := r.Status == domain.ReminderRunning && r.LeaseUntil != nil && r.LeaseUntil.Before(now)
		if expired && r.Attempts >= maxAttempts {
			r.Status, r.LeaseUntil, r.LastError = domain.ReminderFailed, nil, domain.ReminderLeaseExpiredError
			continue
		}
		if (r.Status == domain.ReminderPending && !r.DueAt.After(now)) || expired {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].DueAt.Before(due[j].DueAt) })
	if len(due) > p.Limit {
		due = due[:p.Limit]
	}
	out := []domain.Reminder{}
	for _, r := range due {
		lease := now.Add(p.Lease)
		r.Status, r.Attempts, r.LeaseUntil = domain.ReminderRunning, r.Attempts+1, &lease
		out = append(out, *r)
	}
	return out, nil
}

func (f *fakeReminders) ClaimedForUpdate(_ context.Context, id uuid.UUID) (*domain.Reminder, error) {
	r, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeReminders) Close(_ context.Context, id uuid.UUID, t domain.ReminderTransition) (*domain.Reminder, error) {
	r, ok := f.items[id]
	if !ok || r.Status != domain.ReminderRunning {
		return nil, domain.ErrNotFound
	}
	r.Status, r.Result, r.LastError, r.LeaseUntil = t.Status, t.Result, t.Error, nil
	now := f.now()
	if t.Status == domain.ReminderPending {
		r.DueAt = now.Add(t.RetryAfter)
	}
	if t.Status == domain.ReminderDone {
		r.DoneAt = &now
	}
	c := *r
	return &c, nil
}

// fakeQuickReplies aplica el indice unico (buzon, nombre sin distinguir mayusculas).
type fakeQuickReplies struct {
	items map[uuid.UUID]*domain.QuickReply
	now   func() time.Time
}

var _ ports.QuickReplyRepository = (*fakeQuickReplies)(nil)

func (f *fakeQuickReplies) nameTaken(q *domain.QuickReply) bool {
	for _, other := range f.items {
		if other.ID != q.ID && other.Username == q.Username && strings.EqualFold(other.Name, q.Name) {
			return true
		}
	}
	return false
}

func (f *fakeQuickReplies) ListByUsername(_ context.Context, tenantID uuid.UUID, username string) ([]domain.QuickReply, error) {
	var out []domain.QuickReply
	for _, q := range f.items {
		if q.TenantID == tenantID && q.Username == username {
			out = append(out, *q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (f *fakeQuickReplies) Count(ctx context.Context, tenantID uuid.UUID, username string) (int, error) {
	items, _ := f.ListByUsername(ctx, tenantID, username)
	return len(items), nil
}

func (f *fakeQuickReplies) Create(_ context.Context, q *domain.QuickReply) error {
	if f.items == nil {
		f.items = map[uuid.UUID]*domain.QuickReply{}
	}
	if f.nameTaken(q) {
		return domain.ErrAlreadyExists
	}
	q.CreatedAt, q.UpdatedAt = f.now(), f.now()
	c := *q
	f.items[q.ID] = &c
	return nil
}

func (f *fakeQuickReplies) Update(_ context.Context, q *domain.QuickReply) error {
	cur, ok := f.items[q.ID]
	if !ok || cur.TenantID != q.TenantID || cur.Username != q.Username {
		return domain.ErrNotFound
	}
	if f.nameTaken(q) {
		return domain.ErrAlreadyExists
	}
	q.CreatedAt, q.UpdatedAt = cur.CreatedAt, f.now()
	c := *q
	f.items[q.ID] = &c
	return nil
}

func (f *fakeQuickReplies) Delete(_ context.Context, tenantID uuid.UUID, username string, id uuid.UUID) error {
	q, ok := f.items[id]
	if !ok || q.TenantID != tenantID || q.Username != username {
		return domain.ErrNotFound
	}
	delete(f.items, id)
	return nil
}

func (f *fakeQuickReplies) DeleteByUsername(_ context.Context, tenantID uuid.UUID, username string) error {
	for id, q := range f.items {
		if q.TenantID == tenantID && q.Username == username {
			delete(f.items, id)
		}
	}
	return nil
}
