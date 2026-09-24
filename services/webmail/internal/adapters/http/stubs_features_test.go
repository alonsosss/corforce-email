package http

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// stubSettings hace de mail-directory para la firma, las reglas, la contrasena y los envios
// programados: anota con que buzon y datos se le llamo.
type stubSettings struct {
	mu          sync.Mutex
	username    string
	signatureIn *domain.SignatureInput
	filtersIn   *domain.MailFiltersInput
	password    string
	err         error
	rows        map[string]domain.ScheduledSend
	created     []domain.NewScheduledSend
	canceled    []string
	// reminders hace de mail-directory para los recordatorios y las respuestas rapidas.
	reminders *stubReminders
}

func newStubSettings() *stubSettings {
	return &stubSettings{rows: map[string]domain.ScheduledSend{}, reminders: newStubReminders()}
}

func (s *stubSettings) Signature(_ context.Context, username string) (domain.Signature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username = username
	return domain.Signature{Enabled: true, HTML: "<p>Ana</p>", Text: "Ana", Limits: domain.SignatureLimits{MaxHTMLBytes: 8192, MaxTextBytes: 4096}}, s.err
}

func (s *stubSettings) SetSignature(_ context.Context, username string, in domain.SignatureInput) (domain.Signature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username, s.signatureIn = username, &in
	if s.err != nil {
		return domain.Signature{}, s.err
	}
	return domain.Signature{Enabled: in.Enabled, HTML: in.HTML, Text: in.Text, OnReplies: in.OnReplies}, nil
}

func (s *stubSettings) Filters(_ context.Context, username string) (domain.MailFilters, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username = username
	return domain.MailFilters{Limits: map[string]int{"max_rules": 50}}, s.err
}

func (s *stubSettings) SetFilters(_ context.Context, username string, in domain.MailFiltersInput) (domain.MailFilters, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username, s.filtersIn = username, &in
	if s.err != nil {
		return domain.MailFilters{}, s.err
	}
	return domain.MailFilters{Rules: in.Rules, Forwarding: in.Forwarding, Limits: map[string]int{"max_rules": 50}}, nil
}

func (s *stubSettings) SetPassword(_ context.Context, username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username = username
	if s.err != nil {
		return s.err
	}
	s.password = password
	return nil
}

func (s *stubSettings) CreateScheduled(_ context.Context, in domain.NewScheduledSend) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	id := "00000000-0000-4000-8000-000000000001"
	s.created = append(s.created, in)
	s.rows[id] = domain.ScheduledSend{ID: id, SendAt: in.SendAt, Subject: in.Subject, Recipients: in.Recipients,
		Status: domain.ScheduledPending, Folder: in.Folder, UID: in.UID, UIDValidity: in.UIDValidity, MessageID: in.MessageID}
	return id, nil
}

func (s *stubSettings) ListScheduled(_ context.Context, username string) ([]domain.ScheduledSend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.username = username
	var out []domain.ScheduledSend
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, s.err
}

func (s *stubSettings) RescheduleScheduled(_ context.Context, _, id string, at time.Time) (domain.ScheduledSend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return domain.ScheduledSend{}, s.err
	}
	row, ok := s.rows[id]
	if !ok {
		return domain.ScheduledSend{}, domain.ErrScheduledNotFound
	}
	row.SendAt = at
	s.rows[id] = row
	return row, nil
}

func (s *stubSettings) CancelScheduled(_ context.Context, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if _, ok := s.rows[id]; !ok {
		return domain.ErrScheduledNotFound
	}
	s.canceled = append(s.canceled, id)
	delete(s.rows, id)
	return nil
}

func (s *stubSettings) ClaimScheduled(context.Context, int, time.Duration) ([]domain.ScheduledClaim, error) {
	return nil, nil
}

func (s *stubSettings) FinishScheduled(context.Context, string, domain.ScheduledOutcome) error {
	return nil
}

// stubDAV hace de mail-dav: anota con que empresa, buzon, id y If-Match se le llamo.
type stubDAV struct {
	mu       sync.Mutex
	mb       domain.MailboxRef
	id       string
	ifMatch  string
	query    domain.ContactQuery
	window   domain.EventWindow
	contact  domain.ContactInput
	event    domain.EventInput
	imported []byte
	filename string
	err      error
	calls    int
}

