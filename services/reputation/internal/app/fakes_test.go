package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/reputation/internal/domain"
	"github.com/alonsosss/corforce-email/services/reputation/internal/ports"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

var errBoom = errors.New("dependencia caida")

func key(tenantID uuid.UUID, class domain.Class) string {
	return tenantID.String() + ":" + string(class)
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func ptr(v int64) *int64 { return &v }

// fakeTx ejecuta la funcion sin transaccion: los fakes no revierten, igual que una
// transaccion que ya confirmo.
type fakeTx struct{}

func (fakeTx) Transact(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type fakeStats struct {
	processed map[string]bool
	daily     map[string]map[string]domain.Counts
}

func newFakeStats() *fakeStats {
	return &fakeStats{processed: map[string]bool{}, daily: map[string]map[string]domain.Counts{}}
}

func (f *fakeStats) MarkProcessed(_ context.Context, _ uuid.UUID, eventID string) (bool, error) {
	if f.processed[eventID] {
		return false, nil
	}
	f.processed[eventID] = true
	return true, nil
}

func (f *fakeStats) AddDaily(_ context.Context, tenantID uuid.UUID, class domain.Class, day time.Time, d domain.Counts) error {
	k := key(tenantID, class)
	if f.daily[k] == nil {
		f.daily[k] = map[string]domain.Counts{}
	}
	dk := day.UTC().Format(time.DateOnly)
	c := f.daily[k][dk]
	c.Sent += d.Sent
	c.Bounced += d.Bounced
	c.Complained += d.Complained
	f.daily[k][dk] = c
	return nil
}

func (f *fakeStats) WindowCounts(_ context.Context, tenantID uuid.UUID, class domain.Class, from time.Time) (domain.Counts, error) {
	var out domain.Counts
	fromKey := from.UTC().Format(time.DateOnly)
	for dk, c := range f.daily[key(tenantID, class)] {
		if dk >= fromKey {
			out.Sent += c.Sent
			out.Bounced += c.Bounced
			out.Complained += c.Complained
		}
	}
	return out, nil
}

func (f *fakeStats) WindowCountsByClass(ctx context.Context, tenantID uuid.UUID, from time.Time) (map[domain.Class]domain.Counts, error) {
	out := map[domain.Class]domain.Counts{}
	for _, c := range domain.Classes() {
		counts, _ := f.WindowCounts(ctx, tenantID, c, from)
		if counts != (domain.Counts{}) {
			out[c] = counts
		}
	}
	return out, nil
}

func (f *fakeStats) Prune(context.Context, time.Time, time.Time) (int64, int64, error) {
	return 0, 0, nil
}

// day devuelve lo contado para la clase en un dia concreto.
func (f *fakeStats) day(tenantID uuid.UUID, class domain.Class, day time.Time) domain.Counts {
	return f.daily[key(tenantID, class)][day.UTC().Format(time.DateOnly)]
}

type fakeStates struct {
	records map[string]domain.Record
	history []domain.Change
	// failForUpdate hace fallar las proximas N llamadas a GetForUpdate.
	failForUpdate int
}

func newFakeStates() *fakeStates { return &fakeStates{records: map[string]domain.Record{}} }

func (f *fakeStates) Ensure(_ context.Context, tenantID uuid.UUID, class domain.Class, now time.Time) error {
	k := key(tenantID, class)
	if _, ok := f.records[k]; !ok {
		r := domain.DefaultRecord(tenantID, class)
		r.ChangedAt = now
		f.records[k] = r
	}
	return nil
}

func (f *fakeStates) GetForUpdate(_ context.Context, tenantID uuid.UUID, class domain.Class) (domain.Record, error) {
	if f.failForUpdate > 0 {
		f.failForUpdate--
		return domain.Record{}, errBoom
	}
	r, ok := f.records[key(tenantID, class)]
	if !ok {
		return domain.Record{}, errors.New("sin fila de estado")
	}
	return r, nil
}

func (f *fakeStates) Get(_ context.Context, tenantID uuid.UUID, class domain.Class) (domain.Record, error) {
	if r, ok := f.records[key(tenantID, class)]; ok {
		return r, nil
	}
	return domain.DefaultRecord(tenantID, class), nil
}

func (f *fakeStates) List(_ context.Context, tenantID uuid.UUID) ([]domain.Record, error) {
	var out []domain.Record
	for _, c := range domain.Classes() {
		if r, ok := f.records[key(tenantID, c)]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStates) Save(_ context.Context, r domain.Record) error {
	k := key(r.TenantID, r.Class)
	if _, ok := f.records[k]; !ok {
		return errors.New("sin fila de estado")
	}
	f.records[k] = r
	return nil
}

func (f *fakeStates) AppendHistory(_ context.Context, c *domain.Change) error {
	c.ID = uuid.New()
	f.history = append(f.history, *c)
	return nil
}

func (f *fakeStates) ListHistory(_ context.Context, tenantID uuid.UUID, fl ports.HistoryFilter) ([]domain.Change, int64, error) {
	var out []domain.Change
	for i := len(f.history) - 1; i >= 0; i-- {
		c := f.history[i]
		if c.TenantID != tenantID || (fl.Class != "" && c.Class != fl.Class) {
			continue
		}
		out = append(out, c)
	}
	return out, int64(len(out)), nil
}

func (f *fakeStates) set(r domain.Record) { f.records[key(r.TenantID, r.Class)] = r }

func (f *fakeStates) get(tenantID uuid.UUID, class domain.Class) domain.Record {
	return f.records[key(tenantID, class)]
}

type fakeLimits struct {
	overrides map[string]domain.LimitOverride
}

func (f *fakeLimits) Get(_ context.Context, tenantID uuid.UUID, class domain.Class) (*domain.LimitOverride, error) {
	if o, ok := f.overrides[key(tenantID, class)]; ok {
		return &o, nil
	}
	return nil, nil
}

func (f *fakeLimits) List(_ context.Context, tenantID uuid.UUID) ([]domain.LimitOverride, error) {
	var out []domain.LimitOverride
	for _, c := range domain.Classes() {
		if o, ok := f.overrides[key(tenantID, c)]; ok {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeLimits) Upsert(_ context.Context, o *domain.LimitOverride) error {
	o.UpdatedAt = time.Now()
	f.overrides[key(o.TenantID, o.Class)] = *o
	return nil
}

func (f *fakeLimits) Delete(_ context.Context, tenantID uuid.UUID, class domain.Class) error {
	delete(f.overrides, key(tenantID, class))
	return nil
}

type fakeEvents struct{ changes []domain.Change }

func (f *fakeEvents) StateChanged(_ context.Context, c domain.Change) error {
	f.changes = append(f.changes, c)
	return nil
}

// fakeRate reproduce la regla del script de Redis: comprueba dia y hora y solo si caben
// suma a las dos.
type fakeRate struct {
	used  map[string][2]int64
	err   error
	calls int
}

func (f *fakeRate) Reserve(_ context.Context, tenantID uuid.UUID, class domain.Class, _ time.Time, count int64, l domain.Limits) (domain.RateOutcome, error) {
	f.calls++
	if f.err != nil {
		return domain.RateOutcome{}, f.err
	}
	k := key(tenantID, class)
	u := f.used[k]
	switch {
	case u[1]+count > l.Daily:
		return domain.RateOutcome{HourUsed: u[0], DayUsed: u[1], Exceeded: domain.WindowDay}, nil
	case u[0]+count > l.Hourly:
		return domain.RateOutcome{HourUsed: u[0], DayUsed: u[1], Exceeded: domain.WindowHour}, nil
	}
	u[0] += count
	u[1] += count
	f.used[k] = u
	return domain.RateOutcome{Allowed: true, HourUsed: u[0], DayUsed: u[1]}, nil
}

func (f *fakeRate) Usage(_ context.Context, tenantID uuid.UUID, class domain.Class, _ time.Time) (int64, int64, error) {
	if f.err != nil {
		return 0, 0, f.err
	}
	u := f.used[key(tenantID, class)]
	return u[0], u[1], nil
}

type fakeBilling struct {
	ent   domain.Entitlement
	err   error
	calls int
}

func (f *fakeBilling) Check(_ context.Context, _ uuid.UUID, _ domain.Class, quantity int64) (domain.Entitlement, error) {
	f.calls++
	if f.err != nil {
		return domain.Entitlement{}, f.err
	}
	e := f.ent
	e.Requested = quantity
	return e, nil
}

type fakeTenants struct{ active []uuid.UUID }

func (f *fakeTenants) Scope(ctx context.Context, tenantID uuid.UUID) (context.Context, error) {
	for _, id := range f.active {
		if id == tenantID {
			return ctx, nil
		}
	}
	return nil, domain.ErrTenantNotFound
}

func (f *fakeTenants) ForEachActive(ctx context.Context, _ time.Duration, fn func(context.Context, uuid.UUID)) error {
	for _, id := range f.active {
		fn(ctx, id)
	}
	return nil
}

type fakeMetrics struct {
	authorize map[string]int
	changes   map[string]int
	degraded  map[string]int
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{authorize: map[string]int{}, changes: map[string]int{}, degraded: map[string]int{}}
}

func (f *fakeMetrics) Authorize(class domain.Class, result string) {
	f.authorize[string(class)+"/"+result]++
}

func (f *fakeMetrics) StateChanged(class domain.Class, to domain.State) {
	f.changes[string(class)+"/"+string(to)]++
}

func (f *fakeMetrics) Degraded(dependency string) { f.degraded[dependency]++ }

// testPolicy: limites pequenos para poder agotarlos en las pruebas.
func testPolicy() domain.Policy {
	return domain.Policy{
		Thresholds: domain.Thresholds{
			BounceWarn: dec("0.02"), BounceBlock: dec("0.04"),
			ComplaintWarn: dec("0.0005"), ComplaintBlock: dec("0.0008"),
			MinVolume: 200,
		},
		WindowDays: 7,
		Defaults: map[domain.Class]domain.Limits{
			domain.ClassTransactional: {Hourly: 100, Daily: 1000},
			domain.ClassMarketing:     {Hourly: 500, Daily: 5000},
		},
	}
}

type harness struct {
	uc      *UseCase
	stats   *fakeStats
	states  *fakeStates
	limits  *fakeLimits
	events  *fakeEvents
	rate    *fakeRate
	billing *fakeBilling
	tenants *fakeTenants
	metrics *fakeMetrics
	now     time.Time
}

func newHarness() *harness {
	h := &harness{
		stats:   newFakeStats(),
		states:  newFakeStates(),
		limits:  &fakeLimits{overrides: map[string]domain.LimitOverride{}},
		events:  &fakeEvents{},
		rate:    &fakeRate{used: map[string][2]int64{}},
		billing: &fakeBilling{ent: domain.Entitlement{Allowed: true, HardLimit: true, Limit: ptr(100000), Remaining: ptr(100000)}},
		tenants: &fakeTenants{},
		metrics: newFakeMetrics(),
		now:     time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC),
	}
	h.uc = New(Deps{
		Stats: h.stats, States: h.states, Limits: h.limits, Tx: fakeTx{}, Events: h.events,
		Rate: h.rate, Billing: h.billing, Tenants: h.tenants, Metrics: h.metrics,
		Policy: testPolicy(), Logger: zap.NewNop(),
		Now: func() time.Time { return h.now },
	})
	return h
}

func (h *harness) today() time.Time { return dayOf(h.now) }
