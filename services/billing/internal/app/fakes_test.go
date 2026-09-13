package app

import (
	"context"
	"time"

	"github.com/alonsosss/corforce-email/services/billing/internal/domain"
	"github.com/alonsosss/corforce-email/services/billing/internal/ports"
	"github.com/google/uuid"
)

const dayLayout = "2006-01-02"

type counterKey struct {
	tenant   uuid.UUID
	resource domain.Resource
	period   string
}

type itemKey struct {
	tenant   uuid.UUID
	resource domain.Resource
	key      string
	source   string
}

type publishedEvent struct {
	subject string
	tenant  uuid.UUID
	status  domain.SubscriptionStatus
	start   time.Time
	usage   map[domain.Resource]int64
}

// fakeDB guarda en memoria lo mismo que el registro, con sus mismas unicidades.
type fakeDB struct {
	plans     map[uuid.UUID]*domain.Plan
	subs      map[uuid.UUID]*domain.Subscription
	counters  map[counterKey]*domain.Counter
	items     map[itemKey]bool
	processed map[string]string
	events    []publishedEvent
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		plans:     map[uuid.UUID]*domain.Plan{},
		subs:      map[uuid.UUID]*domain.Subscription{},
		counters:  map[counterKey]*domain.Counter{},
		items:     map[itemKey]bool{},
		processed: map[string]string{},
	}
}

func (db *fakeDB) quantity(tenant uuid.UUID, r domain.Resource, period time.Time) int64 {
	if c, ok := db.counters[counterKey{tenant, r, period.Format(dayLayout)}]; ok {
		return c.Quantity
	}
	return 0
}

func (db *fakeDB) eventsOf(subject string) []publishedEvent {
	var out []publishedEvent
	for _, e := range db.events {
		if e.subject == subject {
			out = append(out, e)
		}
	}
	return out
}

func copyPlan(p *domain.Plan) *domain.Plan {
	c := *p
	c.Limits = append([]domain.PlanLimit(nil), p.Limits...)
	return &c
}

func copySub(s *domain.Subscription) *domain.Subscription {
	c := *s
	return &c
}

type fakePlans struct{ db *fakeDB }

func (f fakePlans) Create(_ context.Context, p *domain.Plan) error {
	for _, e := range f.db.plans {
		if e.Code == p.Code {
			return domain.ErrPlanCodeTaken
		}
	}
	p.ID = uuid.New()
	f.db.plans[p.ID] = copyPlan(p)
	return nil
}

func (f fakePlans) Get(_ context.Context, id uuid.UUID) (*domain.Plan, error) {
	p, ok := f.db.plans[id]
	if !ok {
		return nil, domain.ErrPlanNotFound
	}
	return copyPlan(p), nil
}

func (f fakePlans) GetForUpdate(ctx context.Context, id uuid.UUID) (*domain.Plan, error) {
	return f.Get(ctx, id)
}

func (f fakePlans) GetByCode(_ context.Context, code string) (*domain.Plan, error) {
	for _, p := range f.db.plans {
		if p.Code == code {
			return copyPlan(p), nil
		}
	}
	return nil, domain.ErrPlanNotFound
}

func (f fakePlans) List(_ context.Context, status domain.PlanStatus) ([]domain.Plan, error) {
	var out []domain.Plan
	for _, p := range f.db.plans {
		if status == "" || p.Status == status {
			out = append(out, *copyPlan(p))
		}
	}
	return out, nil
}

func (f fakePlans) Update(_ context.Context, p *domain.Plan, _ bool) error {
	if _, ok := f.db.plans[p.ID]; !ok {
		return domain.ErrPlanNotFound
	}
	f.db.plans[p.ID] = copyPlan(p)
	return nil
}

func (f fakePlans) HasSubscriptions(_ context.Context, id uuid.UUID) (bool, error) {
	for _, s := range f.db.subs {
		if s.PlanID == id {
			return true, nil
		}
	}
	return false, nil
}

type fakeSubs struct{ db *fakeDB }

func (f fakeSubs) Create(_ context.Context, s *domain.Subscription) (bool, error) {
	if _, ok := f.db.subs[s.TenantID]; ok {
		return false, nil
	}
	s.ID = uuid.New()
	f.db.subs[s.TenantID] = copySub(s)
	return true, nil
}

func (f fakeSubs) GetByTenant(_ context.Context, tenantID uuid.UUID) (*domain.Subscription, error) {
	s, ok := f.db.subs[tenantID]
	if !ok {
		return nil, domain.ErrSubscriptionNotFound
	}
	return copySub(s), nil
}

func (f fakeSubs) GetByTenantForUpdate(ctx context.Context, tenantID uuid.UUID) (*domain.Subscription, error) {
	return f.GetByTenant(ctx, tenantID)
}

func (f fakeSubs) Update(_ context.Context, s *domain.Subscription) error {
	if _, ok := f.db.subs[s.TenantID]; !ok {
		return domain.ErrSubscriptionNotFound
	}
	f.db.subs[s.TenantID] = copySub(s)
	return nil
}

func (f fakeSubs) List(_ context.Context, fl ports.SubscriptionFilter) ([]domain.Subscription, int64, error) {
	var out []domain.Subscription
	for _, s := range f.db.subs {
		if fl.Status == "" || s.Status == fl.Status {
			out = append(out, *copySub(s))
		}
	}
	return out, int64(len(out)), nil
}

