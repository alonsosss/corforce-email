//go:build integration

package postgres

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

func (f *fixture) click(msg, campaign uuid.UUID, link string, at time.Time) domain.MessageEvent {
	ev := f.event(msg, domain.MilestoneClicked, at)
	ev.CampaignID = &campaign
	ev.Link = link
	return ev
}

func (f *fixture) links(campaign uuid.UUID, limit int) *domain.CampaignLinks {
	f.t.Helper()
	out, err := f.rep.CampaignLinks(f.ctx, f.tenant, campaign, limit)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestClicsPorEnlaceEnLaBase(t *testing.T) {
	f := newFixture(t)
	campaign := uuid.New()
	const oferta, blog = "https://tienda.test/oferta", "https://tienda.test/blog"
	ana, eva := uuid.New(), uuid.New()
	early, late := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Minute)

	// Clics concurrentes de la misma persona en el mismo enlace, con una reentrega cruzada:
	// la fila del mensaje los serializa y los unicos no se inflan.
	evs := []domain.MessageEvent{f.click(ana, campaign, oferta, late), f.click(ana, campaign, oferta, early)}
	evs = append(evs, evs[0], f.click(eva, campaign, oferta, late), f.click(ana, campaign, blog, late))
	var wg sync.WaitGroup
	errs := make(chan error, len(evs))
	for _, ev := range evs {
		wg.Add(1)
		go func(ev domain.MessageEvent) {
			defer wg.Done()
			if _, err := f.uc.IngestMessageEvent(f.ctx, ev); err != nil {
				errs <- err
			}
		}(ev)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("ingesta: %v", err)
	}

	got := f.links(campaign, 10)
	if got.TotalLinks != 2 || got.TotalClicks != 4 || len(got.Links) != 2 {
		t.Fatalf("totales: %+v", got)
	}
	top := got.Links[0]
	if top.URL != oferta || top.Clicks != 3 || top.UniqueClicks != 2 {
		t.Fatalf("oferta primero, por clics: %+v", top)
	}
	if !top.FirstClickedAt.Equal(early.Truncate(time.Microsecond)) || !top.LastClickedAt.Equal(late.Truncate(time.Microsecond)) {
		t.Fatalf("primer y ultimo clic: %+v", top)
	}
	if only := f.links(campaign, 1); len(only.Links) != 1 || only.TotalClicks != 4 {
		t.Fatalf("el limite recorta la lista, no los totales: %+v", only)
	}
	if other := (&fixture{t: t, ctx: f.ctx, rep: f.rep, tenant: uuid.New()}).links(campaign, 10); other.TotalClicks != 0 || len(other.Links) != 0 {
		t.Fatalf("otra empresa no ve los clics: %+v", other)
	}
	if n := f.count("analytics.campaign_link_clicks"); n != 3 {
		t.Fatalf("un recuerdo por mensaje y enlace: %d", n)
	}

	later := newUseCase(func() time.Time { return time.Now().Add(91 * 24 * time.Hour) })
	res, err := later.Prune(f.ctx, f.tenant)
	if err != nil || res.LinkClicks != 3 {
		t.Fatalf("poda: %+v %v", res, err)
	}
	if again := f.links(campaign, 10); again.TotalClicks != 4 || again.Links[0].UniqueClicks != 2 {
		t.Fatalf("la poda no toca el agregado: %+v", again)
	}
}

func TestTopeDeEnlacesPorCampanaEnLaBase(t *testing.T) {
	f := newFixture(t)
	campaign := uuid.New()
	at := time.Now().Add(-time.Minute)
	extra := 2
	for i := 0; i < domain.MaxLinksPerCampaign+extra; i++ {
		f.ingest(f.click(uuid.New(), campaign, fmt.Sprintf("https://tienda.test/p/%d", i), at))
	}
	got := f.links(campaign, domain.MaxLinksLimit)
	if got.TotalLinks != domain.MaxLinksPerCampaign || got.TotalClicks != int64(domain.MaxLinksPerCampaign+extra) {
		t.Fatalf("tope: %d enlaces y %d clics", got.TotalLinks, got.TotalClicks)
	}
	var other *domain.LinkStats
	for i := range got.Links {
		if got.Links[i].URL == domain.OtherLinks {
			other = &got.Links[i]
		}
	}
	if other == nil || other.Clicks != int64(extra) || other.UniqueClicks != int64(extra) {
		t.Fatalf("las URL de mas van a otros: %+v", other)
	}
}
