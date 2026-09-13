package app

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

var now = time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC)

type harness struct {
	t      *testing.T
	store  *fakeStore
	uc     *UseCase
	tenant uuid.UUID
}

func newHarness(t *testing.T) *harness {
	s := newFakeStore()
	return &harness{t: t, store: s, uc: newTestUseCase(s, now), tenant: uuid.New()}
}

func (h *harness) event(msg uuid.UUID, m domain.Milestone, occurred time.Time) domain.MessageEvent {
	return domain.MessageEvent{
		EventID: uuid.New(), TenantID: h.tenant, MessageID: msg, Milestone: m,
		Class: domain.ClassTransactional, RecipientDomain: "example.com", OccurredAt: occurred,
	}
}

func (h *harness) ingest(ev domain.MessageEvent) IngestResult {
	h.t.Helper()
	res, err := h.uc.IngestMessageEvent(context.Background(), ev)
	if err != nil {
		h.t.Fatalf("ingesta: %v", err)
	}
	return res
}

func (h *harness) totals() domain.Counters {
	h.t.Helper()
	day := domain.Day(now)
	c, err := h.uc.Overview(context.Background(), h.tenant, domain.ClassQuery{Range: domain.Range{From: day.AddDate(0, 0, -30), To: day}})
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func TestIdempotenciaPorEventID(t *testing.T) {
	h := newHarness(t)
	ev := h.event(uuid.New(), domain.MilestoneSent, now.Add(-time.Hour))
	if res := h.ingest(ev); res.Duplicate || !res.Changed {
		t.Fatalf("primera entrega: %+v", res)
	}
	if res := h.ingest(ev); !res.Duplicate {
		t.Fatalf("la reentrega es duplicada: %+v", res)
	}
	if got := h.totals(); got.Sent != 1 {
		t.Fatalf("el mismo evento dos veces suma una: %+v", got)
	}
}

func TestAperturaUnicaEnLosAgregados(t *testing.T) {
	h := newHarness(t)
	msg := uuid.New()
	for _, m := range []domain.Milestone{domain.MilestoneSent, domain.MilestoneDelivered, domain.MilestoneOpened, domain.MilestoneOpened, domain.MilestoneClicked, domain.MilestoneClicked} {
		h.ingest(h.event(msg, m, now.Add(-time.Hour)))
	}
	got := h.totals()
	if got.Sent != 1 || got.Delivered != 1 || got.OpenedUnique != 1 || got.ClickedUnique != 1 {
		t.Fatalf("aperturas y clics unicos: %+v", got)
	}
	if rates := got.Rates(); rates.Open != "1.0000" || rates.Click != "1.0000" {
		t.Fatalf("tasas: %+v", rates)
	}
}

func TestReboteDuroYBlando(t *testing.T) {
	h := newHarness(t)
	bounce := func(msg uuid.UUID, kind domain.BounceKind) {
		ev := h.event(msg, domain.MilestoneBounced, now.Add(-time.Hour))
		ev.BounceKind = kind
		h.ingest(ev)
	}
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	bounce(a, domain.BounceSoft)
	bounce(b, domain.BounceHard)
	bounce(c, domain.BounceSoft)
	bounce(c, domain.BounceHard)
	bounce(a, domain.BounceSoft)
	if got := h.totals(); got.BouncedHard != 2 || got.BouncedSoft != 1 {
		t.Fatalf("duros 2 (uno reclasificado) y blandos 1: %+v", got)
	}
}

func TestDimensionesDelPrimerEvento(t *testing.T) {
	h := newHarness(t)
	msg, campaign := uuid.New(), uuid.New()
	first := h.event(msg, domain.MilestoneSent, now.Add(-2*time.Hour))
	first.Class, first.CampaignID, first.RecipientDomain = domain.ClassMarketing, &campaign, "mail.example"
	h.ingest(first)
	later := h.event(msg, domain.MilestoneDelivered, now.Add(-time.Hour))
	later.Class, later.RecipientDomain = domain.ClassTransactional, "otro.example"
	h.ingest(later)

	day := domain.Day(now)
	mk := h.store.class[classKey{h.tenant, day, domain.ClassMarketing}]
	if mk.Sent != 1 || mk.Delivered != 1 {
		t.Fatalf("todo el mensaje cae en marketing: %+v", mk)
	}
	if _, ok := h.store.class[classKey{h.tenant, day, domain.ClassTransactional}]; ok {
		t.Fatal("un evento posterior no cambia la clase del mensaje")
	}
	if c := h.store.campaign[campaignKey{h.tenant, day, campaign}]; c.Delivered != 1 {
		t.Fatalf("la campana del primer evento: %+v", c)
	}
	if d := h.store.domains[domainKey{h.tenant, day, domain.ClassMarketing, "mail.example"}]; d.Delivered != 1 {
		t.Fatalf("el dominio del primer evento: %+v", d)
	}
}

func TestSinCampanaNiDominioSoloCuentaLaClase(t *testing.T) {
	h := newHarness(t)
	ev := h.event(uuid.New(), domain.MilestoneSent, now.Add(-time.Hour))
	ev.RecipientDomain = ""
	h.ingest(ev)
	if len(h.store.campaign) != 0 || len(h.store.domains) != 0 || len(h.store.class) != 1 {
		t.Fatalf("campana %d, dominio %d, clase %d", len(h.store.campaign), len(h.store.domains), len(h.store.class))
	}
}

func TestFalloDeLaBaseNoDejaElEventoContado(t *testing.T) {
	h := newHarness(t)
	ev := h.event(uuid.New(), domain.MilestoneSent, now.Add(-time.Hour))
	h.store.failApply = errors.New("la base no responde")
	if _, err := h.uc.IngestMessageEvent(context.Background(), ev); err == nil || IsPermanent(err) {
		t.Fatalf("un fallo de base es transitorio: %v", err)
	}
	if h.store.processed[ev.EventID] || len(h.store.facts) != 0 {
		t.Fatal("la transaccion fallida no deja rastro")
	}
	h.store.failApply = nil
	if res := h.ingest(ev); res.Duplicate {
		t.Fatal("el reintento cuenta")
	}
	if got := h.totals(); got.Sent != 1 {
		t.Fatalf("reintento: %+v", got)
	}
}

func TestEventoInvalidoEsPermanente(t *testing.T) {
	h := newHarness(t)
	ev := h.event(uuid.Nil, domain.MilestoneSent, now)
	if _, err := h.uc.IngestMessageEvent(context.Background(), ev); !IsPermanent(err) {
		t.Fatalf("sin message_id se descarta: %v", err)
	}
}

func TestInstanteSinDeclararUsaElDelSobre(t *testing.T) {
	h := newHarness(t)
	ev := h.event(uuid.New(), domain.MilestoneSent, time.Time{})
	ev.PublishedAt = now.Add(-26 * time.Hour)
	h.ingest(ev)
	if _, ok := h.store.class[classKey{h.tenant, domain.Day(now.Add(-26 * time.Hour)), domain.ClassTransactional}]; !ok {
		t.Fatal("cuenta en el dia del sobre")
	}
}

func TestCampanaFueraDeOrden(t *testing.T) {
	h := newHarness(t)
	campaign := uuid.New()
	ev := func(a domain.CampaignAction, t time.Time) domain.CampaignEvent {
		return domain.CampaignEvent{EventID: uuid.New(), TenantID: h.tenant, CampaignID: campaign, Action: a, OccurredAt: t}
	}
	completed := ev(domain.CampaignCompleted, now.Add(-time.Hour))
	started := ev(domain.CampaignStarted, now.Add(-5*time.Hour))
	for _, e := range []domain.CampaignEvent{completed, started, started} {
		if _, err := h.uc.IngestCampaignEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	got := h.store.campaigns[campaign]
	if got.Status != "completed" || got.StartedAt == nil || got.CompletedAt == nil {
		t.Fatalf("estado final: %+v", got)
	}
}

func TestPodaUsaLasRetenciones(t *testing.T) {
	h := newHarness(t)
	if _, err := h.uc.Prune(context.Background(), h.tenant); err != nil {
		t.Fatal(err)
	}
	if !h.store.factsBefore.Equal(now.Add(-90*24*time.Hour)) || !h.store.eventsBefore.Equal(now.Add(-ProcessedEventsRetention)) {
		t.Fatalf("cortes: mensajes %v, eventos %v", h.store.factsBefore, h.store.eventsBefore)
	}
}

func TestCampanasPaginaFueraDelTotal(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 3; i++ {
		h.store.summaries = append(h.store.summaries, domain.CampaignSummary{CampaignID: uuid.New()})
	}
	list, total, err := h.uc.Campaigns(context.Background(), h.tenant, 2, 2)
	if err != nil || total != 3 || len(list) != 1 {
		t.Fatalf("segunda pagina: %d de %d, %v", len(list), total, err)
	}
	calls := h.store.listCalls
	for _, page := range []int{3, math.MaxInt} {
		list, _, err := h.uc.Campaigns(context.Background(), h.tenant, page, 2)
		if err != nil || len(list) != 0 {
			t.Fatalf("pagina %d: %d, %v", page, len(list), err)
		}
	}
	if h.store.listCalls != calls {
		t.Fatal("una pagina fuera del total no consulta")
	}
}

func TestSerieDeCampanaPorDefecto(t *testing.T) {
	h := newHarness(t)
	first, last := domain.Day(now.AddDate(0, 0, -4)), domain.Day(now.AddDate(0, 0, -1))
	id := uuid.New()
	h.store.summaries = []domain.CampaignSummary{{CampaignID: id, FirstDay: &first, LastDay: &last}}
	detail, err := h.uc.Campaign(context.Background(), h.tenant, id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Range.Days() != 4 || !h.store.seriesRange.From.Equal(first) {
		t.Fatalf("rango por defecto: %+v", detail.Range)
	}
	if _, err := h.uc.Campaign(context.Background(), h.tenant, id, "2026-09-10", "2026-09-01"); !domain.IsValidation(err) {
		t.Fatalf("rango explicito invalido: %v", err)
	}
	if _, err := h.uc.Campaign(context.Background(), h.tenant, uuid.New(), "", ""); !errors.Is(err, domain.ErrCampaignNotFound) {
		t.Fatalf("campana desconocida: %v", err)
	}
}