func (d *stubDAV) note(mb domain.MailboxRef, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.mb, d.id = mb, id
	return d.err
}

var sampleContact = domain.Contact{
	ID: "c1", ETag: `"v1"`,
	ContactInput: domain.ContactInput{Name: "Luis Soto", Emails: []domain.ContactValue{{Value: "luis@x.pe", Type: "work"}}},
}

func (d *stubDAV) ListContacts(_ context.Context, mb domain.MailboxRef, q domain.ContactQuery) (domain.ContactPage, error) {
	d.query = q
	if err := d.note(mb, ""); err != nil {
		return domain.ContactPage{}, err
	}
	return domain.ContactPage{Items: []domain.Contact{sampleContact}, Total: 1, Page: 1, PerPage: 25}, nil
}
func (d *stubDAV) Contact(_ context.Context, mb domain.MailboxRef, id string) (domain.Contact, error) {
	return sampleContact, d.note(mb, id)
}
func (d *stubDAV) CreateContact(_ context.Context, mb domain.MailboxRef, in domain.ContactInput) (domain.Contact, error) {
	d.contact = in
	return domain.Contact{ID: "nuevo", ETag: `"v1"`, ContactInput: in}, d.note(mb, "")
}
func (d *stubDAV) UpdateContact(_ context.Context, mb domain.MailboxRef, id string, in domain.ContactInput, ifMatch string) (domain.Contact, error) {
	d.contact, d.ifMatch = in, ifMatch
	return domain.Contact{ID: id, ETag: `"v2"`, ContactInput: in}, d.note(mb, id)
}
func (d *stubDAV) DeleteContact(_ context.Context, mb domain.MailboxRef, id string) error {
	return d.note(mb, id)
}
func (d *stubDAV) ExportContacts(_ context.Context, mb domain.MailboxRef) (io.ReadCloser, error) {
	if err := d.note(mb, ""); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Luis\r\nEND:VCARD\r\n")), nil
}
func (d *stubDAV) ImportContacts(_ context.Context, mb domain.MailboxRef, filename string, data []byte) (domain.ImportResult, error) {
	d.imported, d.filename = data, filename
	return domain.ImportResult{Imported: 2, Updated: 1, Skipped: []domain.ImportSkip{{Index: 3, Reason: "sin FN"}}}, d.note(mb, "")
}
func (d *stubDAV) Limits(context.Context) (map[string]int64, error) {
	return map[string]int64{"max_import_bytes": 1 << 20, "max_event_window_days": 62}, d.note(domain.MailboxRef{}, "")
}
func (d *stubDAV) Occurrences(_ context.Context, mb domain.MailboxRef, w domain.EventWindow) ([]domain.Occurrence, error) {
	d.window = w
	return []domain.Occurrence{{ID: "e1", Start: "2026-09-25T10:00:00Z", End: "2026-09-25T11:00:00Z", Title: "Reunion", Recurring: true}}, d.note(mb, "")
}
func (d *stubDAV) Event(_ context.Context, mb domain.MailboxRef, id string) (domain.Event, error) {
	return domain.Event{ID: id, ETag: `"e1"`, EventInput: domain.EventInput{Title: "Reunion"}}, d.note(mb, id)
}
func (d *stubDAV) CreateEvent(_ context.Context, mb domain.MailboxRef, in domain.EventInput) (domain.Event, error) {
	d.event = in
	return domain.Event{ID: "e9", ETag: `"e1"`, EventInput: in}, d.note(mb, "")
}
func (d *stubDAV) UpdateEvent(_ context.Context, mb domain.MailboxRef, id string, in domain.EventInput, ifMatch string) (domain.Event, error) {
	d.event, d.ifMatch = in, ifMatch
	return domain.Event{ID: id, ETag: `"e2"`, EventInput: in}, d.note(mb, id)
}
func (d *stubDAV) DeleteEvent(_ context.Context, mb domain.MailboxRef, id string) error {
	return d.note(mb, id)
}
