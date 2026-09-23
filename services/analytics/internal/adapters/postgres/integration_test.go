//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/analytics/internal/app"
	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	migrateOnce sync.Once
	migrateErr  error
)

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

// applyMigrationsTwice aplica en orden, dos veces, las migraciones de cada directorio
// (relativo a la raiz del repositorio): la segunda pasada demuestra que toleran
// re-ejecutarse.
func applyMigrationsTwice(ctx context.Context, pool *pgxpool.Pool, dirs ...string) error {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")
	var files []string
	for _, dir := range dirs {
		found, err := filepath.Glob(filepath.Join(root, dir, "*.sql"))
		if err != nil || len(found) == 0 {
			return fmt.Errorf("migraciones de %s: %v", dir, err)
		}
		sort.Strings(found)
		files = append(files, found...)
	}
	for pass := 1; pass <= 2; pass++ {
		for _, f := range files {
			sql, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("pasada %d, %s: %w", pass, filepath.Base(f), err)
			}
		}
	}
	return nil
}

// testCtx abre ANALYTICS_TEST_DSN, una base desechable, y la primera vez le aplica las
// migraciones de analytics. Cada prueba usa una empresa nueva, asi que la base puede
// reutilizarse.
func testCtx(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := integrationEnv(t, "ANALYTICS_TEST_DSN")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrateOnce.Do(func() {
		migrateErr = applyMigrationsTwice(context.Background(), pool, "migrations/tenant/canonical/analytics")
	})
	if migrateErr != nil {
		t.Fatalf("migraciones: %v", migrateErr)
	}
	return db.WithPool(context.Background(), pool), pool
}

func newUseCase(now func() time.Time) *app.UseCase {
	p := &db.ContextPool{}
	return app.New(app.Deps{
		Tx: p, Ledger: NewLedger(p), Facts: NewFacts(p), Stats: NewStats(p),
		Campaigns: NewCampaigns(p), Links: NewLinks(p), Reports: NewReports(p),
		MessageRetention: 90 * 24 * time.Hour, Now: now,
	})
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	uc     *app.UseCase
	rep    *Reports
	tenant uuid.UUID
	today  time.Time
}

func newFixture(t *testing.T) *fixture {
	ctx, pool := testCtx(t)
	return &fixture{t: t, ctx: ctx, pool: pool, uc: newUseCase(nil), rep: NewReports(&db.ContextPool{}),
		tenant: uuid.New(), today: domain.Day(time.Now())}
}

func (f *fixture) event(msg uuid.UUID, m domain.Milestone, at time.Time) domain.MessageEvent {
	return domain.MessageEvent{EventID: uuid.New(), TenantID: f.tenant, MessageID: msg, Milestone: m,
		Class: domain.ClassMarketing, RecipientDomain: "gmail.com", OccurredAt: at}
}

func (f *fixture) ingest(ev domain.MessageEvent) app.IngestResult {
	f.t.Helper()
	res, err := f.uc.IngestMessageEvent(f.ctx, ev)
	if err != nil {
		f.t.Fatalf("ingesta %s: %v", ev.Milestone, err)
	}
	return res
}

func (f *fixture) totals(from, to time.Time, class domain.Class) domain.Counters {
	f.t.Helper()
	c, err := f.rep.Totals(f.ctx, f.tenant, domain.ClassQuery{Range: domain.Range{From: from, To: to}, Class: class})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) count(table string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, f.tenant).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestMismoEventoDosVecesSumaUna(t *testing.T) {
	f := newFixture(t)
	ev := f.event(uuid.New(), domain.MilestoneSent, time.Now().Add(-time.Minute))
	if res := f.ingest(ev); res.Duplicate || !res.Changed {
		t.Fatalf("primera: %+v", res)
	}
	if res := f.ingest(ev); !res.Duplicate {
		t.Fatalf("segunda: %+v", res)
	}
	if got := f.totals(f.today, f.today, ""); got.Sent != 1 {
		t.Fatalf("una sola suma: %+v", got)
	}
	if n := f.count("analytics.processed_events"); n != 1 {
		t.Fatalf("processed_events: %d", n)
	}
	if other := (&fixture{t: t, ctx: f.ctx, rep: f.rep, tenant: uuid.New()}).totals(f.today, f.today, ""); !other.IsZero() {
		t.Fatalf("otra empresa no ve nada: %+v", other)
	}
}