func (f fakeSubs) ListDue(_ context.Context, now time.Time, exclude []uuid.UUID, limit int) ([]uuid.UUID, error) {
	skip := map[uuid.UUID]bool{}
	for _, id := range exclude {
		skip[id] = true
	}
	var out []uuid.UUID
	for tenant, s := range f.db.subs {
		if skip[tenant] || len(out) >= limit {
			continue
		}
		live := s.Status != domain.StatusCancelled
		due := (live && !s.CurrentPeriodEnd.After(domain.Date(now))) ||
			(s.Status == domain.StatusTrialing && s.TrialEndsAt != nil && !s.TrialEndsAt.After(now)) ||
			(live && s.CancelAt != nil && !s.CancelAt.After(now))
		if due {
			out = append(out, tenant)
		}
	}
	return out, nil
}

type fakeUsage struct{ db *fakeDB }

func (f fakeUsage) LockCounter(_ context.Context, tenantID uuid.UUID, r domain.Resource, period time.Time) (*domain.Counter, error) {
	k := counterKey{tenantID, r, domain.Date(period).Format(dayLayout)}
	c, ok := f.db.counters[k]
	if !ok {
		c = &domain.Counter{TenantID: tenantID, Resource: r, PeriodStart: domain.Date(period)}
		f.db.counters[k] = c
	}
	cp := *c
	return &cp, nil
}

func (f fakeUsage) SaveCounter(_ context.Context, c *domain.Counter) error {
	cp := *c
	f.db.counters[counterKey{c.TenantID, c.Resource, c.PeriodStart.Format(dayLayout)}] = &cp
	return nil
}

func (f fakeUsage) Quantity(_ context.Context, tenantID uuid.UUID, r domain.Resource, period time.Time) (int64, error) {
	return f.db.quantity(tenantID, r, period), nil
}

func (f fakeUsage) Quantities(_ context.Context, tenantID uuid.UUID, flowStart time.Time) (map[domain.Resource]int64, error) {
	out := map[domain.Resource]int64{}
	for _, r := range domain.Resources() {
		if k := (counterKey{tenantID, r, r.CounterPeriod(flowStart).Format(dayLayout)}); f.db.counters[k] != nil {
			out[r] = f.db.counters[k].Quantity
		}
	}
	return out, nil
}

func (f fakeUsage) others(k itemKey) bool {
	for other := range f.db.items {
		if other.tenant == k.tenant && other.resource == k.resource && other.key == k.key && other.source != k.source {
			return true
		}
	}
	return false
}

func (f fakeUsage) AddStockItem(_ context.Context, tenantID uuid.UUID, r domain.Resource, key, source string) (bool, error) {
	k := itemKey{tenantID, r, key, source}
	if f.db.items[k] {
		return false, nil
	}
	f.db.items[k] = true
	return !f.others(k), nil
}

func (f fakeUsage) RemoveStockItem(_ context.Context, tenantID uuid.UUID, r domain.Resource, key, source string) (bool, error) {
	k := itemKey{tenantID, r, key, source}
	if !f.db.items[k] {
		return false, nil
	}
	delete(f.db.items, k)
	return !f.others(k), nil
}

type fakeLedger struct{ db *fakeDB }

func (f fakeLedger) MarkProcessed(_ context.Context, eventID, subject string) (bool, error) {
	if _, ok := f.db.processed[eventID]; ok {
		return false, nil
	}
	f.db.processed[eventID] = subject
	return true, nil
}

func (f fakeLedger) PruneProcessed(context.Context, time.Time) (int64, error) { return 0, nil }

type fakeTx struct{}

func (fakeTx) Transact(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type fakeEvents struct{ db *fakeDB }

func (f fakeEvents) add(e publishedEvent) error {
	f.db.events = append(f.db.events, e)
	return nil
}

func (f fakeEvents) SubscriptionCreated(_ context.Context, s *domain.Subscription) error {
	return f.add(publishedEvent{subject: "billing.subscription.created", tenant: s.TenantID, status: s.Status})
}

func (f fakeEvents) SubscriptionChanged(_ context.Context, s *domain.Subscription, _ domain.SubscriptionStatus, _ string) error {
	return f.add(publishedEvent{subject: "billing.subscription.changed", tenant: s.TenantID, status: s.Status})
}

func (f fakeEvents) SubscriptionSuspended(_ context.Context, s *domain.Subscription, _ domain.SubscriptionStatus) error {
	return f.add(publishedEvent{subject: "billing.subscription.suspended", tenant: s.TenantID, status: s.Status})
}

func (f fakeEvents) PeriodClosed(_ context.Context, s *domain.Subscription, start, _ time.Time, usage map[domain.Resource]int64) error {
	return f.add(publishedEvent{subject: "billing.period.closed", tenant: s.TenantID, start: start, usage: usage})
}

func (f fakeEvents) LimitReached(_ context.Context, tenantID uuid.UUID, _ domain.PlanLimit, periodStart time.Time, _ int64) error {
	return f.add(publishedEvent{subject: "billing.limit.reached", tenant: tenantID, start: periodStart})
}

// newTestUseCase arma el caso de uso sobre los fakes con un reloj que el test mueve.
func newTestUseCase(now time.Time, cfg Config) (*UseCase, *fakeDB, *time.Time) {
	db := newFakeDB()
	clock := now
	uc := New(Deps{
		Plans: fakePlans{db}, Subscriptions: fakeSubs{db}, Usage: fakeUsage{db}, Ledger: fakeLedger{db},
		Tx: fakeTx{}, Events: fakeEvents{db}, Config: cfg, Now: func() time.Time { return clock },
	})
	return uc, db, &clock
}
