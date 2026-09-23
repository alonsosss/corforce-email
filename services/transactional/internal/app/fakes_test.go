package app

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/alonsosss/corforce-email/services/transactional/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// outboxEvent es un evento encolado por el fake (la outbox vive en el mismo "almacen"
// que los datos para que un rollback los retire juntos, como en la base real).
type outboxEvent struct {
	Subject  string
	TenantID uuid.UUID
	Payload  map[string]any
}

// fakeRepo implementa ports.Repository y ports.EventPublisher en memoria, con
// transacciones que deshacen todo lo escrito si fn falla.
type fakeRepo struct {
	messages    map[uuid.UUID]domain.Message
	order       []uuid.UUID
	events      []domain.Event
	submissions map[string]domain.Submission
	domains     map[string]domain.SendingDomain
	unsubs      map[string]bool
	outbox      []outboxEvent

	// commitBeforeNextTx simula una transaccion concurrente que confirma justo antes de
	// que empiece la siguiente.
	commitBeforeNextTx func(r *fakeRepo)
	// attributionErr simula que la lectura de la atribucion falla (base caida).
	attributionErr error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		messages:    map[uuid.UUID]domain.Message{},
		submissions: map[string]domain.Submission{},
		domains:     map[string]domain.SendingDomain{},
		unsubs:      map[string]bool{},
	}
}

type repoState struct {
	messages    map[uuid.UUID]domain.Message
	order       []uuid.UUID
	events      []domain.Event
	submissions map[string]domain.Submission
	domains     map[string]domain.SendingDomain
	unsubs      map[string]bool
	outbox      []outboxEvent
}

func copyMap[K comparable, V any](in map[K]V) map[K]V {
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (r *fakeRepo) snapshot() repoState {
	return repoState{
		messages:    copyMap(r.messages),
		order:       append([]uuid.UUID(nil), r.order...),
		events:      append([]domain.Event(nil), r.events...),
		submissions: copyMap(r.submissions),
		domains:     copyMap(r.domains),
		unsubs:      copyMap(r.unsubs),
		outbox:      append([]outboxEvent(nil), r.outbox...),
	}
}

func (r *fakeRepo) restore(s repoState) {
	r.messages, r.order, r.events = s.messages, s.order, s.events
	r.submissions, r.domains, r.unsubs, r.outbox = s.submissions, s.domains, s.unsubs, s.outbox
}

func (r *fakeRepo) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	if hook := r.commitBeforeNextTx; hook != nil {
		r.commitBeforeNextTx = nil
		hook(r)
	}
	s := r.snapshot()
	if err := fn(ctx); err != nil {
		r.restore(s)
		return err
	}
	return nil
}

func (r *fakeRepo) Publish(_ context.Context, subject string, tenantID uuid.UUID, payload map[string]any) error {
	r.outbox = append(r.outbox, outboxEvent{Subject: subject, TenantID: tenantID, Payload: payload})
	return nil
}

func (r *fakeRepo) published(subject string) []outboxEvent {
	var out []outboxEvent
	for _, e := range r.outbox {
		if e.Subject == subject {
			out = append(out, e)
		}
	}
	return out
}

func (r *fakeRepo) InsertMessage(_ context.Context, m *domain.Message) error {
	if _, ok := r.messages[m.ID]; ok {
		return errors.New("duplicate message")
	}
	r.messages[m.ID] = *m
	r.order = append(r.order, m.ID)
	return nil
}

func (r *fakeRepo) GetMessage(_ context.Context, tenantID, id uuid.UUID) (*domain.Message, error) {
	m, ok := r.messages[id]
	if !ok || m.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	return &m, nil
}

func (r *fakeRepo) GetAttribution(_ context.Context, tenantID, id uuid.UUID) (*domain.MessageAttribution, error) {
	if r.attributionErr != nil {
		return nil, r.attributionErr
	}
	m, ok := r.messages[id]
	if !ok || m.TenantID != tenantID {
		return nil, domain.ErrNotFound
	}
	a := m.Attribution()
	return &a, nil
}

