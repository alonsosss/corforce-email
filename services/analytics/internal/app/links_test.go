package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/analytics/internal/domain"
	"github.com/google/uuid"
)

func (h *harness) click(msg, campaign uuid.UUID, link string) domain.MessageEvent {
	ev := h.event(msg, domain.MilestoneClicked, now.Add(-time.Hour))
	ev.Class = domain.ClassMarketing
	ev.CampaignID = &campaign
	ev.Link = link
	return ev
}

func (h *harness) links(campaign uuid.UUID) *domain.CampaignLinks {
	h.t.Helper()
	out, err := h.uc.CampaignLinks(context.Background(), h.tenant, campaign, domain.MaxLinksLimit)
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) linkStats(campaign uuid.UUID, url string) domain.LinkStats {
	for _, l := range h.links(campaign).Links {
		if l.URL == url {
			return l
		}
	}
	return domain.LinkStats{}
}

func TestClicsPorEnlaceTotalesYUnicos(t *testing.T) {
	h := newHarness(t)
	campaign := uuid.New()
	ana, eva := uuid.New(), uuid.New()
	const oferta, blog = "https://tienda.test/oferta", "https://tienda.test/blog"

	first := h.click(ana, campaign, oferta)
	if res := h.ingest(first); !res.Changed {
		t.Fatalf("el primer clic cambia algo: %+v", res)
	}
	if res := h.ingest(first); !res.Duplicate {
		t.Fatalf("la reentrega del mismo evento no suma: %+v", res)
	}
	if res := h.ingest(h.click(ana, campaign, oferta)); !res.Changed {
		t.Fatalf("el segundo clic de la misma persona suma en el enlace: %+v", res)
	}
	h.ingest(h.click(ana, campaign, blog))
	h.ingest(h.click(eva, campaign, oferta))

	if got := h.linkStats(campaign, oferta); got.Clicks != 3 || got.UniqueClicks != 2 {
		t.Fatalf("oferta: %+v", got)
	}
	if got := h.linkStats(campaign, blog); got.Clicks != 1 || got.UniqueClicks != 1 {
		t.Fatalf("blog: %+v", got)
	}
	all := h.links(campaign)
	if all.TotalLinks != 2 || all.TotalClicks != 4 {
		t.Fatalf("totales: %+v", all)
	}
	if got := h.totals(); got.ClickedUnique != 2 {
		t.Fatalf("clicked_unique sigue contando personas: %+v", got)
	}
}

func TestClicSinCampanaOSinEnlaceNoSeDesglosa(t *testing.T) {
	h := newHarness(t)
	campaign := uuid.New()
	noLink := h.click(uuid.New(), campaign, "")
	h.ingest(noLink)
	transactional := h.event(uuid.New(), domain.MilestoneClicked, now.Add(-time.Hour))
	transactional.Link = "https://tienda.test/pedido"
	h.ingest(transactional)
	opened := h.click(uuid.New(), campaign, "https://tienda.test/x")
	opened.Milestone = domain.MilestoneOpened
	h.ingest(opened)
	if got := h.links(campaign); got.TotalClicks != 0 || len(h.store.links) != 0 {
		t.Fatalf("nada que desglosar: %+v %v", got, h.store.links)
	}
}

func TestClicUsaLaCampanaDeLaFilaDelMensaje(t *testing.T) {
	h := newHarness(t)
	campaign := uuid.New()
	msg := uuid.New()
	sent := h.event(msg, domain.MilestoneSent, now.Add(-2*time.Hour))
	sent.Class = domain.ClassMarketing
	sent.CampaignID = &campaign
	h.ingest(sent)
	late := h.click(msg, uuid.New(), "https://tienda.test/o")
	h.ingest(late)
	if got := h.linkStats(campaign, "https://tienda.test/o"); got.Clicks != 1 {
		t.Fatalf("el clic cae en la campana fijada por el primer evento: %+v", h.store.links)
	}
}

func TestEnlacesPorEncimaDelTopeVanAOtros(t *testing.T) {
	h := newHarness(t)
	campaign := uuid.New()
	for i := 0; i < domain.MaxLinksPerCampaign+3; i++ {
		h.ingest(h.click(uuid.New(), campaign, fmt.Sprintf("https://tienda.test/p/%d", i)))
	}
	all := h.links(campaign)
	if all.TotalLinks != domain.MaxLinksPerCampaign || all.TotalClicks != int64(domain.MaxLinksPerCampaign+3) {
		t.Fatalf("tope: %d enlaces, %d clics", all.TotalLinks, all.TotalClicks)
	}
	if got := h.linkStats(campaign, domain.OtherLinks); got.Clicks != 3 || got.UniqueClicks != 3 {
		t.Fatalf("otros: %+v", got)
	}
}

func TestEnvioDePruebaNoDesglosaClics(t *testing.T) {
	h := newHarness(t)
	campaign := uuid.New()
	ev := h.click(uuid.New(), campaign, "https://tienda.test/o")
	ev.Test = true
	if res := h.ingest(ev); !res.Test {
		t.Fatalf("prueba: %+v", res)
	}
	if len(h.store.links) != 0 {
		t.Fatal("un envio de prueba no deja clics")
	}
}

func TestPodaOlvidaLosClicsConLaRetencion(t *testing.T) {
	h := newHarness(t)
	if _, err := h.uc.Prune(context.Background(), h.tenant); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-90 * 24 * time.Hour); !h.store.clicksBefore.Equal(want) {
		t.Fatalf("clics antes de %v, se esperaba %v", h.store.clicksBefore, want)
	}
}
