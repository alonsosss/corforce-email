package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
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

// Open reserva el buzon falso hasta Close: el trabajador de envios programados abre conexiones en
// paralelo y el buzon falso no es concurrente, como no lo es una conexion IMAP.
func (f *fakeMail) Open(_ context.Context, username string) (ports.Mailbox, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mb.session.Lock()
	f.opened++
	f.mb.username = username
	return f.mb, nil
}

type appended struct {
	folder string
	raw    []byte
	flags  []domain.Flag
}

// storedPart es una parte de un mensaje del buzon que OpenPart entrega.
type storedPart struct {
	part domain.Part
	data []byte
}

type partRequest struct {
	folder string
	uid    uint32
	part   string
	limit  int64
}

// fakeMessage es un mensaje guardado en el buzon falso: Append lo registra y OpenRaw, Stat y
// FindByMessageID lo leen. El Message-ID sale de la marca "id=" que pone fakeComposer.
type fakeMessage struct {
	raw       []byte
	messageID string
	flags     []domain.Flag
}

var fakeMessageIDPattern = regexp.MustCompile(`id=([^;]+);`)

type fakeMailbox struct {
	session    sync.Mutex
	username   string
	folders    []domain.Folder
	raw        *domain.RawMessage
	reply      domain.ReplyReference
	appended   []appended
	appendUID  uint32
	appendErr  map[string]error
	parts      map[string]storedPart
	partReqs   []partRequest
	moved      []string
	expunged   []string
	expungeErr error
	moveErr    error
	flagged    []domain.FlagChange
	listed     string
	listQuery  domain.ListQuery
	closed     int
	// missing son UIDs que ya no estan: no cuentan en SetFlags, Move ni Expunge.
	missing map[uint32]bool
	// messages guarda lo que se anadio, por carpeta y UID, con la UIDVALIDITY de uidValidity.
	messages    map[string]map[uint32]*fakeMessage
	uidValidity uint32
	nextUID     uint32
	openErr     error
	// operaciones de carpetas.
	created   []string
	renamed   []string
	deleted   []string
	emptied   []string
	emptyN    int
	createErr error
}

func (m *fakeMailbox) Close() error {
	m.closed++
	m.session.Unlock()
	return nil
}
func (m *fakeMailbox) Folders(context.Context, bool) ([]domain.Folder, error) {
	return m.folders, nil
}
func (m *fakeMailbox) Quota(context.Context) (*domain.Quota, error) { return nil, nil }
func (m *fakeMailbox) List(_ context.Context, folder string, q domain.ListQuery) (domain.MessagePage, error) {
	m.listed, m.listQuery = folder, q
	return domain.MessagePage{}, nil
}
func (m *fakeMailbox) Read(context.Context, string, uint32, domain.ReadOptions) (*domain.RawMessage, error) {
	if m.raw == nil {
		return nil, domain.ErrMessageNotFound
	}
	return m.raw, nil
}

// OpenPart aplica el tope como el adaptador IMAP: una parte mayor que maxBytes no se abre.
func (m *fakeMailbox) OpenPart(_ context.Context, folder string, uid uint32, partID string, maxBytes int64) (domain.Part, io.ReadCloser, error) {
	m.partReqs = append(m.partReqs, partRequest{folder: folder, uid: uid, part: partID, limit: maxBytes})
	if m.parts == nil {
		return domain.Part{ID: "2"}, io.NopCloser(strings.NewReader("datos")), nil
	}
	p, ok := m.parts[partID]
	if !ok {
		return domain.Part{}, nil, domain.ErrPartNotFound
	}
	if int64(len(p.data)) > maxBytes {
		return domain.Part{}, nil, domain.ErrPartTooLarge
	}
	return p.part, io.NopCloser(strings.NewReader(string(p.data))), nil
}
func (m *fakeMailbox) ReplyReference(context.Context, string, uint32) (domain.ReplyReference, error) {
	return m.reply, nil
}

// present son los UIDs pedidos que existen.
func (m *fakeMailbox) present(uids []uint32) []uint32 {
	var out []uint32
	for _, uid := range uids {
		if !m.missing[uid] {
			out = append(out, uid)
		}
	}
	return out
}