func (r *fakeRepo) GetMessages(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]domain.Message, error) {
	var out []domain.Message
	for _, id := range ids {
		if m, err := r.GetMessage(ctx, tenantID, id); err == nil {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (r *fakeRepo) ListMessages(_ context.Context, tenantID uuid.UUID, f domain.MessageFilter, offset, limit int) ([]domain.Message, int64, error) {
	var out []domain.Message
	for _, id := range r.order {
		m := r.messages[id]
		if m.TenantID != tenantID || (f.Status != "" && m.Status != f.Status) || (f.Class != "" && m.Class != f.Class) {
			continue
		}
		out = append(out, m)
	}
	return out, int64(len(out)), nil
}

func (r *fakeRepo) LockQueuedMessage(ctx context.Context, tenantID, id uuid.UUID) (*domain.Message, error) {
	return r.GetMessage(ctx, tenantID, id)
}

func (r *fakeRepo) update(tenantID, id uuid.UUID, fn func(m *domain.Message)) error {
	m, ok := r.messages[id]
	if !ok || m.TenantID != tenantID {
		return domain.ErrNotFound
	}
	fn(&m)
	r.messages[id] = m
	return nil
}

func (r *fakeRepo) MarkSent(_ context.Context, tenantID, id uuid.UUID, sesID string, sentAt time.Time) error {
	return r.update(tenantID, id, func(m *domain.Message) {
		m.Status, m.SESMessageID, m.SentAt, m.Error = domain.StatusSent, &sesID, &sentAt, nil
		m.Attempts++
	})
}

func (r *fakeRepo) MarkFailed(_ context.Context, tenantID, id uuid.UUID, reason string) error {
	return r.update(tenantID, id, func(m *domain.Message) {
		m.Status, m.Error = domain.StatusFailed, &reason
		m.Attempts++
	})
}

func (r *fakeRepo) RecordAttempt(_ context.Context, tenantID, id uuid.UUID, reason string) (int, error) {
	attempts := 0
	err := r.update(tenantID, id, func(m *domain.Message) {
		m.Attempts++
		m.Error = &reason
		attempts = m.Attempts
	})
	return attempts, err
}

func (r *fakeRepo) TransitionStatus(_ context.Context, tenantID, id uuid.UUID, to string, from []string) (bool, error) {
	changed := false
	err := r.update(tenantID, id, func(m *domain.Message) {
		for _, s := range from {
			if m.Status == s {
				m.Status = to
				changed = true
				return
			}
		}
	})
	return changed, err
}

func (r *fakeRepo) ReleaseDue(_ context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for _, id := range r.order {
		m := r.messages[id]
		if len(ids) >= limit {
			break
		}
		if m.TenantID == tenantID && m.Status == domain.StatusAccepted && m.ScheduledAt != nil && !m.ScheduledAt.After(now) {
			m.Status = domain.StatusQueued
			r.messages[id] = m
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (r *fakeRepo) CountByStatus(_ context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.StatusCount, error) {
	counts := map[string]int64{}
	for _, m := range r.messages {
		if m.TenantID == tenantID && !m.Test && !m.CreatedAt.Before(from) && m.CreatedAt.Before(to) {
			counts[m.Status]++
		}
	}
	var out []domain.StatusCount
	for _, s := range domain.Statuses {
		if counts[s] > 0 {
			out = append(out, domain.StatusCount{Status: s, Count: counts[s]})
		}
	}
	return out, nil
}

func (r *fakeRepo) CountTestMessagesSince(_ context.Context, tenantID uuid.UUID, since time.Time) (int, error) {
	n := 0
	for _, m := range r.messages {
		if m.TenantID == tenantID && m.Test && !m.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (r *fakeRepo) InsertEvent(_ context.Context, e *domain.Event) (bool, error) {
	if _, ok := r.messages[e.MessageID]; !ok {
		return false, domain.ErrNotFound
	}
	if e.SNSMessageID != nil {
		for _, existing := range r.events {
			if existing.SNSMessageID != nil && *existing.SNSMessageID == *e.SNSMessageID {
				return false, nil
			}
		}
	}
	r.events = append(r.events, *e)
	return true, nil
}

func (r *fakeRepo) ListEvents(_ context.Context, tenantID, messageID uuid.UUID) ([]domain.Event, error) {
	var out []domain.Event
	for _, e := range r.events {
		if e.TenantID == tenantID && e.MessageID == messageID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r *fakeRepo) eventsOfType(messageID uuid.UUID, eventType string) int {
	n := 0
	for _, e := range r.events {
		if e.MessageID == messageID && e.Type == eventType {
			n++
		}
	}
	return n
}

func submissionKey(tenantID uuid.UUID, key string) string { return tenantID.String() + "|" + key }

func (r *fakeRepo) GetSubmission(_ context.Context, tenantID uuid.UUID, key string) (*domain.Submission, error) {
	s, ok := r.submissions[submissionKey(tenantID, key)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &s, nil
}

func (r *fakeRepo) InsertSubmission(_ context.Context, s *domain.Submission) (bool, error) {
	k := submissionKey(s.TenantID, s.IdempotencyKey)
	if _, ok := r.submissions[k]; ok {
		return false, nil
	}
	r.submissions[k] = *s
	return true, nil
}

func domainKey(tenantID uuid.UUID, name string) string { return tenantID.String() + "|" + name }

// UpsertSendingDomain conserva sending_ready si el evento no lo trae, como el repositorio real.
func (r *fakeRepo) UpsertSendingDomain(_ context.Context, d *domain.SendingDomain) error {
	k := domainKey(d.TenantID, d.Domain)
	next := *d
	if prev, ok := r.domains[k]; ok && next.SendingReady == nil {
		next.SendingReady = prev.SendingReady
	}
	r.domains[k] = next
	return nil
}

func (r *fakeRepo) DeleteSendingDomain(_ context.Context, tenantID uuid.UUID, name string) error {
	delete(r.domains, domainKey(tenantID, name))
	return nil
}

func (r *fakeRepo) GetSendingDomain(_ context.Context, tenantID uuid.UUID, name string) (*domain.SendingDomain, error) {
	d, ok := r.domains[domainKey(tenantID, name)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &d, nil
}

func (r *fakeRepo) ListSendingDomains(_ context.Context, tenantID uuid.UUID) ([]domain.SendingDomain, error) {
	var out []domain.SendingDomain
	for _, d := range r.domains {
		if d.TenantID == tenantID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *fakeRepo) InsertUnsubscribe(_ context.Context, u *domain.Unsubscribe) (bool, error) {
	k := u.TenantID.String() + "|" + u.MessageID.String() + "|" + u.Email
	if r.unsubs[k] {
		return false, nil
	}
	r.unsubs[k] = true
	return true, nil
}

// fakeSuppression es la lista de supresion de la empresa.
type fakeSuppression struct {
	suppressed map[string]string // email en minusculas -> reason principal
	// causes fija todas las causas vigentes de una direccion; sin entrada, la unica es
	// la principal. withoutReasons simula un suppression anterior al campo reasons.
	causes         map[string][]string
	withoutReasons bool
	checks         [][]string
	added          []ports.SuppressionEntry
	checkErr       error
	addErr         error
}

func (s *fakeSuppression) Check(_ context.Context, _ uuid.UUID, emails []string) ([]ports.Suppressed, error) {
	s.checks = append(s.checks, append([]string(nil), emails...))
	if s.checkErr != nil {
		return nil, s.checkErr
	}
	var out []ports.Suppressed
	for _, e := range emails {
		reason, ok := s.suppressed[strings.ToLower(e)]
		if !ok {
			continue
		}
		reasons := s.causes[strings.ToLower(e)]
		if reasons == nil {
			reasons = []string{reason}
		}
		if s.withoutReasons {
			reasons = nil
		}
		out = append(out, ports.Suppressed{Email: e, Reason: reason, Reasons: reasons})
	}
	return out, nil
}

func (s *fakeSuppression) Add(_ context.Context, _ uuid.UUID, entry ports.SuppressionEntry) error {
	if s.addErr != nil {
		return s.addErr
	}
	s.added = append(s.added, entry)
	return nil
}

// renderedBody pasa el enlace de baja por html/template, como el servicio templates: el
// enlace llega escapado (& como &amp;) igual que en produccion.
var renderedBody = template.Must(template.New("body").Parse(`<p>Pedido</p><a href="{{.}}">Darse de baja</a>`))

// fakeTemplates renderiza con las variables reservadas a la vista, para poder comprobar
// que cambian por destinatario. Admite llamadas concurrentes (los lotes renderizan en
// paralelo).
type fakeTemplates struct {
	mu    sync.Mutex
	calls []ports.RenderRequest
	err   error
	// withoutUnsubscribe simula una plantilla que no usa unsubscribe_url.
	withoutUnsubscribe bool
	// kind es el tipo que informa templates; vacio simula una version sin el campo.
	kind string
	// extraHTML se anade al cuerpo renderizado (enlaces de contenido).
	extraHTML string
}

func (f *fakeTemplates) Render(_ context.Context, _ uuid.UUID, req ports.RenderRequest) (*ports.Rendered, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	err, without, kind, extra := f.err, f.withoutUnsubscribe, f.kind, f.extraHTML
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var html strings.Builder
	if without {
		html.WriteString("<p>Pedido</p>")
	} else if err := renderedBody.Execute(&html, req.Reserved.UnsubscribeURL); err != nil {
		return nil, err
	}
	html.WriteString(extra)
	version := 3
	if req.Version != nil {
		version = *req.Version
	}
	return &ports.Rendered{Subject: "Hola " + req.Reserved.RecipientEmail, HTML: html.String(), Text: "Pedido", Version: version, Kind: kind}, nil
}

func (f *fakeTemplates) callFor(email string) (ports.RenderRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Reserved.RecipientEmail == email {
			return c, true
		}
	}
	return ports.RenderRequest{}, false
}

// reputationCall es una autorizacion pedida.
type reputationCall struct {
	Class string
	Count int
}

// fakeReputation autoriza todo salvo que se fije una denegacion (auth) o una caida (err).
type fakeReputation struct {
	calls []reputationCall
	auth  *ports.Authorization
	err   error
}

func (f *fakeReputation) Authorize(_ context.Context, _ uuid.UUID, class string, count int) (*ports.Authorization, error) {
	f.calls = append(f.calls, reputationCall{Class: class, Count: count})
	if f.err != nil {
		return nil, f.err
	}
	if f.auth != nil {
		a := *f.auth
		return &a, nil
	}
	return &ports.Authorization{Allowed: true, Class: class, State: "healthy"}, nil
}

// fakeSender es el proveedor: errs se consume en orden; err se devuelve siempre.
type fakeSender struct {
	sent  []domain.OutgoingEmail
	errs  []error
	err   error
	calls int
}

func (s *fakeSender) Send(_ context.Context, email domain.OutgoingEmail) (string, error) {
	s.calls++
	if len(s.errs) > 0 {
		e := s.errs[0]
		s.errs = s.errs[1:]
		if e != nil {
			return "", e
		}
	} else if s.err != nil {
		return "", s.err
	}
	s.sent = append(s.sent, email)
	return fmt.Sprintf("ses-%d", s.calls), nil
}

type fakeLimiter struct{ waits int }

func (l *fakeLimiter) Wait(context.Context) error {
	l.waits++
	return nil
}

const testSigningKey = "0123456789abcdef0123456789abcdef-test"

type fixture struct {
	uc       *UseCase
	deps     Deps
	repo     *fakeRepo
	supp     *fakeSuppression
	tpl      *fakeTemplates
	rep      *fakeReputation
	sender   *fakeSender
	limiter  *fakeLimiter
	mSender  *fakeSender
	mLimiter *fakeLimiter
	links    *domain.LinkSigner
	metrics  *fakeMetrics
	tenant   uuid.UUID
	now      time.Time
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	links, err := domain.NewLinkSigner(testSigningKey, "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		repo:     newFakeRepo(),
		supp:     &fakeSuppression{suppressed: map[string]string{}},
		tpl:      &fakeTemplates{kind: domain.TemplateKindTransactional},
		rep:      &fakeReputation{},
		sender:   &fakeSender{},
		limiter:  &fakeLimiter{},
		mSender:  &fakeSender{},
		mLimiter: &fakeLimiter{},
		metrics:  &fakeMetrics{},
		links:    links,
		tenant:   uuid.New(),
		now:      time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC),
	}
	f.deps = Deps{
		Repo: f.repo, Events: f.repo, Suppression: f.supp, Templates: f.tpl, Reputation: f.rep,
		Sender: f.sender, Limiter: f.limiter, Marketing: Lane{Sender: f.mSender, Limiter: f.mLimiter},
		Links: links, UTM: domain.NewLinkTagger([]string{domain.HostOf("https://app.example.com")}),
		Config: cfg, Logger: zap.NewNop(), Metrics: f.metrics,
		Now: func() time.Time { return f.now },
	}
	f.uc = New(f.deps)
	return f
}

func (f *fixture) setDomain(tenantID uuid.UUID, name, status, purpose string) {
	f.repo.domains[domainKey(tenantID, name)] = domain.SendingDomain{
		TenantID: tenantID, Domain: name, Status: status, Purpose: purpose, UpdatedAt: f.now,
	}
}

// fastRetries acorta las esperas entre reintentos transitorios durante la prueba.
func fastRetries(t *testing.T) {
	t.Helper()
	saved := transientRetryDelays
	transientRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { transientRetryDelays = saved })
}

type fakeMetrics struct {
	mu       sync.Mutex
	attempts []string
	events   []string
	rejected []string
	accounts []domain.SESAccountStatus
	failures int
}

func (m *fakeMetrics) SendAttempt(class, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts = append(m.attempts, class+"/"+result)
}
func (m *fakeMetrics) SESEvent(t string) { m.mu.Lock(); m.events = append(m.events, t); m.mu.Unlock() }
func (m *fakeMetrics) SESEventRejected(r string) {
	m.mu.Lock()
	m.rejected = append(m.rejected, r)
	m.mu.Unlock()
}
func (m *fakeMetrics) SESAccount(s domain.SESAccountStatus) {
	m.mu.Lock()
	m.accounts = append(m.accounts, s)
	m.mu.Unlock()
}
func (m *fakeMetrics) SESAccountCheckFailed() { m.mu.Lock(); m.failures++; m.mu.Unlock() }