func TestEventosConcurrentesDelMismoMensaje(t *testing.T) {
	f := newFixture(t)
	const messages = 15
	campaign := uuid.New()
	at := time.Now().Add(-time.Minute)
	var evs []domain.MessageEvent
	for i := 0; i < messages; i++ {
		msg := uuid.New()
		plan := []domain.Milestone{domain.MilestoneSent, domain.MilestoneDelivered,
			domain.MilestoneOpened, domain.MilestoneOpened, domain.MilestoneOpened, domain.MilestoneOpened,
			domain.MilestoneClicked, domain.MilestoneClicked, domain.MilestoneBounced, domain.MilestoneBounced}
		for j, m := range plan {
			ev := f.event(msg, m, at.Add(-time.Duration(j)*time.Second))
			ev.CampaignID = &campaign
			if m == domain.MilestoneBounced {
				ev.BounceKind = domain.BounceSoft
				if j%2 == 1 {
					ev.BounceKind = domain.BounceHard
				}
			}
			evs = append(evs, ev)
		}
		// La misma entrega dos veces a la vez: la reentrega que cruza con la original.
		evs = append(evs, evs[len(evs)-1])
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(evs))
	start := make(chan struct{})
	for _, ev := range evs {
		wg.Add(1)
		go func(ev domain.MessageEvent) {
			defer wg.Done()
			<-start
			if _, err := f.uc.IngestMessageEvent(f.ctx, ev); err != nil {
				errs <- err
			}
		}(ev)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("ingesta concurrente: %v", err)
	}

	want := domain.Counters{Sent: messages, Delivered: messages, OpenedUnique: messages, ClickedUnique: messages, BouncedHard: messages}
	if got := f.totals(f.today.AddDate(0, 0, -1), f.today, ""); got != want {
		t.Fatalf("clase: %+v, se esperaba %+v", got, want)
	}
	summary, err := f.rep.GetCampaign(f.ctx, f.tenant, campaign)
	if err != nil || summary.Totals != want {
		t.Fatalf("campana: %+v %v", summary, err)
	}
	domains, err := f.rep.TopDomains(f.ctx, f.tenant, domain.ClassQuery{Range: domain.Range{From: f.today.AddDate(0, 0, -1), To: f.today}}, 10)
	if err != nil || len(domains) != 1 || domains[0].Totals != want {
		t.Fatalf("dominio: %+v %v", domains, err)
	}
	if n := f.count("analytics.message_facts"); n != messages {
		t.Fatalf("una fila por mensaje: %d", n)
	}
}

func TestCorreccionesEnLaBase(t *testing.T) {
	f := newFixture(t)
	msg := uuid.New()
	soft := f.event(msg, domain.MilestoneBounced, time.Now().Add(-time.Hour))
	soft.BounceKind = domain.BounceSoft
	hard := f.event(msg, domain.MilestoneBounced, time.Now().Add(-time.Minute))
	hard.BounceKind = domain.BounceHard
	f.ingest(soft)
	f.ingest(hard)
	if got := f.totals(f.today.AddDate(0, 0, -1), f.today, ""); got.BouncedSoft != 0 || got.BouncedHard != 1 {
		t.Fatalf("el duro reclasifica al blando: %+v", got)
	}

	// Una apertura de ayer que llega despues de la de hoy mueve la cuenta a ayer.
	yesterday := f.today.AddDate(0, 0, -1)
	open := uuid.New()
	f.ingest(f.event(open, domain.MilestoneOpened, f.today.Add(time.Minute)))
	f.ingest(f.event(open, domain.MilestoneOpened, yesterday.Add(12*time.Hour)))
	series, err := f.rep.Series(f.ctx, f.tenant, domain.ClassQuery{Range: domain.Range{From: yesterday, To: f.today}})
	if err != nil || len(series) != 2 {
		t.Fatalf("serie: %+v %v", series, err)
	}
	if series[0].Counters.OpenedUnique != 1 || series[1].Counters.OpenedUnique != 0 {
		t.Fatalf("la apertura queda en su primer dia: %+v", series)
	}
}

func TestPodaNoTocaAgregados(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		msg := uuid.New()
		f.ingest(f.event(msg, domain.MilestoneSent, time.Now().Add(-time.Minute)))
		f.ingest(f.event(msg, domain.MilestoneDelivered, time.Now().Add(-time.Minute)))
	}
	other := &fixture{t: t, ctx: f.ctx, pool: f.pool, uc: f.uc, rep: f.rep, tenant: uuid.New(), today: f.today}
	other.ingest(other.event(uuid.New(), domain.MilestoneSent, time.Now().Add(-time.Minute)))

	before := f.totals(f.today, f.today, "")
	later := newUseCase(func() time.Time { return time.Now().Add(91 * 24 * time.Hour) })
	res, err := later.Prune(f.ctx, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 3 || res.Events != 6 {
		t.Fatalf("poda: %+v", res)
	}
	if n := f.count("analytics.message_facts"); n != 0 {
		t.Fatalf("quedan %d mensajes", n)
	}
	if after := f.totals(f.today, f.today, ""); after != before || after.Sent != 3 {
		t.Fatalf("los agregados no se podan: antes %+v, despues %+v", before, after)
	}
	if n := other.count("analytics.message_facts"); n != 1 {
		t.Fatalf("la poda de una empresa no toca otra: %d", n)
	}
}

