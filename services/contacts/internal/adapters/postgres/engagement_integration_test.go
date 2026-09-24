//go:build integration

package postgres

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type engagementRow struct {
	received, lastEvent time.Time
	opened, clicked     *time.Time
}

func readEngagement(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contactID, campaignID uuid.UUID) (engagementRow, bool) {
	t.Helper()
	var r engagementRow
	err := pool.QueryRow(ctx,
		`SELECT received_at, last_opened_at, last_clicked_at, last_event_at FROM contacts.engagement
		  WHERE contact_id = $1 AND campaign_id = $2`, contactID, campaignID).Scan(&r.received, &r.opened, &r.clicked, &r.lastEvent)
	if err != nil {
		return engagementRow{}, false
	}
	return r, true
}

func touch(t *testing.T, ctx context.Context, repo *EngagementRepository, tenant, contact, campaign uuid.UUID, kind domain.EngagementKind, at time.Time) bool {
	t.Helper()
	tc, ok, err := domain.EngagementEvent{TenantID: tenant, ContactID: contact, CampaignID: campaign, Kind: kind, OccurredAt: at}.
		Touch(at.Add(time.Minute), 0)
	if err != nil || !ok {
		t.Fatalf("toque: %v %v", ok, err)
	}
	applied, err := repo.Touch(ctx, tc)
	if err != nil {
		t.Fatalf("Touch: %v", err)
	}
	return applied
}

func TestProyeccionDeInteraccionIdempotente(t *testing.T) {
	pool, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts, repo := NewContactRepository(cp), NewEngagementRepository(cp)
	tenant := uuid.New()
	c := insert(t, ctx, contacts, newContact(tenant, "proyeccion@example.com"))
	campaign := uuid.New()
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

	// Desordenados: el clic antes que la entrega y dos aperturas.
	touch(t, ctx, repo, tenant, c.ID, campaign, domain.EngagementClicked, base.Add(2*time.Hour))
	touch(t, ctx, repo, tenant, c.ID, campaign, domain.EngagementDelivered, base)
	touch(t, ctx, repo, tenant, c.ID, campaign, domain.EngagementOpened, base.Add(3*time.Hour))
	touch(t, ctx, repo, tenant, c.ID, campaign, domain.EngagementOpened, base.Add(time.Hour))
	row, ok := readEngagement(t, ctx, pool, c.ID, campaign)
	if !ok || !row.received.Equal(base) || !row.opened.Equal(base.Add(3*time.Hour)) || !row.clicked.Equal(base.Add(2*time.Hour)) ||
		!row.lastEvent.Equal(base.Add(3*time.Hour)) {
		t.Fatalf("proyeccion: %+v", row)
	}
	var updated time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM contacts.engagement WHERE contact_id = $1`, c.ID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	// Reentrega: la fila no cambia y no se reescribe.
	if !touch(t, ctx, repo, tenant, c.ID, campaign, domain.EngagementClicked, base.Add(2*time.Hour)) {
		t.Fatal("un contacto que existe cuenta como aplicado aunque nada cambie")
	}
	var again time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM contacts.engagement WHERE contact_id = $1`, c.ID).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if !again.Equal(updated) {
		t.Fatal("la reentrega reescribio la fila")
	}

	if touch(t, ctx, repo, tenant, uuid.New(), campaign, domain.EngagementOpened, base) {
		t.Fatal("un contacto que no existe no deja rastro")
	}
	if touch(t, ctx, repo, uuid.New(), c.ID, campaign, domain.EngagementOpened, base) {
		t.Fatal("el contacto de otra empresa no deja rastro")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM contacts.contacts WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := readEngagement(t, ctx, pool, c.ID, campaign); ok {
		t.Fatal("la interaccion cae con el contacto")
	}
}

func TestPodaDeLaInteraccion(t *testing.T) {
	pool, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts, repo := NewContactRepository(cp), NewEngagementRepository(cp)
	tenant := uuid.New()
	c := insert(t, ctx, contacts, newContact(tenant, "poda@example.com"))
	old, recent := uuid.New(), uuid.New()
	now := time.Now().UTC()
	touch(t, ctx, repo, tenant, c.ID, old, domain.EngagementOpened, now.Add(-100*24*time.Hour))
	touch(t, ctx, repo, tenant, c.ID, recent, domain.EngagementDelivered, now.Add(-100*24*time.Hour))
	touch(t, ctx, repo, tenant, c.ID, recent, domain.EngagementClicked, now.Add(-time.Hour))
	other := uuid.New()
	oc := insert(t, ctx, contacts, newContact(other, "poda@example.com"))
	touch(t, ctx, repo, other, oc.ID, old, domain.EngagementOpened, now.Add(-100*24*time.Hour))

	n, err := repo.Prune(ctx, tenant, now.Add(-30*24*time.Hour), 1000)
	if err != nil || n != 1 {
		t.Fatalf("poda: %d %v", n, err)
	}
	if _, ok := readEngagement(t, ctx, pool, c.ID, old); ok {
		t.Fatal("lo antiguo se poda")
	}
	if _, ok := readEngagement(t, ctx, pool, c.ID, recent); !ok {
		t.Fatal("una fila con actividad reciente se conserva aunque se recibiera hace tiempo")
	}
	if _, ok := readEngagement(t, ctx, pool, oc.ID, old); !ok {
		t.Fatal("la poda de una empresa no toca a otra")
	}
}

