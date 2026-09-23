package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

type classKey struct {
	tenant uuid.UUID
	day    time.Time
	class  domain.Class
}

type campaignKey struct {
	tenant   uuid.UUID
	day      time.Time
	campaign uuid.UUID
}

type domainKey struct {
	tenant uuid.UUID
	day    time.Time
	class  domain.Class
	name   string
}

// fakeStore guarda en memoria lo mismo que las tablas de analytics y aplica cada
// transaccion con todo o nada, como la base.
type fakeStore struct {
	processed map[uuid.UUID]bool
	facts     map[uuid.UUID]domain.MessageFact
	class     map[classKey]domain.Counters
	campaign  map[campaignKey]domain.Counters
	domains   map[domainKey]domain.Counters
	campaigns map[uuid.UUID]domain.CampaignSeen
	links     map[linkKey]domain.LinkStats
	clicks    map[clickKey]bool

	failApply    error
	clicksBefore time.Time

	factsBefore, eventsBefore time.Time

	summaries   []domain.CampaignSummary
	listCalls   int
	seriesRange domain.Range
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		processed: map[uuid.UUID]bool{},
		facts:     map[uuid.UUID]domain.MessageFact{},
		class:     map[classKey]domain.Counters{},
		campaign:  map[campaignKey]domain.Counters{},
		domains:   map[domainKey]domain.Counters{},
		campaigns: map[uuid.UUID]domain.CampaignSeen{},
		links:     map[linkKey]domain.LinkStats{},
		clicks:    map[clickKey]bool{},
	}
}

type linkKey struct {
	campaign uuid.UUID
	url      string
}

type clickKey struct {
	message uuid.UUID
	url     string
}

func (s *fakeStore) RecordClick(_ context.Context, c domain.LinkClick) error {
	url := c.URL
	if _, ok := s.links[linkKey{c.CampaignID, url}]; !ok {
		distinct := 0
		for k := range s.links {
			if k.campaign == c.CampaignID && k.url != domain.OtherLinks {
				distinct++
			}
		}
		if distinct >= domain.MaxLinksPerCampaign {
			url = domain.OtherLinks
		}
	}
	k := linkKey{c.CampaignID, url}
	st := s.links[k]
	if st.Clicks == 0 {
		st = domain.LinkStats{URL: url, FirstClickedAt: c.At, LastClickedAt: c.At}
	}
	st.Clicks++
	if !s.clicks[clickKey{c.MessageID, url}] {
		s.clicks[clickKey{c.MessageID, url}] = true
		st.UniqueClicks++
	}
	if c.At.After(st.LastClickedAt) {
		st.LastClickedAt = c.At
	}
	if c.At.Before(st.FirstClickedAt) {
		st.FirstClickedAt = c.At
	}
	s.links[k] = st
	return nil
}

func (s *fakeStore) PruneClicks(_ context.Context, _ uuid.UUID, before time.Time) (int64, error) {
	s.clicksBefore = before
	return 0, nil
}

func (s *fakeStore) CampaignLinks(_ context.Context, _, campaignID uuid.UUID, limit int) (*domain.CampaignLinks, error) {
	out := &domain.CampaignLinks{CampaignID: campaignID, Links: []domain.LinkStats{}}
	for k, st := range s.links {
		if k.campaign != campaignID {
			continue
		}
		if k.url != domain.OtherLinks {
			out.TotalLinks++
		}
		out.TotalClicks += st.Clicks
		out.Links = append(out.Links, st)
	}
	if len(out.Links) > limit {
		out.Links = out.Links[:limit]
	}
	return out, nil
}

func newTestUseCase(s *fakeStore, now time.Time) *UseCase {
	return New(Deps{
		Tx: s, Ledger: s, Facts: s, Stats: s, Campaigns: campaignRepo{s}, Links: s, Reports: s,
		MessageRetention: 90 * 24 * time.Hour,
		Now:              func() time.Time { return now },
	})
}

func copyMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *fakeStore) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	processed, facts, class := copyMap(s.processed), copyMap(s.facts), copyMap(s.class)
	campaign, domains, campaigns := copyMap(s.campaign), copyMap(s.domains), copyMap(s.campaigns)
	links, clicks := copyMap(s.links), copyMap(s.clicks)
	if err := fn(ctx); err != nil {
		s.processed, s.facts, s.class = processed, facts, class
		s.campaign, s.domains, s.campaigns = campaign, domains, campaigns
		s.links, s.clicks = links, clicks
		return err
	}
	return nil
}

func (s *fakeStore) MarkProcessed(_ context.Context, _, eventID uuid.UUID) (bool, error) {
	if s.processed[eventID] {
		return false, nil
	}
	s.processed[eventID] = true
	return true, nil
}

func (s *fakeStore) PruneProcessed(_ context.Context, _ uuid.UUID, before time.Time) (int64, error) {
	s.eventsBefore = before
	return 0, nil
}

