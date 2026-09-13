package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time          { return c.now }
func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeAuth hace de mail-auth: cualquier rechazo es el mismo error, como el real.
type fakeAuth struct {
	users  map[string]string
	names  map[string]string
	calls  int
	lastIP string
	err    error
}

func (a *fakeAuth) Verify(_ context.Context, username, password, remoteIP string) (domain.Identity, error) {
	a.calls++
	a.lastIP = remoteIP
	if a.err != nil {
		return domain.Identity{}, a.err
	}
	if pw, ok := a.users[username]; ok && pw == password {
		return domain.Identity{Username: username, DisplayName: a.names[username]}, nil
	}
	return domain.Identity{}, domain.ErrInvalidCredentials
}

type storedSession struct {
	sess     domain.Session
	deadline time.Time
}

// fakeStore imita a Redis: la inactividad es el TTL de la clave. ignoreTTL deja las
// claves vivas para comprobar que la vida maxima la aplica el caso de uso.
type fakeStore struct {
	mu            sync.Mutex
	clock         *testClock
	sessions      map[string]storedSession
	revoked       map[string]time.Time
	touches       []time.Duration
	ignoreTTL     bool
	failRevokedAt error
	failGet       error
	failCreate    error
}

func newFakeStore(clock *testClock) *fakeStore {
	return &fakeStore{clock: clock, sessions: map[string]storedSession{}, revoked: map[string]time.Time{}}
}

func (s *fakeStore) Create(_ context.Context, key string, sess domain.Session, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failCreate != nil {
		return s.failCreate
	}
	s.sessions[key] = storedSession{sess: sess, deadline: s.clock.Now().Add(ttl)}
	return nil
}

func (s *fakeStore) Get(_ context.Context, key string) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet != nil {
		return domain.Session{}, s.failGet
	}
	st, ok := s.sessions[key]
	if !ok || (!s.ignoreTTL && !s.clock.Now().Before(st.deadline)) {
		delete(s.sessions, key)
		return domain.Session{}, domain.ErrSessionInvalid
	}
	return st.sess, nil
}

func (s *fakeStore) Touch(_ context.Context, key string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touches = append(s.touches, ttl)
	if st, ok := s.sessions[key]; ok {
		st.deadline = s.clock.Now().Add(ttl)
		s.sessions[key] = st
	}
	return nil
}

func (s *fakeStore) Delete(_ context.Context, key, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
	return nil
}

func (s *fakeStore) Revoke(_ context.Context, username string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.After(s.revoked[username]) {
		s.revoked[username] = at
	}
	for k, st := range s.sessions {
		if st.sess.Username == username && !st.sess.CreatedAt.After(at) {
			delete(s.sessions, k)
		}
	}
	return nil
}

func (s *fakeStore) RevokedAt(_ context.Context, username string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRevokedAt != nil {
		return time.Time{}, s.failRevokedAt
	}
	return s.revoked[username], nil
}

type fakeMail struct {
	mb     *fakeMailbox
	err    error
	opened int
}

func (f *fakeMail) Open(_ context.Context, username string) (ports.Mailbox, error) {
	f.opened++
	if f.err != nil {
		return nil, f.err
	}
	f.mb.username = username
	return f.mb, nil
}

type appended struct {
	folder string
	raw    []byte
	flags  []domain.Flag
}

type fakeMailbox struct {
	username  string
	folders   []domain.Folder
	raw       *domain.RawMessage
	reply     domain.ReplyReference
	appended  []appended
	appendUID uint32
	moved     []string
	expunged  []string
	flagged   []domain.FlagChange
	listed    string
	closed    int
}

func (m *fakeMailbox) Close() error { m.closed++; return nil }
func (m *fakeMailbox) Folders(context.Context, bool) ([]domain.Folder, error) {
	return m.folders, nil
}
func (m *fakeMailbox) Quota(context.Context) (*domain.Quota, error) { return nil, nil }
func (m *fakeMailbox) List(_ context.Context, folder string, _ domain.ListQuery) (domain.MessagePage, error) {
	m.listed = folder
	return domain.MessagePage{}, nil
}
func (m *fakeMailbox) Read(context.Context, string, uint32, domain.ReadOptions) (*domain.RawMessage, error) {
	if m.raw == nil {
		return nil, domain.ErrMessageNotFound
	}
	return m.raw, nil
}
func (m *fakeMailbox) OpenPart(context.Context, string, uint32, string, int64) (domain.Part, io.ReadCloser, error) {
	return domain.Part{ID: "2"}, io.NopCloser(strings.NewReader("datos")), nil
}
func (m *fakeMailbox) ReplyReference(context.Context, string, uint32) (domain.ReplyReference, error) {
	return m.reply, nil
}
func (m *fakeMailbox) SetFlags(_ context.Context, _ string, _ uint32, change domain.FlagChange) error {
	m.flagged = append(m.flagged, change)
	return nil
}
func (m *fakeMailbox) Move(_ context.Context, folder string, uid uint32, dest string) error {
	m.moved = append(m.moved, fmt.Sprintf("%s:%d->%s", folder, uid, dest))
	return nil
}
func (m *fakeMailbox) Expunge(_ context.Context, folder string, uid uint32) error {
	m.expunged = append(m.expunged, fmt.Sprintf("%s:%d", folder, uid))
	return nil
}
func (m *fakeMailbox) Append(_ context.Context, folder string, raw []byte, flags []domain.Flag, _ time.Time) (uint32, error) {
	m.appended = append(m.appended, appended{folder: folder, raw: raw, flags: flags})
	return m.appendUID, nil
}

