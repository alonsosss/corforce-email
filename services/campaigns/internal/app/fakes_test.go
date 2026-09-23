package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/campaigns/internal/domain"
	"github.com/alonsosss/corforce-email/services/campaigns/internal/ports"
	"github.com/google/uuid"
)

// errCrash simula que el proceso muere despues del 202 de transactional y antes del
// commit que cierra el lote.
var errCrash = errors.New("caida simulada antes del commit")

type publishedEvent struct {
	subject string
	payload map[string]any
}

type engagementRow struct {
	campaignID uuid.UUID
	contactID  *uuid.UUID
	kind       domain.PhaseKind
	variant    *int
	delivered  bool
	opened     bool
	clicked    bool
}

type recipientKey struct {
	campaignID uuid.UUID
	round      domain.Round
	contactID  uuid.UUID
}

// memStore es la base en memoria de las pruebas: transacciones con rollback, a lo sumo
// un lote pendiente por campana y las mismas guardas por estado y reserva que el SQL.
type memStore struct {
	campaigns  map[uuid.UUID]domain.Campaign
	batches    map[uuid.UUID]domain.Batch
	processed  map[string]time.Time
	engagement map[uuid.UUID]engagementRow
	phases     map[uuid.UUID]domain.Phase
	recipients map[recipientKey]uuid.UUID
	events     []publishedEvent

	// lockedByOther simula otra transaccion con la campana bloqueada (SKIP LOCKED).
	lockedByOther map[uuid.UUID]bool
	// failMarkDelivered hace fallar ese numero de cierres de lote (errCrash).
	failMarkDelivered int
}

func newMemStore() *memStore {
	return &memStore{
		campaigns:     map[uuid.UUID]domain.Campaign{},
		batches:       map[uuid.UUID]domain.Batch{},
		processed:     map[string]time.Time{},
		engagement:    map[uuid.UUID]engagementRow{},
		phases:        map[uuid.UUID]domain.Phase{},
		recipients:    map[recipientKey]uuid.UUID{},
		lockedByOther: map[uuid.UUID]bool{},
	}
}

func (s *memStore) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	campaigns := make(map[uuid.UUID]domain.Campaign, len(s.campaigns))
	for k, v := range s.campaigns {
		campaigns[k] = v
	}
	batches := make(map[uuid.UUID]domain.Batch, len(s.batches))
	for k, v := range s.batches {
		batches[k] = v
	}
	processed := make(map[string]time.Time, len(s.processed))
	for k, v := range s.processed {
		processed[k] = v
	}
	engagement := make(map[uuid.UUID]engagementRow, len(s.engagement))
	for k, v := range s.engagement {
		engagement[k] = v
	}
	phases := make(map[uuid.UUID]domain.Phase, len(s.phases))
	for k, v := range s.phases {
		phases[k] = v
	}
	recipients := make(map[recipientKey]uuid.UUID, len(s.recipients))
	for k, v := range s.recipients {
		recipients[k] = v
	}
	events := append([]publishedEvent(nil), s.events...)
	if err := fn(ctx); err != nil {
		s.campaigns, s.batches, s.processed, s.engagement, s.events = campaigns, batches, processed, engagement, events
		s.phases, s.recipients = phases, recipients
		return err
	}
	return nil
}

// ── Campanas ─────────────────────────────────────────────────────────────────

type fakeCampaigns struct {
	s   *memStore
	now func() time.Time
}

func (f fakeCampaigns) nameTaken(c *domain.Campaign) bool {
	for id, o := range f.s.campaigns {
		if id != c.ID && o.TenantID == c.TenantID && strings.EqualFold(o.Name, c.Name) {
			return true
		}
	}
	return false
}

func (f fakeCampaigns) Insert(_ context.Context, c *domain.Campaign) error {
	if f.nameTaken(c) {
		return domain.ErrNameTaken
	}
	c.CreatedAt = f.now()
	c.UpdatedAt = c.CreatedAt
	f.s.campaigns[c.ID] = *c
	return nil
}