func (s *fakeStore) LockOrCreate(_ context.Context, seed domain.MessageFact) (*domain.MessageFact, error) {
	f, ok := s.facts[seed.MessageID]
	if !ok {
		f = seed
		s.facts[seed.MessageID] = f
	}
	if f.TenantID != seed.TenantID {
		return nil, fmt.Errorf("%w: otra empresa", domain.ErrInvalidEvent)
	}
	return &f, nil
}

func (s *fakeStore) Save(_ context.Context, f *domain.MessageFact) error {
	s.facts[f.MessageID] = *f
	return nil
}

func (s *fakeStore) PruneInactive(_ context.Context, _ uuid.UUID, before time.Time) (int64, error) {
	s.factsBefore = before
	return 0, nil
}

// Apply se comporta como las dos sentencias reales: suma creando la fila, o corrige una
// fila que debe existir, y nunca deja un contador negativo (el CHECK de la tabla).
func (s *fakeStore) Apply(_ context.Context, tenantID uuid.UUID, dims domain.Dimensions, day domain.DayCounters) error {
	if s.failApply != nil {
		return s.failApply
	}
	add := func(current domain.Counters, exists bool) (domain.Counters, error) {
		if !exists && !day.Counters.NonNegative() {
			return current, errors.New("descuento sobre un agregado inexistente")
		}
		for i, v := range day.Counters.Values() {
			current.Add(domain.Counter(i), v)
		}
		if !current.NonNegative() {
			return current, errors.New("contador negativo")
		}
		return current, nil
	}
	ck := classKey{tenantID, day.Day, dims.Class}
	c, ok := s.class[ck]
	next, err := add(c, ok)
	if err != nil {
		return err
	}
	s.class[ck] = next
	if dims.CampaignID != nil {
		k := campaignKey{tenantID, day.Day, *dims.CampaignID}
		c, ok := s.campaign[k]
		if next, err = add(c, ok); err != nil {
			return err
		}
		s.campaign[k] = next
	}
	if dims.RecipientDomain != "" {
		k := domainKey{tenantID, day.Day, dims.Class, dims.RecipientDomain}
		c, ok := s.domains[k]
		if next, err = add(c, ok); err != nil {
			return err
		}
		s.domains[k] = next
	}
	return nil
}

func (s *fakeStore) CreateIfAbsent(_ context.Context, c domain.CampaignSeen) (bool, error) {
	if _, ok := s.campaigns[c.CampaignID]; ok {
		return false, nil
	}
	s.campaigns[c.CampaignID] = c
	return true, nil
}

func (s *fakeStore) Lock(_ context.Context, _, campaignID uuid.UUID) (*domain.CampaignSeen, error) {
	c := s.campaigns[campaignID]
	return &c, nil
}

func (s *fakeStore) SaveCampaign(c *domain.CampaignSeen) { s.campaigns[c.CampaignID] = *c }

// campaignRepo adapta el guardado de campanas, que en ports se llama igual que el de
// mensajes.
type campaignRepo struct{ *fakeStore }

func (r campaignRepo) Save(_ context.Context, c *domain.CampaignSeen) error {
	r.SaveCampaign(c)
	return nil
}

func (s *fakeStore) Totals(_ context.Context, tenantID uuid.UUID, q domain.ClassQuery) (domain.Counters, error) {
	var out domain.Counters
	for k, c := range s.class {
		if k.tenant != tenantID || k.day.Before(q.Range.From) || k.day.After(q.Range.To) || (q.Class != "" && k.class != q.Class) {
			continue
		}
		for i, v := range c.Values() {
			out.Add(domain.Counter(i), v)
		}
	}
	return out, nil
}

func (s *fakeStore) Series(context.Context, uuid.UUID, domain.ClassQuery) ([]domain.DayCounters, error) {
	return nil, nil
}

func (s *fakeStore) CountCampaigns(context.Context, uuid.UUID) (int64, error) {
	return int64(len(s.summaries)), nil
}

func (s *fakeStore) ListCampaigns(_ context.Context, _ uuid.UUID, limit, offset int) ([]domain.CampaignSummary, error) {
	s.listCalls++
	end := offset + limit
	if end > len(s.summaries) {
		end = len(s.summaries)
	}
	return s.summaries[offset:end], nil
}

func (s *fakeStore) GetCampaign(_ context.Context, _, campaignID uuid.UUID) (*domain.CampaignSummary, error) {
	for _, c := range s.summaries {
		if c.CampaignID == campaignID {
			return &c, nil
		}
	}
	return nil, domain.ErrCampaignNotFound
}

func (s *fakeStore) CampaignSeries(_ context.Context, _, _ uuid.UUID, r domain.Range) ([]domain.DayCounters, error) {
	s.seriesRange = r
	return nil, nil
}

func (s *fakeStore) TopDomains(context.Context, uuid.UUID, domain.ClassQuery, int) ([]domain.DomainStats, error) {
	return nil, nil
}