func (m *fakeMailbox) SetFlags(_ context.Context, _ string, uids []uint32, change domain.FlagChange) (int, error) {
	n := len(m.present(uids))
	if n > 0 {
		m.flagged = append(m.flagged, change)
	}
	return n, nil
}
func (m *fakeMailbox) Move(_ context.Context, folder string, uids []uint32, dest string) (int, error) {
	if m.moveErr != nil {
		return 0, m.moveErr
	}
	uids = m.present(uids)
	for _, uid := range uids {
		m.moved = append(m.moved, fmt.Sprintf("%s:%d->%s", folder, uid, dest))
		if msg := m.messages[folder][uid]; msg != nil {
			delete(m.messages[folder], uid)
			m.put(dest, m.newUID(), msg)
		}
	}
	return len(uids), nil
}
func (m *fakeMailbox) Expunge(_ context.Context, folder string, uids []uint32) (int, error) {
	if m.expungeErr != nil {
		return 0, m.expungeErr
	}
	uids = m.present(uids)
	for _, uid := range uids {
		m.expunged = append(m.expunged, fmt.Sprintf("%s:%d", folder, uid))
		delete(m.messages[folder], uid)
	}
	return len(uids), nil
}
func (m *fakeMailbox) Empty(_ context.Context, folder string) (int, error) {
	m.emptied = append(m.emptied, folder)
	return m.emptyN, nil
}
func (m *fakeMailbox) Append(_ context.Context, folder string, raw []byte, flags []domain.Flag, _ time.Time) (domain.AppendedMessage, error) {
	if err := m.appendErr[folder]; err != nil {
		return domain.AppendedMessage{}, err
	}
	m.appended = append(m.appended, appended{folder: folder, raw: raw, flags: flags})
	msg := &fakeMessage{raw: raw, flags: flags}
	if match := fakeMessageIDPattern.FindSubmatch(raw); match != nil {
		msg.messageID = string(match[1])
	}
	uid := m.appendUID
	if uid == 0 {
		uid = m.newUID()
	}
	m.put(folder, uid, msg)
	return domain.AppendedMessage{UID: uid, UIDValidity: m.validity()}, nil
}

func (m *fakeMailbox) validity() uint32 {
	if m.uidValidity == 0 {
		return 1
	}
	return m.uidValidity
}

func (m *fakeMailbox) newUID() uint32 {
	m.nextUID++
	return m.nextUID + 100
}

func (m *fakeMailbox) put(folder string, uid uint32, msg *fakeMessage) {
	if m.messages == nil {
		m.messages = map[string]map[uint32]*fakeMessage{}
	}
	if m.messages[folder] == nil {
		m.messages[folder] = map[uint32]*fakeMessage{}
	}
	m.messages[folder][uid] = msg
}

func (m *fakeMailbox) Stat(_ context.Context, folder string, uid uint32) (domain.StoredMessage, error) {
	if m.openErr != nil {
		return domain.StoredMessage{}, m.openErr
	}
	msg := m.messages[folder][uid]
	if msg == nil {
		return domain.StoredMessage{}, domain.ErrMessageNotFound
	}
	return domain.StoredMessage{UIDValidity: m.validity(), UID: uid, MessageID: msg.messageID, Size: int64(len(msg.raw))}, nil
}
func (m *fakeMailbox) OpenRaw(ctx context.Context, folder string, uid uint32, maxBytes int64) (domain.StoredMessage, io.ReadCloser, error) {
	st, err := m.Stat(ctx, folder, uid)
	if err != nil {
		return domain.StoredMessage{}, nil, err
	}
	if st.Size > maxBytes {
		return domain.StoredMessage{}, nil, domain.ErrMessageTooLarge
	}
	return st, io.NopCloser(bytes.NewReader(m.messages[folder][uid].raw)), nil
}
func (m *fakeMailbox) FindByMessageID(_ context.Context, folder, messageID string) (uint32, error) {
	if m.openErr != nil {
		return 0, m.openErr
	}
	for uid, msg := range m.messages[folder] {
		if msg.messageID == messageID {
			return uid, nil
		}
	}
	return 0, nil
}
func (m *fakeMailbox) CreateFolder(_ context.Context, name string) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.created = append(m.created, name)
	m.folders = append(m.folders, domain.Folder{Name: name, Delimiter: "/", Role: domain.RoleByName(name), Selectable: true})
	return nil
}
func (m *fakeMailbox) RenameFolder(_ context.Context, name, newName string) error {
	m.renamed = append(m.renamed, name+"->"+newName)
	return nil
}
func (m *fakeMailbox) DeleteFolder(_ context.Context, name string) error {
	m.deleted = append(m.deleted, name)
	return nil
}