func TestSerieRellenaLosDias(t *testing.T) {
	f := newFixture(t)
	from := f.today.AddDate(0, 0, -9)
	f.ingest(f.event(uuid.New(), domain.MilestoneSent, from.AddDate(0, 0, 4).Add(3*time.Hour)))
	f.ingest(f.event(uuid.New(), domain.MilestoneSent, from.AddDate(0, 0, 7).Add(3*time.Hour)))
	series, err := f.rep.Series(f.ctx, f.tenant, domain.ClassQuery{Range: domain.Range{From: from, To: f.today}, Class: domain.ClassMarketing})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 10 {
		t.Fatalf("un punto por dia: %d", len(series))
	}
	for i, p := range series {
		want := int64(0)
		if i == 4 || i == 7 {
			want = 1
		}
		if !p.Day.Equal(from.AddDate(0, 0, i)) || p.Counters.Sent != want {
			t.Fatalf("dia %d: %s sent=%d", i, domain.FormatDate(p.Day), p.Counters.Sent)
		}
	}
	empty, err := f.rep.Series(f.ctx, f.tenant, domain.ClassQuery{Range: domain.Range{From: from, To: f.today}, Class: domain.ClassTransactional})
	if err != nil || len(empty) != 10 || !empty[4].Counters.IsZero() {
		t.Fatalf("otra clase, todo en cero: %+v %v", empty, err)
	}
}

func TestCampanasListadoYDetalle(t *testing.T) {
	f := newFixture(t)
	seen, onlyStats, latest := uuid.New(), uuid.New(), uuid.New()
	base := time.Now().Add(-3 * time.Hour)
	campaignEvent := func(id uuid.UUID, a domain.CampaignAction, at time.Time) {
		if _, err := f.uc.IngestCampaignEvent(f.ctx, domain.CampaignEvent{EventID: uuid.New(), TenantID: f.tenant, CampaignID: id, Action: a, OccurredAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	campaignEvent(seen, domain.CampaignCompleted, base.Add(2*time.Hour))
	campaignEvent(seen, domain.CampaignStarted, base)
	campaignEvent(latest, domain.CampaignStarted, base.Add(time.Hour))
	for i := 0; i < 2; i++ {
		ev := f.event(uuid.New(), domain.MilestoneSent, time.Now().Add(-time.Minute))
		ev.CampaignID = &onlyStats
		f.ingest(ev)
	}

	total, err := f.rep.CountCampaigns(f.ctx, f.tenant)
	if err != nil || total != 3 {
		t.Fatalf("total: %d %v", total, err)
	}
	list, err := f.rep.ListCampaigns(f.ctx, f.tenant, 10, 0)
	if err != nil || len(list) != 3 {
		t.Fatalf("listado: %+v %v", list, err)
	}
	if list[0].CampaignID != latest || list[1].CampaignID != seen || list[2].CampaignID != onlyStats || list[2].Totals.Sent != 2 {
		t.Fatalf("orden por inicio descendente, sin inicio al final: %+v", list)
	}
	page, err := f.rep.ListCampaigns(f.ctx, f.tenant, 1, 1)
	if err != nil || len(page) != 1 || page[0].CampaignID != seen {
		t.Fatalf("paginado: %+v %v", page, err)
	}

	got, err := f.rep.GetCampaign(f.ctx, f.tenant, seen)
	if err != nil || got.Status == nil || *got.Status != "completed" || got.StartedAt == nil || got.CompletedAt == nil || got.FirstDay != nil {
		t.Fatalf("campana vista: %+v %v", got, err)
	}
	detail, err := f.uc.Campaign(f.ctx, f.tenant, onlyStats, "", "")
	if err != nil || detail.Summary.Status != nil || detail.Summary.Totals.Sent != 2 || len(detail.Series) != detail.Range.Days() {
		t.Fatalf("campana solo con envios: %+v %v", detail, err)
	}
	if _, err := f.rep.GetCampaign(f.ctx, f.tenant, uuid.New()); !errors.Is(err, domain.ErrCampaignNotFound) {
		t.Fatalf("desconocida: %v", err)
	}
}

func TestTopDominios(t *testing.T) {
	f := newFixture(t)
	send := func(name string, class domain.Class, bounce bool) {
		msg := uuid.New()
		ev := f.event(msg, domain.MilestoneSent, time.Now().Add(-time.Minute))
		ev.RecipientDomain, ev.Class = name, class
		f.ingest(ev)
		if bounce {
			b := f.event(msg, domain.MilestoneBounced, time.Now().Add(-time.Minute))
			b.BounceKind = domain.BounceHard
			f.ingest(b)
		}
	}
	send("gmail.com", domain.ClassMarketing, true)
	send("gmail.com", domain.ClassMarketing, false)
	send("gmail.com", domain.ClassMarketing, false)
	send("outlook.com", domain.ClassMarketing, false)
	send("yahoo.com", domain.ClassTransactional, false)

	q := domain.ClassQuery{Range: domain.Range{From: f.today, To: f.today}, Class: domain.ClassMarketing}
	top, err := f.rep.TopDomains(f.ctx, f.tenant, q, 1)
	if err != nil || len(top) != 1 || top[0].RecipientDomain != "gmail.com" || top[0].Totals.Sent != 3 || top[0].Totals.Rates().Bounce != "0.3333" {
		t.Fatalf("top 1: %+v %v", top, err)
	}
	all, err := f.rep.TopDomains(f.ctx, f.tenant, domain.ClassQuery{Range: q.Range}, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("todas las clases: %+v %v", all, err)
	}
}