// Segmentos de comportamiento evaluados en la base: ana abrio la campana 1, beto hizo clic
// en la 2, carla recibio las tres y no abrio ninguna, dani solo recibio la 3 (hace 40 dias
// abrio la 1). La campana 3 es la mas reciente.
func TestSegmentosDeComportamiento(t *testing.T) {
	pool, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts, repo, q := NewContactRepository(cp), NewEngagementRepository(cp), NewSegmentQuery(cp)
	tenant := uuid.New()
	ana := insert(t, ctx, contacts, newContact(tenant, "ana@comp.test"))
	beto := insert(t, ctx, contacts, newContact(tenant, "beto@comp.test"))
	carla := insert(t, ctx, contacts, newContact(tenant, "carla@comp.test"))
	dani := insert(t, ctx, contacts, newContact(tenant, "dani@comp.test"))
	nadie := insert(t, ctx, contacts, newContact(tenant, "nadie@comp.test"))
	c1, c2, c3 := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	day := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	for _, ct := range []*domain.Contact{ana, beto, carla} {
		touch(t, ctx, repo, tenant, ct.ID, c1, domain.EngagementDelivered, day(50))
		touch(t, ctx, repo, tenant, ct.ID, c2, domain.EngagementDelivered, day(20))
		touch(t, ctx, repo, tenant, ct.ID, c3, domain.EngagementDelivered, day(5))
	}
	touch(t, ctx, repo, tenant, ana.ID, c1, domain.EngagementOpened, day(49))
	touch(t, ctx, repo, tenant, beto.ID, c2, domain.EngagementClicked, day(19))
	touch(t, ctx, repo, tenant, dani.ID, c1, domain.EngagementOpened, day(40))
	touch(t, ctx, repo, tenant, dani.ID, c3, domain.EngagementDelivered, day(5))
	// La misma interaccion en otra empresa no aparece.
	otro := insert(t, ctx, contacts, newContact(uuid.New(), "ana@comp.test"))
	touch(t, ctx, repo, otro.TenantID, otro.ID, c1, domain.EngagementOpened, day(1))

	names := map[uuid.UUID]string{ana.ID: "ana", beto.ID: "beto", carla.ID: "carla", dani.ID: "dani", nadie.ID: "nadie"}
	schema := segment.Schema{Enums: map[string][]string{"status": {"active"}}}
	cases := []struct {
		rule string
		want []string
	}{
		{`{"field":"campaign","op":"opened","value":"` + c1.String() + `"}`, []string{"ana", "dani"}},
		{`{"field":"campaign","op":"clicked","value":"` + c2.String() + `"}`, []string{"beto"}},
		{`{"field":"campaign","op":"opened","value":"` + c2.String() + `"}`, []string{"beto"}},
		{`{"field":"last_campaigns","op":"opened","value":1}`, []string{}},
		{`{"field":"last_campaigns","op":"opened","value":2}`, []string{"beto", "dani"}},
		{`{"field":"last_campaigns","op":"opened","value":3}`, []string{"ana", "beto", "dani"}},
		{`{"field":"last_campaigns","op":"clicked","value":3}`, []string{"beto"}},
		{`{"field":"last_campaigns","op":"not_opened","value":3}`, []string{"carla"}},
		{`{"field":"last_campaigns","op":"not_opened","value":1}`, []string{"ana", "beto", "carla", "dani"}},
		{`{"field":"last_campaigns","op":"not_opened","value":4}`, []string{}},
		{`{"field":"last_days","op":"opened","value":30}`, []string{"beto"}},
		{`{"field":"last_days","op":"opened","value":45}`, []string{"beto", "dani"}},
		{`{"field":"last_days","op":"clicked","value":30}`, []string{"beto"}},
		{`{"field":"last_days","op":"clicked","value":10}`, []string{}},
	}
	all := []uuid.UUID{ana.ID, beto.ID, carla.ID, dani.ID, nadie.ID}
	// La audiencia solo recorre enviables: se les da el consentimiento para comprobar la
	// exclusion.
	if _, err := pool.Exec(ctx, `UPDATE contacts.contacts SET marketing_consent = 'granted' WHERE tenant_id = $1`, tenant); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		def, err := segment.Parse([]byte(`{"match":"all","rules":[` + tc.rule + `]}`))
		if err != nil {
			t.Fatal(err)
		}
		page, err := q.Page(ctx, tenant, def, schema, 100, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.rule, err)
		}
		got := []string{}
		for _, c := range page {
			got = append(got, names[c.ID])
		}
		sort.Strings(got)
		if len(got) != len(tc.want) || (len(got) > 0 && !equalStrings(got, tc.want)) {
			t.Errorf("%s: %v, se esperaba %v", tc.rule, got, tc.want)
		}
		count, err := q.Count(ctx, tenant, def, schema)
		if err != nil || count != int64(len(tc.want)) {
			t.Errorf("%s: cuenta %d %v", tc.rule, count, err)
		}
		// Dentro de una exclusion (NOT) el predicado sigue siendo de dos valores.
		spec := ports.AudienceSpec{Include: []segment.Definition{mustDef(t, `{"match":"all","rules":[{"field":"status","op":"eq","value":"active"}]}`)},
			Exclude: []segment.Definition{def}, Schema: schema, Limit: 100}
		rest, err := q.Audience(ctx, tenant, spec)
		if err != nil || len(rest) != len(all)-len(tc.want) {
			t.Errorf("%s excluido: %d contactos %v", tc.rule, len(rest), err)
		}
		matched, err := q.MatchAmong(ctx, tenant, def, schema, all)
		if err != nil || len(matched) != len(tc.want) {
			t.Errorf("%s por ids: %v %v", tc.rule, matched, err)
		}
	}
}