type sendCall struct {
	username, from string
	rcpts          []string
	raw            []byte
}

type fakeSender struct {
	mu    sync.Mutex
	calls []sendCall
	err   error
}

func (s *fakeSender) Send(_ context.Context, username, from string, rcpts []string, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, sendCall{username: username, from: from, rcpts: rcpts, raw: raw})
	return s.err
}

// fakeDirectory hace de mail-directory: los remitentes que la regla de Postfix permite.
type fakeDirectory struct {
	mu    sync.Mutex
	ids   []string
	err   error
	calls int
	// respuesta automatica: lo que devuelve, con que buzon y con que datos se le llamo.
	vacation    domain.Vacation
	vacationErr error
	vacationFor string
	vacationIn  *domain.VacationInput
	// libreta de direcciones: lo que devuelve y con que se le llamo.
	book      []domain.AddressBookEntry
	bookErr   error
	bookFor   string
	bookQuery string
	bookLimit int
	// firma, reglas y contrasena.
	signature   domain.Signature
	signatureIn *domain.SignatureInput
	filters     domain.MailFilters
	filtersIn   *domain.MailFiltersInput
	settingsFor string
	settingsErr error
	passwordFor string
	passwordSet string
	passwordErr error
	// envios programados.
	scheduled    map[string]*domain.ScheduledSend
	scheduledNew []domain.NewScheduledSend
	createErr    error
	cancelErr    error
	claimQueue   []domain.ScheduledClaim
	claimErr     error
	claimLease   time.Duration
	finished     map[string][]domain.ScheduledOutcome
	finishErr    error
	nextID       int
}

func (d *fakeDirectory) Vacation(_ context.Context, username string) (domain.Vacation, error) {
	d.vacationFor = username
	return d.vacation, d.vacationErr
}

func (d *fakeDirectory) SetVacation(_ context.Context, username string, in domain.VacationInput) (domain.Vacation, error) {
	d.vacationFor, d.vacationIn = username, &in
	if d.vacationErr != nil {
		return domain.Vacation{}, d.vacationErr
	}
	d.vacation = domain.Vacation{Enabled: in.Enabled, Subject: in.Subject, Message: in.Message, IntervalDays: in.IntervalDays, StartsOn: in.StartsOn, EndsOn: in.EndsOn}
	return d.vacation, nil
}

func (d *fakeDirectory) Search(_ context.Context, username, query string, limit int) ([]domain.AddressBookEntry, error) {
	d.bookFor, d.bookQuery, d.bookLimit = username, query, limit
	return d.book, d.bookErr
}

func (d *fakeDirectory) SenderIdentities(context.Context, string) ([]string, error) {
	d.calls++
	return d.ids, d.err
}

func (d *fakeDirectory) Signature(_ context.Context, username string) (domain.Signature, error) {
	d.settingsFor = username
	return d.signature, d.settingsErr
}

func (d *fakeDirectory) SetSignature(_ context.Context, username string, in domain.SignatureInput) (domain.Signature, error) {
	d.settingsFor, d.signatureIn = username, &in
	if d.settingsErr != nil {
		return domain.Signature{}, d.settingsErr
	}
	d.signature = domain.Signature{Enabled: in.Enabled, HTML: in.HTML, Text: in.Text, OnReplies: in.OnReplies}
	return d.signature, nil
}