type sendCall struct {
	username, from string
	rcpts          []string
	raw            []byte
}

type fakeSender struct {
	calls []sendCall
	err   error
}

func (s *fakeSender) Send(_ context.Context, username, from string, rcpts []string, raw []byte) error {
	s.calls = append(s.calls, sendCall{username: username, from: from, rcpts: rcpts, raw: raw})
	return s.err
}

// fakeComposer marca cada composicion con si lleva Bcc; size fuerza el tamano.
type fakeComposer struct {
	includeBcc []bool
	last       domain.Outgoing
	size       int
}

func (c *fakeComposer) Compose(out domain.Outgoing, includeBcc bool) ([]byte, error) {
	c.includeBcc = append(c.includeBcc, includeBcc)
	c.last = out
	raw := []byte(fmt.Sprintf("bcc=%t;", includeBcc))
	if c.size > len(raw) {
		raw = append(raw, make([]byte, c.size-len(raw))...)
	}
	return raw, nil
}

type fakeSanitizer struct {
	opts   domain.SanitizeOptions
	remote bool
}

func (f *fakeSanitizer) Incoming(html string, opts domain.SanitizeOptions) domain.SanitizedHTML {
	f.opts = opts
	return domain.SanitizedHTML{HTML: "limpio:" + html, RemoteImages: f.remote}
}

func (f *fakeSanitizer) Outgoing(html string) (string, string) {
	return "limpio:" + html, "texto plano"
}

type fakeScanner struct {
	err     error
	scanned []string
}

func (s *fakeScanner) Scan(_ context.Context, name string, _ []byte) error {
	s.scanned = append(s.scanned, name)
	return s.err
}

type harness struct {
	svc       *Service
	auth      *fakeAuth
	store     *fakeStore
	mail      *fakeMail
	mb        *fakeMailbox
	sender    *fakeSender
	composer  *fakeComposer
	sanitizer *fakeSanitizer
	scanner   *fakeScanner
	clock     *testClock
}

const (
	testUser = "ana@empresa.pe"
	testPass = "correcta-larga"
	testIP   = "203.0.113.7"
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	clock := &testClock{now: time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)}
	h := &harness{
		auth:      &fakeAuth{users: map[string]string{testUser: testPass}, names: map[string]string{testUser: "Ana Perez"}},
		store:     newFakeStore(clock),
		mb:        &fakeMailbox{appendUID: 7},
		sender:    &fakeSender{},
		composer:  &fakeComposer{},
		sanitizer: &fakeSanitizer{},
		scanner:   &fakeScanner{},
		clock:     clock,
	}
	h.mb.folders = []domain.Folder{
		{Name: "INBOX", Role: domain.RoleInbox, Selectable: true},
		{Name: "Sent", Role: domain.RoleSent, Selectable: true},
		{Name: "Drafts", Role: domain.RoleDrafts, Selectable: true},
		{Name: "Trash", Role: domain.RoleTrash, Selectable: true},
	}
	h.mail = &fakeMail{mb: h.mb}
	svc, err := New(Deps{
		Auth: h.auth, Sessions: h.store, Mail: h.mail, Sender: h.sender, Composer: h.composer,
		Sanitizer: h.sanitizer, Scanner: h.scanner,
		PartURL: func(folder string, uid uint32, part string) string {
			return fmt.Sprintf("/parts/%s/%d/%s", folder, uid, part)
		},
		Clock:  clock.Now,
		Logger: zap.NewNop(),
		Config: Config{
			Sessions:           domain.SessionPolicy{Idle: 30 * time.Minute, Max: 12 * time.Hour},
			Limits:             domain.Limits{MaxRecipients: 3, MaxMessageBytes: 1000},
			MaxBodyPartBytes:   1 << 20,
			MaxAttachmentBytes: 1 << 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

func (h *harness) login(t *testing.T) (string, domain.Session) {
	t.Helper()
	token, sess, err := h.svc.Login(context.Background(), testUser, testPass, testIP, "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return token, sess
}
