// Package apptest son dobles en memoria de los puertos de automations, con las mismas
// reglas que la base (unicidad, reservas, transacciones que se deshacen) y vecinos que
// recuerdan cada llamada. Los usan las pruebas de app y del adaptador HTTP.
package apptest

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/automations/internal/domain"
	"github.com/alonsosss/corforce-email/services/automations/internal/ports"
	"github.com/google/uuid"
)

// Clock es un reloj que avanza a mano.
type Clock struct {
	mu sync.Mutex
	T  time.Time
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.T
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.T = c.T.Add(d)
	c.mu.Unlock()
}

// Outboxed es un evento encolado.
type Outboxed struct {
	Subject  string
	TenantID uuid.UUID
	Payload  map[string]any
}

// Store es la base de la empresa en memoria. Transact deshace todo lo escrito si fn falla,
// outbox incluida, como la base real.
type Store struct {
	mu         sync.Mutex
	Clock      *Clock
	Settings   map[uuid.UUID]domain.DOISettings
	Deliveries map[uuid.UUID]domain.DOIDelivery
	Workflows  map[uuid.UUID]domain.Workflow
	Runs       map[uuid.UUID]domain.Run
	Processed  map[string]uuid.UUID
	Outbox     []Outboxed
	// ContactLocks cuenta las llamadas a LockContact.
	ContactLocks int
	// FailAdvance hace fallar los proximos N avances, como un proceso que muere entre la
	// respuesta del vecino y el commit.
	FailAdvance int
	updates     int64
}

func NewStore(clock *Clock) *Store {
	return &Store{
		Clock: clock, Settings: map[uuid.UUID]domain.DOISettings{}, Deliveries: map[uuid.UUID]domain.DOIDelivery{},
		Workflows: map[uuid.UUID]domain.Workflow{}, Runs: map[uuid.UUID]domain.Run{}, Processed: map[string]uuid.UUID{},
	}
}

// ErrCrash simula la caida del proceso.
var ErrCrash = errors.New("caida simulada")

type snapshot struct {
	settings   map[uuid.UUID]domain.DOISettings
	deliveries map[uuid.UUID]domain.DOIDelivery
	workflows  map[uuid.UUID]domain.Workflow
	runs       map[uuid.UUID]domain.Run
	processed  map[string]uuid.UUID
	outbox     []Outboxed
}

func copyMap[K comparable, V any](in map[K]V, clone func(V) V) map[K]V {
	out := make(map[K]V, len(in))
	for k, v := range in {
		out[k] = clone(v)
	}
	return out
}

func same[V any](v V) V { return v }

func cloneWorkflow(w domain.Workflow) domain.Workflow {
	b, _ := json.Marshal(w.Steps)
	var steps []domain.Step
	_ = json.Unmarshal(b, &steps)
	w.Steps = steps
	return w
}

func (s *Store) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	s.mu.Lock()
	snap := snapshot{
		settings: copyMap(s.Settings, same[domain.DOISettings]), deliveries: copyMap(s.Deliveries, same[domain.DOIDelivery]),
		workflows: copyMap(s.Workflows, cloneWorkflow), runs: copyMap(s.Runs, same[domain.Run]),
		processed: copyMap(s.Processed, same[uuid.UUID]), outbox: append([]Outboxed(nil), s.Outbox...),
	}
	s.mu.Unlock()
	if err := fn(ctx); err != nil {
		s.mu.Lock()
		s.Settings, s.Deliveries, s.Workflows, s.Runs = snap.settings, snap.deliveries, snap.workflows, snap.runs
		s.Processed, s.Outbox = snap.processed, snap.outbox
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Store) Publish(_ context.Context, subject string, tenantID uuid.UUID, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Outbox = append(s.Outbox, Outboxed{Subject: subject, TenantID: tenantID, Payload: payload})
	return nil
}

// Published devuelve los eventos encolados con ese subject.
func (s *Store) Published(subject string) []Outboxed {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Outboxed
	for _, e := range s.Outbox {
		if e.Subject == subject {
			out = append(out, e)
		}
	}
	return out
}