func (d *fakeDirectory) Filters(_ context.Context, username string) (domain.MailFilters, error) {
	d.settingsFor = username
	return d.filters, d.settingsErr
}

func (d *fakeDirectory) SetFilters(_ context.Context, username string, in domain.MailFiltersInput) (domain.MailFilters, error) {
	d.settingsFor, d.filtersIn = username, &in
	if d.settingsErr != nil {
		return domain.MailFilters{}, d.settingsErr
	}
	d.filters = domain.MailFilters{Rules: in.Rules, Forwarding: in.Forwarding}
	return d.filters, nil
}

func (d *fakeDirectory) SetPassword(_ context.Context, username, password string) error {
	d.passwordFor = username
	if d.passwordErr != nil {
		return d.passwordErr
	}
	d.passwordSet = password
	return nil
}

func (d *fakeDirectory) CreateScheduled(_ context.Context, in domain.NewScheduledSend) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.createErr != nil {
		return "", d.createErr
	}
	d.nextID++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", d.nextID)
	d.scheduledNew = append(d.scheduledNew, in)
	if d.scheduled == nil {
		d.scheduled = map[string]*domain.ScheduledSend{}
	}
	d.scheduled[id] = &domain.ScheduledSend{
		ID: id, SendAt: in.SendAt, Subject: in.Subject, Recipients: in.Recipients, Status: domain.ScheduledPending,
		MessageID: in.MessageID, Folder: in.Folder, UIDValidity: in.UIDValidity, UID: in.UID,
	}
	return id, nil
}

func (d *fakeDirectory) ListScheduled(context.Context, string) ([]domain.ScheduledSend, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.settingsErr != nil {
		return nil, d.settingsErr
	}
	var out []domain.ScheduledSend
	for _, row := range d.scheduled {
		out = append(out, *row)
	}
	return out, nil
}

func (d *fakeDirectory) RescheduleScheduled(_ context.Context, _, id string, at time.Time) (domain.ScheduledSend, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row, ok := d.scheduled[id]
	if !ok {
		return domain.ScheduledSend{}, domain.ErrScheduledNotFound
	}
	if row.Status != domain.ScheduledPending {
		return domain.ScheduledSend{}, domain.ErrScheduledNotPending
	}
	row.SendAt = at
	return *row, nil
}

func (d *fakeDirectory) CancelScheduled(_ context.Context, _, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancelErr != nil {
		return d.cancelErr
	}
	row, ok := d.scheduled[id]
	if !ok {
		return domain.ErrScheduledNotFound
	}
	row.Status = domain.ScheduledCanceled
	return nil
}

func (d *fakeDirectory) ClaimScheduled(_ context.Context, limit int, lease time.Duration) ([]domain.ScheduledClaim, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.claimLease = lease
	if d.claimErr != nil {
		return nil, d.claimErr
	}
	n := min(limit, len(d.claimQueue))
	out := d.claimQueue[:n]
	d.claimQueue = d.claimQueue[n:]
	return out, nil
}

func (d *fakeDirectory) FinishScheduled(_ context.Context, id string, outcome domain.ScheduledOutcome) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.finishErr != nil {
		return d.finishErr
	}
	if d.finished == nil {
		d.finished = map[string][]domain.ScheduledOutcome{}
	}
	d.finished[id] = append(d.finished[id], outcome)
	return nil
}

// fakeLedger imita el registro de envios de Redis: reservar si no existe y actualizar o
// liberar solo con la marca de quien reservo.
type fakeLedger struct {
	mu          sync.Mutex
	records     map[string]domain.SendRecord
	seq         int
	failReserve error
}

func newFakeLedger() *fakeLedger { return &fakeLedger{records: map[string]domain.SendRecord{}} }

func (l *fakeLedger) Reserve(_ context.Context, key string, rec domain.SendRecord, _ time.Duration) (domain.SendRecord, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failReserve != nil {
		return domain.SendRecord{}, false, l.failReserve
	}
	if current, ok := l.records[key]; ok {
		return current, false, nil
	}
	l.seq++
	rec.Token = fmt.Sprintf("marca-%d", l.seq)
	l.records[key] = rec
	return rec, true, nil
}

