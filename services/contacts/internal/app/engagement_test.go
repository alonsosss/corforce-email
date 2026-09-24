package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
)

type engagementKey struct{ contact, campaign uuid.UUID }

// fakeEngagement aplica los toques con la misma regla que la base (recepcion mas antigua,
// apertura y clic mas recientes) sobre los contactos del almacen falso.
type fakeEngagement struct {
	s      *fakeStore
	rows   map[engagementKey]domain.EngagementTouch
	writes int
	pruned []time.Time
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func latest(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil || !b.After(*a):
		return a
	}
	return b
}

func (f *fakeEngagement) Touch(_ context.Context, t domain.EngagementTouch) (bool, error) {
	c, ok := f.s.contacts[t.ContactID]
	if !ok || c.TenantID != t.TenantID {
		return false, nil
	}
	k := engagementKey{t.ContactID, t.CampaignID}
	cur, had := f.rows[k]
	if !had {
		f.rows[k] = t
		f.writes++
		return true, nil
	}
	next := cur
	next.ReceivedAt = earliest(cur.ReceivedAt, t.ReceivedAt)
	next.OpenedAt = latest(cur.OpenedAt, t.OpenedAt)
	next.ClickedAt = latest(cur.ClickedAt, t.ClickedAt)
	if next.ReceivedAt != cur.ReceivedAt || next.OpenedAt != cur.OpenedAt || next.ClickedAt != cur.ClickedAt {
		f.writes++
	}
	f.rows[k] = next
	return true, nil
}

func (f *fakeEngagement) Prune(_ context.Context, _ uuid.UUID, before time.Time, _ int) (int64, error) {
	f.pruned = append(f.pruned, before)
	return 0, nil
}

type fakeMatcher struct {
	matched    []uuid.UUID
	lastIDs    []uuid.UUID
	candidates []ports.AnniversaryCandidate
	lastQuery  ports.AnniversaryQuery
}

func (f *fakeMatcher) MatchAmong(_ context.Context, _ uuid.UUID, _ segment.Definition, _ segment.Schema, ids []uuid.UUID) ([]uuid.UUID, error) {
	f.lastIDs = ids
	return f.matched, nil
}

func (f *fakeMatcher) AnniversaryCandidates(_ context.Context, _ uuid.UUID, q ports.AnniversaryQuery) ([]ports.AnniversaryCandidate, error) {
	f.lastQuery = q
	return f.candidates, nil
}

func behaviorFixture(t *testing.T) (*fixture, *fakeEngagement, *fakeMatcher) {
	t.Helper()
	f := newFixture(t)
	eng := &fakeEngagement{s: f.s, rows: map[engagementKey]domain.EngagementTouch{}}
	m := &fakeMatcher{}
	f.uc = New(Deps{
		Contacts: fakeContacts{f.s}, Consents: fakeConsents{f.s}, Tokens: fakeTokens{f.s},
		Lists: fakeLists{f.s}, Attributes: fakeAttributes{f.s}, Segments: fakeSegments{f.s},
		Query: f.query, Imports: fakeImports{f.s}, Tx: fakeTx{}, Events: f.ev, Suppression: f.sup,
		Engagement: eng, Matcher: m,
		Config: Config{PublicBaseURL: "https://app.example.com", EngagementRetention: 30 * 24 * time.Hour},
		Now:    func() time.Time { return f.now },
	})
	return f, eng, m
}