// Run devuelve la ejecucion guardada.
func (s *Store) Run(id uuid.UUID) domain.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Runs[id]
}

// RunsOf devuelve las ejecuciones de un flujo.
func (s *Store) RunsOf(workflowID uuid.UUID) []domain.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Run
	for _, r := range s.Runs {
		if r.WorkflowID == workflowID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Workflow devuelve el flujo guardado.
func (s *Store) Workflow(id uuid.UUID) domain.Workflow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneWorkflow(s.Workflows[id])
}

// Delivery devuelve el intento del evento.
func (s *Store) Delivery(eventID string) (domain.DOIDelivery, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.Deliveries {
		if d.EventID == eventID {
			return d, true
		}
	}
	return domain.DOIDelivery{}, false
}

// touch da una marca de actualizacion estrictamente creciente, como updated_at.
func (s *Store) touch() time.Time {
	s.updates++
	return s.Clock.Now().Add(time.Duration(s.updates) * time.Microsecond)
}

// ── Ajustes y entregas del doble opt-in ─────────────────────────────────────

type Settings struct{ S *Store }

func (f Settings) GetDOISettings(_ context.Context, tenantID uuid.UUID) (*domain.DOISettings, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	s, ok := f.S.Settings[tenantID]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (f Settings) UpsertDOISettings(_ context.Context, s *domain.DOISettings) error {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	t := f.S.touch()
	s.UpdatedAt = &t
	f.S.Settings[s.TenantID] = *s
	return nil
}

type Deliveries struct{ S *Store }

func (f Deliveries) LockContact(context.Context, uuid.UUID, uuid.UUID) error {
	f.S.mu.Lock()
	f.S.ContactLocks++
	f.S.mu.Unlock()
	return nil
}

func (f Deliveries) GetByEvent(_ context.Context, tenantID uuid.UUID, eventID string) (*domain.DOIDelivery, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	for _, d := range f.S.Deliveries {
		if d.TenantID == tenantID && d.EventID == eventID {
			return &d, nil
		}
	}
	return nil, nil
}

func (f Deliveries) CountRecent(_ context.Context, tenantID, contactID uuid.UUID, dayStart, monthStart time.Time, exclude string) (int, int, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	day, month := 0, 0
	for _, d := range f.S.Deliveries {
		if d.TenantID != tenantID || d.ContactID != contactID || d.EventID == exclude {
			continue
		}
		if d.Status != domain.DOIPending && d.Status != domain.DOISent {
			continue
		}
		if !d.CreatedAt.Before(monthStart) {
			month++
			if !d.CreatedAt.Before(dayStart) {
				day++
			}
		}
	}
	return day, month, nil
}

func (f Deliveries) Insert(_ context.Context, d *domain.DOIDelivery) error {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	for _, x := range f.S.Deliveries {
		if x.EventID == d.EventID {
			return errors.New("duplicate key value violates unique constraint automations_doi_deliveries_event_key")
		}
	}
	now := f.S.Clock.Now()
	d.CreatedAt, d.UpdatedAt = now, now
	f.S.Deliveries[d.ID] = *d
	return nil
}

func (f Deliveries) update(tenantID, id uuid.UUID, fn func(d *domain.DOIDelivery)) bool {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	d, ok := f.S.Deliveries[id]
	if !ok || d.TenantID != tenantID || d.Status != domain.DOIPending {
		return false
	}
	fn(&d)
	d.UpdatedAt = f.S.Clock.Now()
	f.S.Deliveries[id] = d
	return true
}

func (f Deliveries) MarkSent(_ context.Context, tenantID, id uuid.UUID, messageID *uuid.UUID, at time.Time) (bool, error) {
	return f.update(tenantID, id, func(d *domain.DOIDelivery) {
		d.Status, d.MessageID, d.SentAt, d.Reason = domain.DOISent, messageID, &at, ""
	}), nil
}

func (f Deliveries) MarkFailed(_ context.Context, tenantID, id uuid.UUID, reason string) (bool, error) {
	return f.update(tenantID, id, func(d *domain.DOIDelivery) { d.Status, d.Reason = domain.DOIFailed, reason }), nil
}

func (f Deliveries) RecordAttempt(_ context.Context, tenantID, id uuid.UUID, reason string) (int, error) {
	attempts := 0
	f.update(tenantID, id, func(d *domain.DOIDelivery) {
		d.Attempts++
		d.Reason = reason
		attempts = d.Attempts
	})
	return attempts, nil
}

func (f Deliveries) List(_ context.Context, tenantID uuid.UUID, status domain.DOIStatus, page, perPage int) ([]domain.DOIDelivery, int64, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	var out []domain.DOIDelivery
	for _, d := range f.S.Deliveries {
		if d.TenantID == tenantID && (status == "" || d.Status == status) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return paginate(out, page, perPage), int64(len(out)), nil
}

func paginate[T any](in []T, page, perPage int) []T {
	if page < 1 {
		page = 1
	}
	from := (page - 1) * perPage
	if from >= len(in) {
		return []T{}
	}
	to := from + perPage
	if to > len(in) {
		to = len(in)
	}
	return in[from:to]
}

// ── Flujos ───────────────────────────────────────────────────────────────────

type Workflows struct{ S *Store }

func (f Workflows) Insert(_ context.Context, w *domain.Workflow) error {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	for _, x := range f.S.Workflows {
		if x.TenantID == w.TenantID && strings.EqualFold(x.Name, w.Name) {
			return domain.ErrNameTaken
		}
	}
	now := f.S.touch()
	w.CreatedAt, w.UpdatedAt = now, now
	f.S.Workflows[w.ID] = cloneWorkflow(*w)
	return nil
}

func (f Workflows) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	w, ok := f.S.Workflows[id]
	if !ok || w.TenantID != tenantID {
		return nil, domain.ErrWorkflowNotFound
	}
	c := cloneWorkflow(w)
	return &c, nil
}

func (f Workflows) GetForUpdate(ctx context.Context, tenantID, id uuid.UUID) (*domain.Workflow, error) {
	return f.Get(ctx, tenantID, id)
}

func (f Workflows) Update(_ context.Context, w *domain.Workflow) error {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	cur, ok := f.S.Workflows[w.ID]
	if !ok || cur.TenantID != w.TenantID {
		return domain.ErrWorkflowNotFound
	}
	for _, x := range f.S.Workflows {
		if x.ID != w.ID && x.TenantID == w.TenantID && strings.EqualFold(x.Name, w.Name) {
			return domain.ErrNameTaken
		}
	}
	w.UpdatedAt = f.S.touch()
	f.S.Workflows[w.ID] = cloneWorkflow(*w)
	return nil
}

func (f Workflows) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	w, ok := f.S.Workflows[id]
	if !ok || w.TenantID != tenantID {
		return domain.ErrWorkflowNotFound
	}
	delete(f.S.Workflows, id)
	for rid, r := range f.S.Runs {
		if r.WorkflowID == id {
			delete(f.S.Runs, rid)
		}
	}
	return nil
}