func (l *fakeLedger) Update(_ context.Context, key string, rec domain.SendRecord, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.records[key]; !ok || current.Token != rec.Token {
		return false, nil
	}
	l.records[key] = rec
	return true, nil
}

func (l *fakeLedger) Release(_ context.Context, key, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.records[key]; ok && current.Token == token {
		delete(l.records, key)
	}
	return nil
}

// fakeComposer marca cada composicion con si lleva Bcc; size fuerza el tamano.
type fakeComposer struct {
	mu          sync.Mutex
	includeBcc  []bool
	last        domain.Outgoing
	size        int
	final       domain.FinalizedMessage
	finalizeErr error
	finalized   []time.Time
}

func (c *fakeComposer) Compose(out domain.Outgoing, includeBcc bool) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.includeBcc = append(c.includeBcc, includeBcc)
	c.last = out
	raw := []byte(fmt.Sprintf("bcc=%t;id=%s;", includeBcc, out.MessageID))
	if c.size > len(raw) {
		raw = append(raw, make([]byte, c.size-len(raw))...)
	}
	return raw, nil
}

// Finalize imita al adaptador: el sobre sale de lo que diga la prueba (final) y la version que
// viaja pierde la marca de Bcc.
func (c *fakeComposer) Finalize(stored []byte, date time.Time) (domain.FinalizedMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalized = append(c.finalized, date)
	if c.finalizeErr != nil {
		return domain.FinalizedMessage{}, c.finalizeErr
	}
	f := c.final
	f.Stored = append([]byte("final;"), stored...)
	f.Wire = []byte(strings.Replace(string(stored), "bcc=true;", "bcc=false;", 1))
	return f, nil
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
	infect  string
	scanned []string
}

// Scan falla con err, o solo con el adjunto llamado infect.
func (s *fakeScanner) Scan(_ context.Context, name string, _ []byte) error {
	s.scanned = append(s.scanned, name)
	if s.infect != "" && name == s.infect {
		return fmt.Errorf("%w: Eicar-Test-Signature", domain.ErrAttachmentInfected)
	}
	return s.err
}

type harness struct {
	dav       *fakeDAV
	svc       *Service
	auth      *fakeAuth
	store     *fakeStore
	mail      *fakeMail
	mb        *fakeMailbox
	sender    *fakeSender
	directory *fakeDirectory
	ledger    *fakeLedger
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
		directory: &fakeDirectory{},
		dav:       &fakeDAV{},
		ledger:    newFakeLedger(),
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
	svc, err := New(h.deps())
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	return h
}

// deps son las dependencias completas del arnes; las pruebas cambian lo que necesitan.
func (h *harness) deps() Deps {
	return Deps{
		Auth: h.auth, Sessions: h.store, Mail: h.mail, Sender: h.sender, Directory: h.directory, Vacations: h.directory, AddressBook: h.directory,
		Signatures: h.directory, Filters: h.directory, Passwords: h.directory, Scheduled: h.directory, Contacts: h.dav, Calendar: h.dav,
		Ledger: h.ledger, Composer: h.composer, Sanitizer: h.sanitizer, Scanner: h.scanner,
		PartURL: func(folder string, uid uint32, part string) string {
			return fmt.Sprintf("/parts/%s/%d/%s", folder, uid, part)
		},
		Clock:  h.clock.Now,
		Logger: zap.NewNop(),
		Config: Config{
			CellCode:              testCell,
			Sessions:              domain.SessionPolicy{Idle: 30 * time.Minute, Max: 12 * time.Hour},
			Limits:                domain.Limits{MaxRecipients: 3, MaxMessageBytes: 1000},
			MaxBodyPartBytes:      1 << 20,
			MaxAttachmentBytes:    1 << 20,
			SendTimeout:           time.Minute,
			MaxScheduledDays:      30,
			ScheduledPollInterval: time.Second,
			ScheduledBatch:        5,
			MaxImportBytes:        1 << 10,
		},
	}
}

