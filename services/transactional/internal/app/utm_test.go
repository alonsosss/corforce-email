package app

import (
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
)

func batchHTML(t *testing.T, f *fixture, cmd MarketingBatchCommand) (string, domain.Message) {
	t.Helper()
	res, err := f.uc.CreateMarketingBatch(ctx, cmd)
	if err != nil || len(res.MessageIDs) == 0 {
		t.Fatalf("lote: %+v, %v", res, err)
	}
	msg := f.repo.messages[res.MessageIDs[0]]
	if msg.HTML == nil {
		t.Fatal("el mensaje debe llevar HTML")
	}
	return *msg.HTML, msg
}

func TestMarketingBatchAddsUTMWithDefaults(t *testing.T) {
	f := newMarketingFixture(t)
	f.tpl.extraHTML = `<a href="https://tienda.test/oferta?id=9#top">Ver</a><a href="mailto:x@tienda.test">m</a>`
	cmd := batchCommand(f, "ana@example.com")
	cmd.From.Name = "Tienda Ñandú"
	html, msg := batchHTML(t, f, cmd)
	want := `https://tienda.test/oferta?id=9&amp;utm_source=tienda-nandu&amp;utm_medium=email&amp;utm_campaign=` +
		cmd.CampaignID.String() + `#top`
	if !strings.Contains(html, want) {
		t.Fatalf("UTM por defecto: %s", html)
	}
	if !strings.Contains(html, `href="mailto:x@tienda.test"`) || strings.Count(html, "utm_") != 3 {
		t.Fatalf("solo el enlace http lleva UTM: %s", html)
	}
	if !f.links.ContainsUnsubscribeLink(html, msg.ID) || strings.Contains(html, "utm_source=tienda-nandu\">Darse") {
		t.Fatalf("la baja queda intacta: %s", html)
	}
}

func TestMarketingBatchUTMFromBatchObject(t *testing.T) {
	f := newMarketingFixture(t)
	f.tpl.extraHTML = `<a href="https://tienda.test/o">Ver</a>`
	cmd := batchCommand(f, "ana@example.com")
	cmd.UTM = &UTMInput{Source: "Boletin", Campaign: "Otono 2026", Content: "Cabecera"}
	html, _ := batchHTML(t, f, cmd)
	if !strings.Contains(html, `https://tienda.test/o?utm_source=boletin&amp;utm_medium=email&amp;utm_campaign=otono-2026&amp;utm_content=cabecera"`) {
		t.Fatalf("UTM del lote: %s", html)
	}
}

func TestMarketingBatchUTMSourceFallsBackToSenderDomain(t *testing.T) {
	f := newMarketingFixture(t)
	f.tpl.extraHTML = `<a href="https://tienda.test/o">Ver</a>`
	cmd := batchCommand(f, "ana@example.com")
	cmd.From.Name = ""
	html, _ := batchHTML(t, f, cmd)
	if !strings.Contains(html, "utm_source="+domain.UTMValue(shopDomain)+"&amp;") {
		t.Fatalf("source del dominio del remitente: %s", html)
	}
}

func TestMarketingBatchUTMDisabled(t *testing.T) {
	f := newMarketingFixture(t)
	f.tpl.extraHTML = `<a href="https://tienda.test/o">Ver</a>`
	cmd := batchCommand(f, "ana@example.com")
	off := false
	cmd.UTM = &UTMInput{Enabled: &off, Source: "ignorado"}
	if html, _ := batchHTML(t, f, cmd); strings.Contains(html, "utm_") {
		t.Fatalf("desactivado no anade UTM: %s", html)
	}
}

func TestMarketingBatchWithoutTaggerKeepsHTML(t *testing.T) {
	f := newMarketingFixture(t)
	f.deps.UTM = nil
	f.uc = New(f.deps)
	f.tpl.extraHTML = `<a href="https://tienda.test/o">Ver</a>`
	if html, _ := batchHTML(t, f, batchCommand(f, "ana@example.com")); strings.Contains(html, "utm_") {
		t.Fatalf("sin etiquetador no se anade nada: %s", html)
	}
}

func TestMarketingBatchUTMValidation(t *testing.T) {
	for name, utm := range map[string]*UTMInput{
		"source sin caracteres validos": {Source: "!!!"},
		"campaign sin caracteres":       {Campaign: " / "},
		"content enorme":                {Content: strings.Repeat("c", domain.MaxUTMInputLen+1)},
	} {
		f := newMarketingFixture(t)
		cmd := batchCommand(f, "ana@example.com")
		cmd.UTM = utm
		if _, err := f.uc.CreateMarketingBatch(ctx, cmd); !domain.IsValidation(err) {
			t.Errorf("%s: se esperaba validacion, err = %v", name, err)
		}
		nothingCreated(t, f, name)
	}
}