func (f fakeCampaigns) get(tenantID, id uuid.UUID) (*domain.Campaign, error) {
	c, ok := f.s.campaigns[id]
	if !ok || c.TenantID != tenantID {
		return nil, domain.ErrCampaignNotFound
	}
	return cloneCampaign(c), nil
}

// cloneCampaign copia la configuracion A/B: la base devuelve una fila nueva en cada
// lectura, y la prueba no debe ver cambios que nadie guardo.
func cloneCampaign(c domain.Campaign) *domain.Campaign {
	if c.ABTest != nil {
		ab := *c.ABTest
		ab.Variants = append([]domain.ABVariant(nil), c.ABTest.Variants...)
		c.ABTest = &ab
	}
	return &c
}

func (f fakeCampaigns) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error) {
	return f.get(tenantID, id)
}

func (f fakeCampaigns) GetForUpdate(_ context.Context, tenantID, id uuid.UUID) (*domain.Campaign, error) {
	return f.get(tenantID, id)
}

func (f fakeCampaigns) LockRunnable(_ context.Context, tenantID, id uuid.UUID, now time.Time) (*domain.Campaign, error) {
	if f.s.lockedByOther[id] {
		return nil, nil
	}
	c, err := f.get(tenantID, id)
	if err != nil {
		return nil, nil
	}
	if c.Status != domain.StatusSending || (c.ResumeAfter != nil && c.ResumeAfter.After(now)) {
		return nil, nil
	}
	return c, nil
}

func (f fakeCampaigns) Update(_ context.Context, c *domain.Campaign) error {
	old, ok := f.s.campaigns[c.ID]
	if !ok {
		return domain.ErrCampaignNotFound
	}
	if f.nameTaken(c) {
		return domain.ErrNameTaken
	}
	next := *cloneCampaign(*c)
	next.Counters = old.Counters
	next.UpdatedAt = f.now()
	c.UpdatedAt = next.UpdatedAt
	f.s.campaigns[c.ID] = next
	return nil
}

func (f fakeCampaigns) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	if _, err := f.get(tenantID, id); err != nil {
		return err
	}
	delete(f.s.campaigns, id)
	for bid, b := range f.s.batches {
		if b.CampaignID == id {
			delete(f.s.batches, bid)
		}
	}
	for mid, e := range f.s.engagement {
		if e.campaignID == id {
			delete(f.s.engagement, mid)
		}
	}
	for pid, p := range f.s.phases {
		if p.CampaignID == id {
			delete(f.s.phases, pid)
		}
	}
	for k := range f.s.recipients {
		if k.campaignID == id {
			delete(f.s.recipients, k)
		}
	}
	return nil
}

func (f fakeCampaigns) List(_ context.Context, tenantID uuid.UUID, fl ports.ListFilter) ([]domain.Campaign, int64, error) {
	var out []domain.Campaign
	for _, c := range f.s.campaigns {
		if c.TenantID != tenantID || (fl.Status != "" && c.Status != fl.Status) {
			continue
		}
		if fl.Search != "" && !strings.Contains(strings.ToLower(c.Name), strings.ToLower(fl.Search)) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, int64(len(out)), nil
}

func (f fakeCampaigns) StartDue(_ context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]domain.Campaign, error) {
	var due []domain.Campaign
	for _, c := range f.s.campaigns {
		if c.TenantID == tenantID && c.Status == domain.StatusScheduled && !c.ScheduledAt.After(now) && !f.s.lockedByOther[c.ID] {
			due = append(due, c)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].ScheduledAt.Before(*due[j].ScheduledAt) })
	if len(due) > limit {
		due = due[:limit]
	}
	for i := range due {
		due[i].Status = domain.StatusSending
		if due[i].StartedAt == nil {
			started := now
			due[i].StartedAt = &started
		}
		due[i].ResumeAfter = nil
		f.s.campaigns[due[i].ID] = due[i]
	}
	return due, nil
}

