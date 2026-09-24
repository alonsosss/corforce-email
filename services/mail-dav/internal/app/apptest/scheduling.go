package apptest

import (
	"context"
	"sort"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-dav/internal/domain"
	"github.com/google/uuid"
)

// Scheduling es la planificacion en memoria sobre un Store (ports.SchedulingStore), con las mismas reglas que las
// funciones de la migracion 05_scheduling.sql: solo la empresa de quien pregunta, ocupacion sin contenido y el
// buzon dueno de una pagina solo si esta activa.
type Scheduling struct {
	store     *Store
	addresses map[uuid.UUID]map[string]uuid.UUID
	pages     map[owner]domain.BookingPage
	bookings  map[uuid.UUID][]bookingRow
	// Reserved cuenta las reservas guardadas.
	Reserved int
}

type bookingRow struct {
	visitor string
	at      time.Time
}

func NewScheduling(store *Store) *Scheduling {
	return &Scheduling{store: store, addresses: map[uuid.UUID]map[string]uuid.UUID{}, pages: map[owner]domain.BookingPage{}, bookings: map[uuid.UUID][]bookingRow{}}
}

// mailboxEvents recorre los eventos de todos los calendarios del buzon.
func (s *Store) mailboxEventsOf(tenant, mailbox uuid.UUID, fn func(slug string, e *domain.Event)) {
	for slug, b := range s.calendars[owner{tenant, mailbox}] {
		for name, e := range s.events[b.ID] {
			fn(slug, &e)
			s.events[b.ID][name] = e
		}
	}
}

func (g *Scheduling) ReplaceBusy(_ context.Context, p domain.Principal, eventID uuid.UUID, etag string, busy []domain.Interval, until *time.Time) (bool, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	replaced := false
	g.store.mailboxEventsOf(p.TenantID, p.MailboxID, func(_ string, e *domain.Event) {
		if e.ID == eventID && e.ETag == etag {
			e.Busy, e.BusyUntil, e.BusyPlanned = busy, until, true
			replaced = true
		}
	})
	return replaced, nil
}

func (g *Scheduling) EventsByID(_ context.Context, p domain.Principal, ids []uuid.UUID) ([]domain.Event, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	want := map[uuid.UUID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := []domain.Event{}
	g.store.mailboxEventsOf(p.TenantID, p.MailboxID, func(_ string, e *domain.Event) {
		if want[e.ID] {
			out = append(out, *e)
		}
	})
	return out, nil
}

func (g *Scheduling) EventByUID(_ context.Context, p domain.Principal, slug, uid string) (domain.Event, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	b, err := g.store.calendars.find(p, slug)
	if err != nil {
		return domain.Event{}, err
	}
	for _, e := range g.store.events[b.ID] {
		if e.UID == uid {
			return e, nil
		}
	}
	return domain.Event{}, domain.ErrNotFound
}

func (g *Scheduling) RegisterAddress(_ context.Context, p domain.Principal, address string) error {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	m := g.addresses[p.TenantID]
	if m == nil {
		m = map[string]uuid.UUID{}
		g.addresses[p.TenantID] = m
	}
	for a, id := range m {
		if id == p.MailboxID {
			delete(m, a)
		}
	}
	m[address] = p.MailboxID
	return nil
}

func (g *Scheduling) ResolveMailboxes(_ context.Context, p domain.Principal, addresses []string) (map[string]uuid.UUID, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	out := map[string]uuid.UUID{}
	for _, a := range addresses {
		if id, ok := g.addresses[p.TenantID][a]; ok {
			out[a] = id
		}
	}
	return out, nil
}

func (g *Scheduling) BusyIntervals(_ context.Context, p domain.Principal, mailboxes []uuid.UUID, from, to time.Time) (map[uuid.UUID][]domain.Interval, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	out := map[uuid.UUID][]domain.Interval{}
	for _, mb := range mailboxes {
		g.store.mailboxEventsOf(p.TenantID, mb, func(_ string, e *domain.Event) {
			for _, iv := range e.Busy {
				if iv.Start.Before(to) && iv.End.After(from) {
					out[mb] = append(out[mb], iv)
				}
			}
		})
		sort.Slice(out[mb], func(i, j int) bool { return out[mb][i].Start.Before(out[mb][j].Start) })
	}
	return out, nil
}

func (g *Scheduling) PendingBusy(_ context.Context, p domain.Principal, mailboxes []uuid.UUID, until time.Time, limit int) (map[uuid.UUID][]uuid.UUID, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	out := map[uuid.UUID][]uuid.UUID{}
	n := 0
	for _, mb := range mailboxes {
		g.store.mailboxEventsOf(p.TenantID, mb, func(_ string, e *domain.Event) {
			if n < limit && (!e.BusyPlanned || (e.BusyUntil != nil && e.BusyUntil.Before(until))) {
				out[mb] = append(out[mb], e.ID)
				n++
			}
		})
	}
	return out, nil
}

func (g *Scheduling) BookingPage(_ context.Context, p domain.Principal) (domain.BookingPage, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	page, ok := g.pages[key(p)]
	if !ok {
		return domain.BookingPage{}, domain.ErrNotFound
	}
	return page, nil
}

func (g *Scheduling) SaveBookingPage(_ context.Context, p domain.Principal, page domain.BookingPage) (domain.BookingPage, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	page.TenantID, page.MailboxID, page.UpdatedAt = p.TenantID, p.MailboxID, g.store.Now()
	g.pages[key(p)] = page
	return page, nil
}

func (g *Scheduling) BookingPageOwner(_ context.Context, p domain.Principal, publicID string) (uuid.UUID, error) {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	for o, page := range g.pages {
		if o.tenant == p.TenantID && page.PublicID == publicID && page.Active {
			return o.mailbox, nil
		}
	}
	return uuid.Nil, nil
}

func (g *Scheduling) ReserveBooking(ctx context.Context, p domain.Principal, slug string, e domain.Event, b domain.Booking, lim domain.BookingLimits) error {
	g.store.mu.Lock()
	since := g.store.Now().Add(-24 * time.Hour)
	total, visitor := 0, 0
	for _, row := range g.bookings[b.PageID] {
		if row.at.After(since) {
			total++
			if row.visitor == b.VisitorHash {
				visitor++
			}
		}
	}
	if total >= lim.Daily || visitor >= lim.PerVisitor {
		g.store.mu.Unlock()
		return domain.ErrBookingLimit
	}
	taken := false
	g.store.mailboxEventsOf(p.TenantID, p.MailboxID, func(_ string, ev *domain.Event) {
		for _, iv := range ev.Busy {
			if iv.Start.Before(b.End.Add(lim.Buffer)) && iv.End.After(b.Start.Add(-lim.Buffer)) {
				taken = true
			}
		}
	})
	g.store.mu.Unlock()
	if taken {
		return domain.ErrSlotUnavailable
	}
	if _, err := g.store.PutEvent(ctx, p, slug, e, domain.Precondition{IfNoneMatchAny: true}, lim.Write); err != nil {
		return err
	}
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	g.bookings[b.PageID] = append(g.bookings[b.PageID], bookingRow{visitor: b.VisitorHash, at: g.store.Now()})
	g.Reserved++
	return nil
}

func (g *Scheduling) DeleteMailboxScheduling(_ context.Context, p domain.Principal) error {
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	delete(g.pages, key(p))
	for a, id := range g.addresses[p.TenantID] {
		if id == p.MailboxID {
			delete(g.addresses[p.TenantID], a)
		}
	}
	return nil
}
