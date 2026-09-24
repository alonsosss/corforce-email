package app

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/textproto"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
)

// MoveTracked mueve un mensaje como Move y devuelve su UID nuevo, como el COPYUID de Dovecot.
func (m *fakeMailbox) MoveTracked(_ context.Context, folder string, uid uint32, dest string) (domain.AppendedMessage, error) {
	if m.moveErr != nil {
		return domain.AppendedMessage{}, m.moveErr
	}
	msg := m.messages[folder][uid]
	if msg == nil || m.missing[uid] {
		return domain.AppendedMessage{}, domain.ErrMessageNotFound
	}
	m.moved = append(m.moved, fmt.Sprintf("%s:%d->%s", folder, uid, dest))
	delete(m.messages[folder], uid)
	next := m.newUID()
	m.put(dest, next, msg)
	return domain.AppendedMessage{UID: next, UIDValidity: m.validity()}, nil
}

// HasReply lee las cabeceras In-Reply-To y References de los mensajes guardados en la carpeta.
func (m *fakeMailbox) HasReply(_ context.Context, folder, messageID string) (bool, error) {
	if m.openErr != nil {
		return false, m.openErr
	}
	ref := "<" + messageID + ">"
	for _, msg := range m.messages[folder] {
		h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(msg.raw))).ReadMIMEHeader()
		if err != nil && len(h) == 0 {
			continue
		}
		if strings.Contains(h.Get("In-Reply-To"), ref) || strings.Contains(h.Get("References"), ref) {
			return true, nil
		}
	}
	return false, nil
}

// fakeReminders hace de mail-directory para los recordatorios y las respuestas rapidas.
type fakeReminders struct {
	mu       sync.Mutex
	rows     map[string]*domain.Reminder
	users    map[string]string
	created  []domain.NewReminder
	claims   []domain.ReminderClaim
	finished map[string]domain.ReminderOutcome
	seq      int
	err      error
	// quick replies.
	quick     map[string]domain.QuickReply
	quickIn   []domain.QuickReply
	quickErr  error
	quickList domain.QuickReplyLimits
}

var (
	_ ports.ReminderDirectory   = (*fakeReminders)(nil)
	_ ports.QuickReplyDirectory = (*fakeReminders)(nil)
)

func newFakeReminders() *fakeReminders {
	return &fakeReminders{
		rows: map[string]*domain.Reminder{}, users: map[string]string{}, finished: map[string]domain.ReminderOutcome{},
		quick: map[string]domain.QuickReply{},
	}
}

func (f *fakeReminders) nextID() string {
	f.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)
}

func (f *fakeReminders) CreateReminder(_ context.Context, in domain.NewReminder) (domain.Reminder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.Reminder{}, f.err
	}
	for id, r := range f.rows {
		if f.users[id] == in.Username && r.Kind == in.Kind && in.MessageID != "" && r.MessageID == in.MessageID &&
			(r.Status == domain.ReminderPending || r.Status == domain.ReminderRunning) {
			return domain.Reminder{}, domain.ErrReminderExists
		}
	}
	f.created = append(f.created, in)
	id := f.nextID()
	r := &domain.Reminder{
		ID: id, Kind: in.Kind, MessageID: in.MessageID, Folder: in.Folder, UIDValidity: in.UIDValidity, UID: in.UID,
		ReturnFolder: in.ReturnFolder, Subject: in.Subject, Addresses: in.Addresses, DueAt: in.DueAt, Status: domain.ReminderPending,
	}
	f.rows[id], f.users[id] = r, in.Username
	return *r, nil
}

func (f *fakeReminders) ListReminders(_ context.Context, username string, kind domain.ReminderKind) ([]domain.Reminder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := []domain.Reminder{}
	for id, r := range f.rows {
		if f.users[id] == username && r.Kind == kind && r.Status != domain.ReminderCanceled && r.Status != domain.ReminderDone {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeReminders) RescheduleReminder(_ context.Context, username, id string, at time.Time) (domain.Reminder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok || f.users[id] != username {
		return domain.Reminder{}, domain.ErrReminderNotFound
	}
	if r.Status != domain.ReminderPending {
		return domain.Reminder{}, domain.ErrReminderNotPending
	}
	r.DueAt = at
	return *r, nil
}

func (f *fakeReminders) CancelReminder(_ context.Context, username, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok || f.users[id] != username {
		return domain.ErrReminderNotFound
	}
	if r.Status == domain.ReminderRunning || r.Status == domain.ReminderDone {
		return domain.ErrReminderNotPending
	}
	r.Status = domain.ReminderCanceled
	return nil
}

// ClaimReminders reclama todas las pendientes, sin mirar la hora: la prueba decide cuales hay.
func (f *fakeReminders) ClaimReminders(_ context.Context, limit int, _ time.Duration) ([]domain.ReminderClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.ReminderClaim{}
	ids := make([]string, 0, len(f.rows))
	for id := range f.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := f.rows[id]
		if r.Status == domain.ReminderPending && len(out) < limit {
			r.Status = domain.ReminderRunning
			out = append(out, domain.ReminderClaim{Reminder: *r, Username: f.users[id]})
		}
	}
	f.claims = append(f.claims, out...)
	return out, nil
}

func (f *fakeReminders) FinishReminder(_ context.Context, id string, outcome domain.ReminderOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok || r.Status != domain.ReminderRunning {
		return domain.ErrReminderNotClaimed
	}
	f.finished[id] = outcome
	r.Status = outcome.Status
	if outcome.Status == domain.ReminderFailed && outcome.Retry {
		r.Status = domain.ReminderPending
	}
	return nil
}

func (f *fakeReminders) row(id string) domain.Reminder {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.rows[id]
}

func (f *fakeReminders) QuickReplies(context.Context, string) (domain.QuickReplyList, error) {
	if f.quickErr != nil {
		return domain.QuickReplyList{}, f.quickErr
	}
	list := domain.QuickReplyList{Items: []domain.QuickReply{}, Limits: f.quickList}
	for _, q := range f.quick {
		list.Items = append(list.Items, q)
	}
	return list, nil
}

func (f *fakeReminders) CreateQuickReply(_ context.Context, _ string, q domain.QuickReply) (domain.QuickReply, error) {
	if f.quickErr != nil {
		return domain.QuickReply{}, f.quickErr
	}
	f.quickIn = append(f.quickIn, q)
	q.ID = f.nextID()
	f.quick[q.ID] = q
	return q, nil
}

func (f *fakeReminders) UpdateQuickReply(_ context.Context, _ string, q domain.QuickReply) (domain.QuickReply, error) {
	if _, ok := f.quick[q.ID]; !ok {
		return domain.QuickReply{}, domain.ErrQuickReplyNotFound
	}
	f.quickIn = append(f.quickIn, q)
	f.quick[q.ID] = q
	return q, nil
}

func (f *fakeReminders) DeleteQuickReply(_ context.Context, _, id string) error {
	if _, ok := f.quick[id]; !ok {
		return domain.ErrQuickReplyNotFound
	}
	delete(f.quick, id)
	return nil
}