func (f Workflows) List(_ context.Context, tenantID uuid.UUID, fl ports.WorkflowFilter) ([]domain.Workflow, int64, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	var out []domain.Workflow
	for _, w := range f.S.Workflows {
		if w.TenantID != tenantID || (fl.Status != "" && w.Status != fl.Status) {
			continue
		}
		if fl.Search != "" && !strings.Contains(strings.ToLower(w.Name), strings.ToLower(fl.Search)) {
			continue
		}
		out = append(out, cloneWorkflow(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return paginate(out, fl.Page, fl.PerPage), int64(len(out)), nil
}

func (f Workflows) ListActiveByTrigger(_ context.Context, tenantID uuid.UUID, t domain.TriggerType) ([]domain.Workflow, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	var out []domain.Workflow
	for _, w := range f.S.Workflows {
		if w.TenantID == tenantID && w.Status == domain.StatusActive && w.Trigger.Type == t {
			out = append(out, cloneWorkflow(w))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ── Ejecuciones ──────────────────────────────────────────────────────────────

type Runs struct{ S *Store }

func (f Runs) Enroll(_ context.Context, r *domain.Run, reEntry bool) (bool, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	for _, x := range f.S.Runs {
		if x.WorkflowID != r.WorkflowID || x.ContactID != r.ContactID {
			continue
		}
		if x.TriggerEventID == r.TriggerEventID || x.EntryKey == r.EntryKey || !reEntry {
			return false, nil
		}
	}
	now := f.S.touch()
	r.CreatedAt, r.UpdatedAt = now, now
	f.S.Runs[r.ID] = *r
	return true, nil
}

func (f Runs) ClaimDue(_ context.Context, tenantID uuid.UUID, now, leaseUntil time.Time, limit int) ([]domain.Run, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	var due []domain.Run
	for _, r := range f.S.Runs {
		w := f.S.Workflows[r.WorkflowID]
		if r.TenantID != tenantID || w.Status != domain.StatusActive || r.NextRunAt.After(now) {
			continue
		}
		expired := r.Status == domain.RunRunning && r.LeaseUntil != nil && r.LeaseUntil.Before(now)
		if r.Status == domain.RunWaiting || expired {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].NextRunAt.Before(due[j].NextRunAt) })
	if len(due) > limit {
		due = due[:limit]
	}
	for i := range due {
		token, until := uuid.New(), leaseUntil
		due[i].Status, due[i].LeaseToken, due[i].LeaseUntil = domain.RunRunning, &token, &until
		f.S.Runs[due[i].ID] = due[i]
	}
	return due, nil
}

func (f Runs) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Run, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	r, ok := f.S.Runs[id]
	if !ok || r.TenantID != tenantID {
		return nil, domain.ErrRunNotFound
	}
	return &r, nil
}

func (f Runs) List(_ context.Context, tenantID uuid.UUID, fl ports.RunFilter) ([]domain.Run, int64, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	var out []domain.Run
	for _, r := range f.S.Runs {
		if r.TenantID == tenantID && r.WorkflowID == fl.WorkflowID && (fl.Status == "" || r.Status == fl.Status) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return paginate(out, fl.Page, fl.PerPage), int64(len(out)), nil
}

// held aplica fn si la ejecucion sigue running con la reserva de quien escribe.
func (f Runs) held(r *domain.Run, fn func(x *domain.Run) bool) bool {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	x, ok := f.S.Runs[r.ID]
	if !ok || x.TenantID != r.TenantID || x.Status != domain.RunRunning || x.LeaseToken == nil ||
		r.LeaseToken == nil || *x.LeaseToken != *r.LeaseToken {
		return false
	}
	if !fn(&x) {
		return false
	}
	x.LeaseToken, x.LeaseUntil, x.UpdatedAt = nil, nil, f.S.touch()
	f.S.Runs[r.ID] = x
	return true
}

func (f Runs) Advance(_ context.Context, r *domain.Run, next int, at time.Time) (bool, error) {
	f.S.mu.Lock()
	if f.S.FailAdvance > 0 {
		f.S.FailAdvance--
		f.S.mu.Unlock()
		return false, ErrCrash
	}
	f.S.mu.Unlock()
	return f.held(r, func(x *domain.Run) bool {
		if x.StepIndex != r.StepIndex {
			return false
		}
		x.StepIndex, x.Status, x.NextRunAt, x.Attempts, x.ErrorCode, x.LastError = next, domain.RunWaiting, at, 0, "", ""
		return true
	}), nil
}

func (f Runs) Reschedule(_ context.Context, r *domain.Run, at time.Time, attempts int, code, reason string) (bool, error) {
	return f.held(r, func(x *domain.Run) bool {
		x.Status, x.NextRunAt, x.Attempts, x.ErrorCode, x.LastError = domain.RunWaiting, at, attempts, code, reason
		return true
	}), nil
}

func (f Runs) Finish(_ context.Context, r *domain.Run, status domain.RunStatus, code, reason string, at time.Time) (bool, error) {
	return f.held(r, func(x *domain.Run) bool {
		x.Status, x.ErrorCode, x.LastError, x.FinishedAt = status, code, reason, &at
		return true
	}), nil
}

func (f Runs) Release(_ context.Context, r *domain.Run) (bool, error) {
	return f.held(r, func(x *domain.Run) bool {
		x.Status = domain.RunWaiting
		return true
	}), nil
}

func (f Runs) CancelByWorkflow(_ context.Context, tenantID, workflowID uuid.UUID, at time.Time) (int64, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	var n int64
	for id, r := range f.S.Runs {
		if r.TenantID == tenantID && r.WorkflowID == workflowID && (r.Status == domain.RunWaiting || r.Status == domain.RunRunning) {
			t := at
			r.Status, r.ErrorCode, r.FinishedAt, r.LeaseToken, r.LeaseUntil = domain.RunCancelled, domain.CodeWorkflowArchived, &t, nil, nil
			f.S.Runs[id] = r
			n++
		}
	}
	return n, nil
}

func (f Runs) CountRecentFailures(_ context.Context, tenantID, workflowID uuid.UUID, codes []string, since time.Time) (int, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	n := 0
	for _, r := range f.S.Runs {
		if r.TenantID != tenantID || r.WorkflowID != workflowID || r.Status != domain.RunFailed || r.FinishedAt == nil || r.FinishedAt.Before(since) {
			continue
		}
		for _, c := range codes {
			if r.ErrorCode == c {
				n++
				break
			}
		}
	}
	return n, nil
}

// ── Eventos procesados ───────────────────────────────────────────────────────

type Processed struct{ S *Store }

func (f Processed) IsProcessed(_ context.Context, _ uuid.UUID, eventID string) (bool, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	_, ok := f.S.Processed[eventID]
	return ok, nil
}

func (f Processed) MarkProcessed(_ context.Context, tenantID uuid.UUID, eventID string, _ time.Time) (bool, error) {
	f.S.mu.Lock()
	defer f.S.mu.Unlock()
	if _, ok := f.S.Processed[eventID]; ok {
		return false, nil
	}
	f.S.Processed[eventID] = tenantID
	return true, nil
}

func (f Processed) PruneProcessed(context.Context, uuid.UUID, time.Time) (int64, error) {
	return 0, nil
}

// ── Vecinos ──────────────────────────────────────────────────────────────────

// Sender es transactional: recuerda cada llamada y, como el real, una clave ya usada
// devuelve lo mismo sin crear otro mensaje. Errs se consume en orden (nil = exito).
type Sender struct {
	mu             sync.Mutex
	DOICalls       []ports.DOIMessage
	MarketingCalls []ports.MarketingMessage
	DOIErrs        []error
	MarketingErrs  []error
	// DOIStatus es el estado del mensaje creado; vacio = queued.
	DOIStatus     string
	DOISuppressed []ports.Suppressed
	created       map[string]bool
}

func (s *Sender) pop(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	e := (*errs)[0]
	*errs = (*errs)[1:]
	return e
}

func (s *Sender) create(key string) uuid.UUID {
	if s.created == nil {
		s.created = map[string]bool{}
	}
	s.created[key] = true
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(key))
}

func (s *Sender) SendDOI(_ context.Context, _ uuid.UUID, m ports.DOIMessage) (*ports.DOIResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DOICalls = append(s.DOICalls, m)
	if err := s.pop(&s.DOIErrs); err != nil {
		return nil, err
	}
	id := s.create(m.IdempotencyKey)
	status := s.DOIStatus
	if status == "" {
		status = "queued"
	}
	return &ports.DOIResult{MessageID: &id, Status: status, Suppressed: s.DOISuppressed}, nil
}

func (s *Sender) SendMarketing(_ context.Context, _ uuid.UUID, m ports.MarketingMessage) (*ports.BatchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MarketingCalls = append(s.MarketingCalls, m)
	if err := s.pop(&s.MarketingErrs); err != nil {
		return nil, err
	}
	return &ports.BatchResult{Accepted: 1, MessageIDs: []uuid.UUID{s.create(m.IdempotencyKey)}}, nil
}

// Created es el numero de mensajes distintos creados (claves unicas).
func (s *Sender) Created() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.created)
}

// Contacts es contacts: enviables por id y miembros por lista.
type Contacts struct {
	mu        sync.Mutex
	Sendables map[uuid.UUID]domain.Contact
	Members   map[uuid.UUID]map[uuid.UUID]bool
	// MissingLists son listas que no existen (404).
	MissingLists map[uuid.UUID]bool
	Err          error
	Calls        int
}

func NewContacts() *Contacts {
	return &Contacts{Sendables: map[uuid.UUID]domain.Contact{}, Members: map[uuid.UUID]map[uuid.UUID]bool{}, MissingLists: map[uuid.UUID]bool{}}
}

func (c *Contacts) Sendable(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]domain.Contact, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	if c.Err != nil {
		return nil, c.Err
	}
	var out []domain.Contact
	for _, id := range ids {
		if ct, ok := c.Sendables[id]; ok {
			out = append(out, ct)
		}
	}
	return out, nil
}

func (c *Contacts) missing(listID uuid.UUID) error {
	if c.MissingLists[listID] {
		return &ports.RejectedError{Status: 404, Code: "NOT_FOUND", Message: "lista no encontrada"}
	}
	return nil
}

func (c *Contacts) ListMembers(_ context.Context, _ uuid.UUID, listID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	if c.Err != nil {
		return nil, c.Err
	}
	if err := c.missing(listID); err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, id := range ids {
		if c.Members[listID][id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func (c *Contacts) AddToList(_ context.Context, _ uuid.UUID, listID uuid.UUID, ids []uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	if c.Err != nil {
		return c.Err
	}
	if err := c.missing(listID); err != nil {
		return err
	}
	if c.Members[listID] == nil {
		c.Members[listID] = map[uuid.UUID]bool{}
	}
	for _, id := range ids {
		c.Members[listID][id] = true
	}
	return nil
}

func (c *Contacts) RemoveFromList(_ context.Context, _ uuid.UUID, listID uuid.UUID, ids []uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	if c.Err != nil {
		return c.Err
	}
	if err := c.missing(listID); err != nil {
		return err
	}
	for _, id := range ids {
		delete(c.Members[listID], id)
	}
	return nil
}

// IsMember dice si el contacto esta en la lista.
func (c *Contacts) IsMember(listID, contactID uuid.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Members[listID][contactID]
}

// Templates renderiza como templates: con confirm_url en las variables lo muestra en el
// HTML salvo que OmitConfirm diga lo contrario.
type Templates struct {
	mu          sync.Mutex
	Kind        string
	Version     int
	Err         error
	OmitConfirm bool
	Calls       []ports.RenderRequest
}

func (t *Templates) Render(_ context.Context, _ uuid.UUID, req ports.RenderRequest) (*ports.Rendered, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Calls = append(t.Calls, req)
	if t.Err != nil {
		return nil, t.Err
	}
	html := "<p>Hola</p>"
	if raw, ok := req.Variables["confirm_url"]; ok && !t.OmitConfirm {
		var u string
		_ = json.Unmarshal(raw, &u)
		html += `<a href="` + u + `">Confirmar</a>`
	}
	version := t.Version
	if req.Version != nil {
		version = *req.Version
	}
	return &ports.Rendered{Subject: "Asunto", HTML: html, Text: "Hola", Version: version, Kind: t.Kind}, nil
}