// Reentregar los mismos hitos, o recibirlos desordenados, deja la misma proyeccion y no
// vuelve a escribir.
func TestInteraccionIdempotenteYSinOrden(t *testing.T) {
	f, eng, _ := behaviorFixture(t)
	c := f.addContact(t, "ana@example.com", domain.StatusActive, domain.ConsentGranted)
	campaign := uuid.New()
	ev := func(kind domain.EngagementKind, ago time.Duration) domain.EngagementEvent {
		return domain.EngagementEvent{TenantID: f.tenant, ContactID: c.ID, CampaignID: campaign, Kind: kind, OccurredAt: f.now.Add(-ago)}
	}
	sequence := []domain.EngagementEvent{
		ev(domain.EngagementClicked, time.Hour),
		ev(domain.EngagementDelivered, 3*time.Hour),
		ev(domain.EngagementOpened, 2*time.Hour),
		ev(domain.EngagementOpened, 30*time.Minute),
	}
	for _, e := range sequence {
		if ok, err := f.uc.RecordEngagement(context.Background(), e); err != nil || !ok {
			t.Fatalf("%v: %v %v", e.Kind, ok, err)
		}
	}
	row := eng.rows[engagementKey{c.ID, campaign}]
	if !row.ReceivedAt.Equal(f.now.Add(-3*time.Hour)) || !row.OpenedAt.Equal(f.now.Add(-30*time.Minute)) || !row.ClickedAt.Equal(f.now.Add(-time.Hour)) {
		t.Fatalf("proyeccion: %+v", row)
	}
	writes := eng.writes
	for _, e := range sequence {
		if _, err := f.uc.RecordEngagement(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if eng.writes != writes || eng.rows[engagementKey{c.ID, campaign}] != row {
		t.Fatalf("la reentrega cambio la proyeccion: %d escrituras, %+v", eng.writes-writes, eng.rows[engagementKey{c.ID, campaign}])
	}
}

func TestInteraccionFueraDeRetencionOContactoBorrado(t *testing.T) {
	f, eng, _ := behaviorFixture(t)
	c := f.addContact(t, "ana@example.com", domain.StatusActive, domain.ConsentGranted)
	old := domain.EngagementEvent{TenantID: f.tenant, ContactID: c.ID, CampaignID: uuid.New(), Kind: domain.EngagementOpened, OccurredAt: f.now.Add(-31 * 24 * time.Hour)}
	if ok, err := f.uc.RecordEngagement(context.Background(), old); err != nil || ok || len(eng.rows) != 0 {
		t.Fatalf("un hito anterior a la retencion no se guarda: %v %v", ok, err)
	}
	gone := domain.EngagementEvent{TenantID: f.tenant, ContactID: uuid.New(), CampaignID: uuid.New(), Kind: domain.EngagementClicked, OccurredAt: f.now}
	if ok, err := f.uc.RecordEngagement(context.Background(), gone); err != nil || ok {
		t.Fatalf("un contacto que no existe no deja rastro: %v %v", ok, err)
	}
	bad := domain.EngagementEvent{TenantID: f.tenant, ContactID: c.ID, Kind: domain.EngagementClicked}
	if _, err := f.uc.RecordEngagement(context.Background(), bad); !errors.Is(err, domain.ErrInvalidEngagement) {
		t.Fatalf("sin campana es definitivo: %v", err)
	}
}

func TestPodaDeInteraccionUsaLaRetencion(t *testing.T) {
	f, eng, _ := behaviorFixture(t)
	if _, err := f.uc.PruneEngagement(context.Background(), f.tenant); err != nil {
		t.Fatal(err)
	}
	if len(eng.pruned) != 1 || !eng.pruned[0].Equal(f.now.Add(-30*24*time.Hour)) {
		t.Fatalf("corte de la poda: %v", eng.pruned)
	}
}

func TestCoincidenciaConSegmentoODefinicion(t *testing.T) {
	f, _, m := behaviorFixture(t)
	f.declare(t, "plan", domain.AttrString, false)
	a, b := uuid.New(), uuid.New()
	m.matched = []uuid.UUID{a}
	def := json.RawMessage(`{"match":"all","rules":[{"field":"attributes.plan","op":"eq","value":"oro"}]}`)

	got, err := f.uc.MatchContacts(context.Background(), f.tenant, MatchInput{Definition: def, ContactIDs: []uuid.UUID{a, b, a}})
	if err != nil || len(got) != 1 || got[0] != a || len(m.lastIDs) != 2 {
		t.Fatalf("definicion: %v %v ids=%v", got, err, m.lastIDs)
	}
	seg, err := f.uc.CreateSegment(context.Background(), f.tenant, SegmentInput{Name: "oro", Definition: def})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.uc.MatchContacts(context.Background(), f.tenant, MatchInput{SegmentID: &seg.ID, ContactIDs: []uuid.UUID{a}}); err != nil || len(got) != 1 {
		t.Fatalf("segmento: %v %v", got, err)
	}
	m.lastIDs = nil
	if got, err := f.uc.MatchContacts(context.Background(), f.tenant, MatchInput{SegmentID: &seg.ID}); err != nil || got == nil || len(got) != 0 || m.lastIDs != nil {
		t.Fatalf("sin ids solo valida: %v %v", got, err)
	}
	missing := uuid.New()
	if _, err := f.uc.MatchContacts(context.Background(), f.tenant, MatchInput{SegmentID: &missing}); !errors.Is(err, domain.ErrSegmentNotFound) {
		t.Fatalf("segmento inexistente: %v", err)
	}
	if _, err := f.uc.MatchContacts(context.Background(), f.tenant, MatchInput{Definition: json.RawMessage(`{"match":"all","rules":[{"field":"attributes.nada","op":"exists"}]}`)}); !errors.Is(err, domain.ErrInvalidSegment) {
		t.Fatalf("definicion invalida: %v", err)
	}
	for _, in := range []MatchInput{{}, {SegmentID: &seg.ID, Definition: def}} {
		if _, err := f.uc.MatchContacts(context.Background(), f.tenant, in); !errors.Is(err, ErrMatchSource) {
			t.Fatalf("fuente ambigua: %v", err)
		}
	}
	many := make([]uuid.UUID, MaxMatchIDs+1)
	for i := range many {
		many[i] = uuid.New()
	}
	if _, err := f.uc.MatchContacts(context.Background(), f.tenant, MatchInput{Definition: def, ContactIDs: many}); !errors.Is(err, domain.ErrInvalidContactIDs) {
		t.Fatalf("demasiados ids: %v", err)
	}
}

func TestAniversariosPorZonaDelContacto(t *testing.T) {
	f, _, m := behaviorFixture(t)
	f.declare(t, "cumple", domain.AttrDate, false)
	f.declare(t, "plan", domain.AttrString, false)
	// 2026-09-23 03:00 UTC: 22:00 del 22 en Lima, 12:00 del 23 en Tokio.
	f.now = time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	lima, tokio, rota := "America/Lima", "Asia/Tokyo", "Zona/Rota"
	sinZona, enLima, enTokio, zonaRota := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	m.candidates = []ports.AnniversaryCandidate{
		{ID: sinZona, Value: "1990-09-23"},
		{ID: enLima, Value: "1990-09-22", Timezone: &lima},
		{ID: enTokio, Value: "1990-09-23", Timezone: &tokio},
		{ID: zonaRota, Value: "1990-09-23", Timezone: &rota},
	}
	page, err := f.uc.Anniversaries(context.Background(), f.tenant, AnniversaryInput{Attribute: "cumple", Hour: 9, FallbackTimezone: "Asia/Tokyo", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	late, err := f.uc.Anniversaries(context.Background(), f.tenant, AnniversaryInput{Attribute: "cumple", Hour: 23, FallbackTimezone: "Asia/Tokyo", Limit: 10})
	if err != nil || len(late.Matches) != 0 {
		t.Fatalf("antes de la hora local no toca a nadie: %+v %v", late, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	// En Lima el 22 ya paso de las 9: su aniversario de este ano es el del 22.
	want := map[uuid.UUID]string{sinZona: "2026-09-23", enLima: "2026-09-22", enTokio: "2026-09-23", zonaRota: "2026-09-23"}
	if len(page.Matches) != len(want) {
		t.Fatalf("coincidencias: %+v", page.Matches)
	}
	for _, mt := range page.Matches {
		if want[mt.ContactID] != mt.Occurrence {
			t.Fatalf("coincidencia inesperada: %+v", mt)
		}
	}
	if page.NextCursor != nil {
		t.Fatal("una tanda incompleta termina el recorrido")
	}
	if m.lastQuery.Attribute != "cumple" || len(m.lastQuery.MonthDays) != 3 {
		t.Fatalf("prefiltro: %+v", m.lastQuery)
	}

	full, err := f.uc.Anniversaries(context.Background(), f.tenant, AnniversaryInput{Attribute: "cumple", Hour: 9, FallbackTimezone: "UTC", Limit: 4})
	if err != nil || full.NextCursor == nil {
		t.Fatalf("una tanda llena sigue: %+v %v", full, err)
	}

	for name, in := range map[string]AnniversaryInput{
		"hora":          {Attribute: "cumple", Hour: 24, FallbackTimezone: "UTC"},
		"zona":          {Attribute: "cumple", Hour: 9, FallbackTimezone: "Marte/Olimpo"},
		"sin zona":      {Attribute: "cumple", Hour: 9},
		"no es fecha":   {Attribute: "plan", Hour: 9, FallbackTimezone: "UTC"},
		"no declarado":  {Attribute: "nada", Hour: 9, FallbackTimezone: "UTC"},
		"zona del host": {Attribute: "cumple", Hour: 9, FallbackTimezone: "Local"},
	} {
		if _, err := f.uc.Anniversaries(context.Background(), f.tenant, in); !errors.Is(err, domain.ErrInvalidAnniversary) {
			t.Errorf("%s: %v", name, err)
		}
	}
	listID := uuid.New()
	if _, err := f.uc.Anniversaries(context.Background(), f.tenant, AnniversaryInput{Attribute: "cumple", Hour: 9, FallbackTimezone: "UTC", ListID: &listID}); !errors.Is(err, domain.ErrListNotFound) {
		t.Fatalf("lista inexistente: %v", err)
	}
}