func (f fakeCampaigns) ListRunnable(_ context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for _, c := range f.s.campaigns {
		if c.TenantID == tenantID && c.Status == domain.StatusSending && (c.ResumeAfter == nil || !c.ResumeAfter.After(now)) {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (f fakeCampaigns) AddDeliveryTotals(_ context.Context, tenantID, id uuid.UUID, targeted, accepted, suppressed int) error {
	c, err := f.get(tenantID, id)
	if err != nil {
		return err
	}
	c.Counters.Targeted += int64(targeted)
	c.Counters.Accepted += int64(accepted)
	c.Counters.Suppressed += int64(suppressed)
	f.s.campaigns[id] = *c
	return nil
}

// ── Lotes ────────────────────────────────────────────────────────────────────

type fakeBatches struct {
	s   *memStore
	now func() time.Time
}

func sameToken(a, b *uuid.UUID) bool { return a != nil && b != nil && *a == *b }

func (f fakeBatches) Pending(_ context.Context, tenantID, campaignID uuid.UUID) (*domain.Batch, error) {
	for _, b := range f.s.batches {
		if b.TenantID == tenantID && b.CampaignID == campaignID && b.Status == domain.BatchPending {
			b.Page = append([]domain.Recipient(nil), b.Page...)
			return &b, nil
		}
	}
	return nil, nil
}

func (f fakeBatches) Last(_ context.Context, tenantID, campaignID uuid.UUID) (*domain.Batch, error) {
	var last *domain.Batch
	for _, b := range f.s.batches {
		if b.TenantID == tenantID && b.CampaignID == campaignID && (last == nil || b.Seq > last.Seq) {
			b := b
			b.Page, b.PageFetched = nil, false
			last = &b
		}
	}
	return last, nil
}

func (f fakeBatches) Insert(_ context.Context, b *domain.Batch) error {
	for _, o := range f.s.batches {
		if o.CampaignID == b.CampaignID && (o.Seq == b.Seq || o.Status == domain.BatchPending) {
			return fmt.Errorf("violacion de unicidad en el lote %d", b.Seq)
		}
	}
	b.CreatedAt = f.now()
	b.UpdatedAt = b.CreatedAt
	f.s.batches[b.ID] = *b
	return nil
}

func (f fakeBatches) Lease(_ context.Context, b *domain.Batch) error {
	st, ok := f.s.batches[b.ID]
	if !ok || st.Status != domain.BatchPending {
		return fmt.Errorf("el lote %s ya no esta pendiente", b.ID)
	}
	st.LeasedUntil, st.LeaseToken = b.LeasedUntil, b.LeaseToken
	f.s.batches[b.ID] = st
	return nil
}

func (f fakeBatches) SavePage(_ context.Context, b *domain.Batch) (bool, error) {
	st, ok := f.s.batches[b.ID]
	if !ok || st.Status != domain.BatchPending || !sameToken(st.LeaseToken, b.LeaseToken) || st.PageFetched {
		return false, nil
	}
	if f.s.campaigns[st.CampaignID].Status != domain.StatusSending {
		return false, nil
	}
	st.Page = append([]domain.Recipient{}, b.Page...)
	st.CursorOut = b.CursorOut
	st.Recipients = len(b.Page)
	st.PageFetched = true
	f.s.batches[b.ID] = st
	return true, nil
}

func (f fakeBatches) MarkDelivered(_ context.Context, tenantID, id uuid.UUID, accepted, suppressed int) (bool, error) {
	if f.s.failMarkDelivered > 0 {
		f.s.failMarkDelivered--
		return false, errCrash
	}
	st, ok := f.s.batches[id]
	if !ok || st.TenantID != tenantID || st.Status != domain.BatchPending {
		return false, nil
	}
	st.Status, st.Accepted, st.Suppressed = domain.BatchDelivered, accepted, suppressed
	st.Page, st.PageFetched, st.LeasedUntil, st.LeaseToken, st.LastError = nil, false, nil, nil, ""
	f.s.batches[id] = st
	return true, nil
}

func (f fakeBatches) MarkFailed(_ context.Context, b *domain.Batch, reason string) (bool, error) {
	st, ok := f.s.batches[b.ID]
	if !ok || st.Status != domain.BatchPending || !sameToken(st.LeaseToken, b.LeaseToken) {
		return false, nil
	}
	st.Status, st.LastError = domain.BatchFailed, reason
	st.Page, st.PageFetched, st.LeasedUntil, st.LeaseToken = nil, false, nil, nil
	f.s.batches[b.ID] = st
	return true, nil
}

func (f fakeBatches) RecordFailure(_ context.Context, b *domain.Batch, reason string) (int, bool, error) {
	st, ok := f.s.batches[b.ID]
	if !ok || st.Status != domain.BatchPending || !sameToken(st.LeaseToken, b.LeaseToken) {
		return 0, false, nil
	}
	st.Attempts++
	st.LastError, st.LeasedUntil, st.LeaseToken = reason, nil, nil
	f.s.batches[b.ID] = st
	return st.Attempts, true, nil
}

func (f fakeBatches) Release(_ context.Context, b *domain.Batch, lastError string) (bool, error) {
	st, ok := f.s.batches[b.ID]
	if !ok || st.Status != domain.BatchPending || !sameToken(st.LeaseToken, b.LeaseToken) {
		return false, nil
	}
	st.LastError, st.LeasedUntil, st.LeaseToken = lastError, nil, nil
	f.s.batches[b.ID] = st
	return true, nil
}

func (f fakeBatches) ResetAttempts(_ context.Context, tenantID, campaignID uuid.UUID) error {
	for id, b := range f.s.batches {
		if b.TenantID == tenantID && b.CampaignID == campaignID && b.Status == domain.BatchPending {
			b.Attempts = 0
			f.s.batches[id] = b
		}
	}
	return nil
}

func (f fakeBatches) DiscardPendingPages(_ context.Context, tenantID, campaignID uuid.UUID) error {
	for id, b := range f.s.batches {
		if b.TenantID == tenantID && b.CampaignID == campaignID && b.Status == domain.BatchPending {
			b.Page, b.PageFetched, b.CursorOut, b.Recipients = nil, false, nil, 0
			f.s.batches[id] = b
		}
	}
	return nil
}

func (f fakeBatches) List(_ context.Context, tenantID, campaignID uuid.UUID, page, perPage int) ([]domain.Batch, int64, error) {
	var out []domain.Batch
	for _, b := range f.s.batches {
		if b.TenantID == tenantID && b.CampaignID == campaignID {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	return out, int64(len(out)), nil
}

// ── Estadisticas y eventos ───────────────────────────────────────────────────

type fakeStats struct{ s *memStore }

func (f fakeStats) MarkProcessed(_ context.Context, _ uuid.UUID, eventID string, at time.Time) (bool, error) {
	if _, ok := f.s.processed[eventID]; ok {
		return false, nil
	}
	f.s.processed[eventID] = at
	return true, nil
}

func (f fakeStats) NoteDelivery(_ context.Context, _ uuid.UUID, campaignID, messageID uuid.UUID, contactID *uuid.UUID, _ time.Time) error {
	if _, ok := f.s.campaigns[campaignID]; !ok {
		return domain.ErrCampaignNotFound
	}
	row := f.s.engagement[messageID]
	row.campaignID = campaignID
	if row.contactID == nil {
		row.contactID = contactID
	}
	row.delivered = true
	f.s.engagement[messageID] = row
	return nil
}

func (f fakeStats) FirstEngagement(_ context.Context, _ uuid.UUID, campaignID, messageID uuid.UUID, contactID *uuid.UUID, kind domain.DeliveryKind, _ time.Time) (bool, error) {
	if _, ok := f.s.campaigns[campaignID]; !ok {
		return false, domain.ErrCampaignNotFound
	}
	row := f.s.engagement[messageID]
	row.campaignID = campaignID
	if row.contactID == nil {
		row.contactID = contactID
	}
	switch kind {
	case domain.KindOpened:
		if row.opened {
			return false, nil
		}
		row.opened = true
	case domain.KindClicked:
		if row.clicked {
			return false, nil
		}
		row.clicked = true
	default:
		return false, fmt.Errorf("%s no se cuenta por mensaje", kind)
	}
	f.s.engagement[messageID] = row
	return true, nil
}

func (f fakeStats) IncrementCounter(_ context.Context, tenantID, campaignID uuid.UUID, kind domain.DeliveryKind) (bool, error) {
	c, ok := f.s.campaigns[campaignID]
	if !ok || c.TenantID != tenantID {
		return false, nil
	}
	counters := map[domain.DeliveryKind]*int64{
		domain.KindSent: &c.Counters.Sent, domain.KindDelivered: &c.Counters.Delivered,
		domain.KindBounced: &c.Counters.Bounced, domain.KindComplained: &c.Counters.Complained,
		domain.KindOpened: &c.Counters.Opened, domain.KindClicked: &c.Counters.Clicked,
		domain.KindUnsubscribed: &c.Counters.Unsubscribed, domain.KindFailed: &c.Counters.Failed,
	}
	*counters[kind]++
	f.s.campaigns[campaignID] = c
	return true, nil
}

func (f fakeStats) PruneProcessed(_ context.Context, _ uuid.UUID, before time.Time) (int64, error) {
	var n int64
	for id, at := range f.s.processed {
		if at.Before(before) {
			delete(f.s.processed, id)
			n++
		}
	}
	return n, nil
}

// ── Fases y destinatarios ────────────────────────────────────────────────────

type fakePhases struct {
	s   *memStore
	now func() time.Time
}

func (f fakePhases) List(_ context.Context, tenantID, campaignID uuid.UUID) ([]domain.Phase, error) {
	var out []domain.Phase
	for _, p := range f.s.phases {
		if p.TenantID == tenantID && p.CampaignID == campaignID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ordinal != out[j].Ordinal {
			return out[i].Ordinal < out[j].Ordinal
		}
		a, b := out[i].SlotAt, out[j].SlotAt
		if a != nil && b != nil && !a.Equal(*b) {
			return a.Before(*b)
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (f fakePhases) Get(_ context.Context, tenantID, id uuid.UUID) (*domain.Phase, error) {
	p, ok := f.s.phases[id]
	if !ok || p.TenantID != tenantID {
		return nil, fmt.Errorf("fase %s inexistente", id)
	}
	return &p, nil
}

func (f fakePhases) Insert(_ context.Context, p *domain.Phase) (bool, error) {
	for _, o := range f.s.phases {
		if o.CampaignID == p.CampaignID && o.Key == p.Key {
			return false, nil
		}
	}
	p.CreatedAt = f.now()
	p.UpdatedAt = p.CreatedAt
	f.s.phases[p.ID] = *p
	return true, nil
}

func (f fakePhases) Update(_ context.Context, p *domain.Phase) error {
	st, ok := f.s.phases[p.ID]
	if !ok {
		return fmt.Errorf("fase %s inexistente", p.ID)
	}
	st.Status, st.NotBefore, st.StartedAt, st.CompletedAt = p.Status, p.NotBefore, p.StartedAt, p.CompletedAt
	f.s.phases[p.ID] = st
	return nil
}

func (f fakePhases) AddTotals(_ context.Context, _ uuid.UUID, id uuid.UUID, targeted, accepted, suppressed int) error {
	st, ok := f.s.phases[id]
	if !ok {
		return fmt.Errorf("fase %s inexistente", id)
	}
	st.Targeted += targeted
	st.Accepted += accepted
	st.Suppressed += suppressed
	f.s.phases[id] = st
	return nil
}

type fakeLedger struct{ s *memStore }

func (f fakeLedger) RecordRecipients(_ context.Context, _ uuid.UUID, campaignID, phaseID uuid.UUID, round domain.Round, contactIDs []uuid.UUID) error {
	for _, id := range contactIDs {
		k := recipientKey{campaignID: campaignID, round: round, contactID: id}
		if _, ok := f.s.recipients[k]; !ok {
			f.s.recipients[k] = phaseID
		}
	}
	return nil
}

func (f fakeLedger) RecordMessages(_ context.Context, _ uuid.UUID, campaignID uuid.UUID, kind domain.PhaseKind, variant *int, messageIDs []uuid.UUID) error {
	for _, id := range messageIDs {
		row := f.s.engagement[id]
		row.campaignID, row.kind, row.variant = campaignID, kind, variant
		f.s.engagement[id] = row
	}
	return nil
}

func (f fakeLedger) Sent(_ context.Context, _ uuid.UUID, campaignID uuid.UUID, contactIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, id := range contactIDs {
		if _, ok := f.s.recipients[recipientKey{campaignID: campaignID, round: domain.RoundInitial, contactID: id}]; ok {
			out[id] = true
		}
	}
	return out, nil
}

func (f fakeLedger) ResendEligible(_ context.Context, _ uuid.UUID, campaignID uuid.UUID, contactIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, id := range contactIDs {
		if _, ok := f.s.recipients[recipientKey{campaignID: campaignID, round: domain.RoundInitial, contactID: id}]; !ok {
			continue
		}
		if _, ok := f.s.recipients[recipientKey{campaignID: campaignID, round: domain.RoundResend, contactID: id}]; ok {
			continue
		}
		delivered, engaged := false, false
		for _, e := range f.s.engagement {
			if e.campaignID != campaignID || e.contactID == nil || *e.contactID != id {
				continue
			}
			if e.delivered && e.kind != domain.PhaseResend {
				delivered = true
			}
			if e.opened || e.clicked {
				engaged = true
			}
		}
		if delivered && !engaged {
			out[id] = true
		}
	}
	return out, nil
}

func (f fakeLedger) Engagement(_ context.Context, _ uuid.UUID, campaignID uuid.UUID) ([]domain.PhaseEngagement, error) {
	type key struct {
		kind    domain.PhaseKind
		variant int
	}
	agg := map[key]*domain.PhaseEngagement{}
	for _, e := range f.s.engagement {
		if e.campaignID != campaignID || e.kind == "" {
			continue
		}
		k := key{kind: e.kind, variant: -1}
		if e.variant != nil {
			k.variant = *e.variant
		}
		row, ok := agg[k]
		if !ok {
			row = &domain.PhaseEngagement{Kind: e.kind, Variant: e.variant}
			agg[k] = row
		}
		row.Accepted++
		if e.delivered {
			row.Delivered++
		}
		if e.opened {
			row.Opened++
		}
		if e.clicked {
			row.Clicked++
		}
	}
	out := make([]domain.PhaseEngagement, 0, len(agg))
	for _, r := range agg {
		out = append(out, *r)
	}
	return out, nil
}

type fakeEvents struct{ s *memStore }

func (f fakeEvents) Publish(_ context.Context, subject string, _ uuid.UUID, payload map[string]any) error {
	f.s.events = append(f.s.events, publishedEvent{subject: subject, payload: payload})
	return nil
}

// ── Vecinos ──────────────────────────────────────────────────────────────────

// fakeAudience sirve paginas por cursor ("" = la primera) y registra cada consulta.
type fakeAudience struct {
	pages map[string]*ports.AudiencePage
	errs  []error
	calls []ports.AudienceQuery
	// hook corre al empezar cada consulta (las pruebas de integracion miran la base ahi).
	hook func(q ports.AudienceQuery)
}

func (f *fakeAudience) Audience(_ context.Context, _ uuid.UUID, q ports.AudienceQuery) (*ports.AudiencePage, error) {
	if f.hook != nil {
		f.hook(q)
	}
	f.calls = append(f.calls, q)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	key := ""
	if q.Cursor != nil {
		key = *q.Cursor
	}
	if p, ok := f.pages[key]; ok {
		return p, nil
	}
	return &ports.AudiencePage{}, nil
}

// fakeSender se comporta como el lote de transactional: con la misma clave devuelve el
// mismo resultado sin crear mensajes nuevos.
type fakeSender struct {
	errs     []error
	suppress map[string]bool
	results  map[string]*ports.BatchResult
	created  int
	calls    []ports.BatchRequest
	// hook corre al empezar cada envio.
	hook func(r ports.BatchRequest)
}

func (f *fakeSender) SendBatch(_ context.Context, _ uuid.UUID, r ports.BatchRequest) (*ports.BatchResult, error) {
	if f.hook != nil {
		f.hook(r)
	}
	f.calls = append(f.calls, r)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	if res, ok := f.results[r.IdempotencyKey]; ok {
		return res, nil
	}
	res := &ports.BatchResult{Suppressed: []ports.SuppressedRecipient{}}
	for _, rc := range r.Recipients {
		if f.suppress[rc.Email] {
			res.Suppressed = append(res.Suppressed, ports.SuppressedRecipient{Email: rc.Email, Reason: "unsubscribe"})
			continue
		}
		res.Accepted++
		res.MessageIDs = append(res.MessageIDs, uuid.New())
	}
	f.created += res.Accepted
	f.results[r.IdempotencyKey] = res
	return res, nil
}

type fakeTemplates struct {
	version int
	err     error
	calls   int
}

func (f *fakeTemplates) PublishedVersion(_ context.Context, _, _ uuid.UUID) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	return f.version, nil
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// ── Arnes ────────────────────────────────────────────────────────────────────

type harness struct {
	uc        *UseCase
	store     *memStore
	audience  *fakeAudience
	sender    *fakeSender
	templates *fakeTemplates
	clock     *fakeClock
	tenantID  uuid.UUID
	// ctx es el de las operaciones de los ayudantes: las pruebas de integracion le ponen
	// el pool de la empresa.
	ctx context.Context
}

func newHarness() *harness {
	h := &harness{
		store:     newMemStore(),
		audience:  &fakeAudience{pages: map[string]*ports.AudiencePage{}},
		sender:    &fakeSender{suppress: map[string]bool{}, results: map[string]*ports.BatchResult{}},
		templates: &fakeTemplates{version: 1},
		clock:     &fakeClock{t: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)},
		tenantID:  uuid.New(),
		ctx:       context.Background(),
	}
	h.uc = New(Deps{
		Campaigns: fakeCampaigns{s: h.store, now: h.clock.now},
		Batches:   fakeBatches{s: h.store, now: h.clock.now},
		Phases:    fakePhases{s: h.store, now: h.clock.now},
		Ledger:    fakeLedger{s: h.store},
		Stats:     fakeStats{s: h.store},
		Tx:        h.store,
		Events:    fakeEvents{s: h.store},
		Audience:  h.audience,
		Sender:    h.sender,
		Templates: h.templates,
		Config:    Config{BatchSize: domain.MaxBatchSize},
		Now:       h.clock.now,
	})
	return h
}

func (h *harness) draft(name string) *domain.Campaign {
	c, err := domain.NewCampaign(h.tenantID, domain.NewCampaignInput{
		Name: name, TemplateID: uuid.New(), FromEmail: "news@shop.example.com", FromName: "Tienda",
		Audience: domain.Audience{ListIDs: []uuid.UUID{uuid.New()}}, CreatedBy: uuid.New(),
	})
	if err != nil {
		panic(err)
	}
	c.CreatedAt = h.clock.now()
	h.store.campaigns[c.ID] = *c
	return c
}

func (h *harness) sending(name string) *domain.Campaign {
	c := h.draft(name)
	version, started := 1, h.clock.now()
	c.Status, c.TemplateVersion, c.StartedAt = domain.StatusSending, &version, &started
	h.store.campaigns[c.ID] = *c
	return c
}

func (h *harness) campaign(id uuid.UUID) domain.Campaign { return h.store.campaigns[id] }

func (h *harness) batches(campaignID uuid.UUID) []domain.Batch {
	var out []domain.Batch
	for _, b := range h.store.batches {
		if b.CampaignID == campaignID {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func (h *harness) published(subject string) *publishedEvent {
	for i := range h.store.events {
		if h.store.events[i].subject == subject {
			return &h.store.events[i]
		}
	}
	return nil
}

func contact(email string) domain.Contact {
	return domain.Contact{
		ID: uuid.New(), Email: email, FirstName: "Ana", LastName: "Diaz",
		Attributes: map[string]json.RawMessage{"plan": json.RawMessage(`"pro"`)},
	}
}

func strPtr(s string) *string { return &s }