func (h *harness) login(t *testing.T) (string, domain.Session) {
	t.Helper()
	token, sess, err := h.svc.Login(context.Background(), testUser, testPass, testIP, "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return token, sess
}

var keySeq int

// sendOpts da una clave de idempotencia nueva en cada llamada.
func sendOpts(replaceUID uint32) domain.SendOptions {
	keySeq++
	return domain.SendOptions{IdempotencyKey: fmt.Sprintf("clave-de-prueba-%06d", keySeq), ReplaceUID: replaceUID}
}

// fakeDAV hace de mail-dav: anota con que buzon y datos se le llamo y devuelve lo que la prueba
// prepare.
type fakeDAV struct {
	mb       domain.MailboxRef
	id       string
	ifMatch  string
	query    domain.ContactQuery
	window   domain.EventWindow
	contact  domain.Contact
	event    domain.Event
	imported []byte
	err      error
	calls    int
}

func (f *fakeDAV) record(mb domain.MailboxRef, id string) error {
	f.calls++
	f.mb, f.id = mb, id
	return f.err
}

func (f *fakeDAV) ListContacts(_ context.Context, mb domain.MailboxRef, q domain.ContactQuery) (domain.ContactPage, error) {
	f.query = q
	if err := f.record(mb, ""); err != nil {
		return domain.ContactPage{}, err
	}
	return domain.ContactPage{Items: []domain.Contact{f.contact}, Total: 1, Page: 1, PerPage: 50}, nil
}
func (f *fakeDAV) Contact(_ context.Context, mb domain.MailboxRef, id string) (domain.Contact, error) {
	return f.contact, f.record(mb, id)
}
func (f *fakeDAV) CreateContact(_ context.Context, mb domain.MailboxRef, in domain.ContactInput) (domain.Contact, error) {
	f.contact.ContactInput = in
	return f.contact, f.record(mb, "")
}
func (f *fakeDAV) UpdateContact(_ context.Context, mb domain.MailboxRef, id string, in domain.ContactInput, ifMatch string) (domain.Contact, error) {
	f.contact.ContactInput, f.ifMatch = in, ifMatch
	return f.contact, f.record(mb, id)
}
func (f *fakeDAV) DeleteContact(_ context.Context, mb domain.MailboxRef, id string) error {
	return f.record(mb, id)
}
func (f *fakeDAV) ExportContacts(_ context.Context, mb domain.MailboxRef) (io.ReadCloser, error) {
	if err := f.record(mb, ""); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("BEGIN:VCARD\r\nEND:VCARD\r\n")), nil
}
func (f *fakeDAV) ImportContacts(_ context.Context, mb domain.MailboxRef, _ string, data []byte) (domain.ImportResult, error) {
	f.imported = data
	return domain.ImportResult{Imported: 1}, f.record(mb, "")
}
func (f *fakeDAV) Limits(context.Context) (map[string]int64, error) {
	f.calls++
	return map[string]int64{"max_import_cards": 1000}, f.err
}
func (f *fakeDAV) Occurrences(_ context.Context, mb domain.MailboxRef, w domain.EventWindow) ([]domain.Occurrence, error) {
	f.window = w
	return []domain.Occurrence{{ID: "e1", Title: "Reunion"}}, f.record(mb, "")
}
func (f *fakeDAV) Event(_ context.Context, mb domain.MailboxRef, id string) (domain.Event, error) {
	return f.event, f.record(mb, id)
}
func (f *fakeDAV) CreateEvent(_ context.Context, mb domain.MailboxRef, in domain.EventInput) (domain.Event, error) {
	f.event.EventInput = in
	return f.event, f.record(mb, "")
}
func (f *fakeDAV) UpdateEvent(_ context.Context, mb domain.MailboxRef, id string, in domain.EventInput, ifMatch string) (domain.Event, error) {
	f.event.EventInput, f.ifMatch = in, ifMatch
	return f.event, f.record(mb, id)
}
func (f *fakeDAV) DeleteEvent(_ context.Context, mb domain.MailboxRef, id string) error {
	return f.record(mb, id)
}