func mustDef(t *testing.T, s string) segment.Definition {
	t.Helper()
	def, err := segment.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCandidatosDeAniversarioEnLaBase(t *testing.T) {
	_, ctx := testPool(t)
	cp := &db.ContextPool{}
	contacts, lists, q := NewContactRepository(cp), NewListRepository(cp), NewSegmentQuery(cp)
	tenant := uuid.New()
	mk := func(email string, attrs map[string]any, tz *string) *domain.Contact {
		c := newContact(tenant, email)
		c.Attributes, c.Timezone = attrs, tz
		return insert(t, ctx, contacts, c)
	}
	hoy := mk("hoy@aniv.test", map[string]any{"cumple": "1990-09-23"}, strp("Asia/Tokyo"))
	bisiesto := mk("bisiesto@aniv.test", map[string]any{"cumple": "1992-02-29"}, nil)
	mk("otro-dia@aniv.test", map[string]any{"cumple": "1990-10-23"}, nil)
	mk("sin-clave@aniv.test", map[string]any{"alta": "1990-09-23"}, nil)
	mk("no-texto@aniv.test", map[string]any{"cumple": 19900923}, nil)
	insert(t, ctx, contacts, func() *domain.Contact {
		c := newContact(uuid.New(), "hoy@aniv.test")
		c.Attributes = map[string]any{"cumple": "1990-09-23"}
		return c
	}())

	got, err := q.AnniversaryCandidates(ctx, tenant, ports.AnniversaryQuery{
		Attribute: "cumple", MonthDays: []string{"09-23", "02-29"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[uuid.UUID]ports.AnniversaryCandidate{}
	for _, c := range got {
		ids[c.ID] = c
	}
	if len(got) != 2 || ids[hoy.ID].Value != "1990-09-23" || ids[hoy.ID].Timezone == nil || *ids[hoy.ID].Timezone != "Asia/Tokyo" ||
		ids[bisiesto.ID].Value != "1992-02-29" || ids[bisiesto.ID].Timezone != nil {
		t.Fatalf("candidatos: %+v", got)
	}

	page, err := q.AnniversaryCandidates(ctx, tenant, ports.AnniversaryQuery{Attribute: "cumple", MonthDays: []string{"09-23", "02-29"}, Limit: 1})
	if err != nil || len(page) != 1 {
		t.Fatalf("tanda de uno: %v %v", page, err)
	}
	next, err := q.AnniversaryCandidates(ctx, tenant, ports.AnniversaryQuery{Attribute: "cumple", MonthDays: []string{"09-23", "02-29"}, After: page[0].ID, Limit: 1})
	if err != nil || len(next) != 1 || next[0].ID == page[0].ID {
		t.Fatalf("siguiente tanda: %v %v", next, err)
	}

	l := &domain.List{TenantID: tenant, Name: "Aniversarios"}
	if err := lists.Create(ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := lists.AddMembers(ctx, tenant, l.ID, []uuid.UUID{bisiesto.ID}); err != nil {
		t.Fatal(err)
	}
	inList, err := q.AnniversaryCandidates(ctx, tenant, ports.AnniversaryQuery{Attribute: "cumple", MonthDays: []string{"09-23", "02-29"}, ListID: &l.ID, Limit: 10})
	if err != nil || len(inList) != 1 || inList[0].ID != bisiesto.ID {
		t.Fatalf("filtro por lista: %v %v", inList, err)
	}
}
