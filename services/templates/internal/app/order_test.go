package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/render"
	"github.com/google/uuid"
)

// Fases 3 a 5 de docs/Plan_Plantillas_de_Pedido.md con el motor de render real: clave estable,
// servidores de imagen del kit y tarjeta de pedido de Gmail.

type orderHarness struct {
	uc     *UseCase
	kits   *apptest.BrandKits
	tenant uuid.UUID
	user   uuid.UUID
}

func newOrderHarness() *orderHarness {
	h := &orderHarness{kits: apptest.NewBrandKits(), tenant: uuid.New(), user: uuid.New()}
	h.uc = New(Deps{
		Repo: apptest.NewRepo(), Tx: &apptest.Tx{}, Renderer: render.NewPort(), Events: &apptest.Events{},
		BrandKits: h.kits, PublicBaseURL: "https://mail.plataforma.example",
	})
	return h
}

func orderVariables() []domain.Variable {
	return []domain.Variable{
		{Name: "order_number", Type: domain.VarString, Required: true},
		{Name: "currency_code", Type: domain.VarString, Required: true},
		{Name: "total", Type: domain.VarNumber, Required: true},
		{Name: "order_url", Type: domain.VarURL},
		{Name: "status", Type: domain.VarString},
		{Name: "items", Type: domain.VarList, Required: true, Fields: []domain.Field{
			{Name: "name", Type: domain.VarString, Required: true},
			{Name: "quantity", Type: domain.VarNumber, Required: true},
			{Name: "unit_price", Type: domain.VarNumber, Required: true},
			{Name: "image_url", Type: domain.VarImage},
		}},
	}
}

func orderContent(markup string) domain.Content {
	return domain.Content{
		Subject:   "Pedido {{.order_number}}",
		HTML:      `<html><head><title>x</title></head><body>{{range .items}}<img src="{{.image_url}}" alt="{{.name}}">{{end}} {{.currency_code}} {{money .total}}</body></html>`,
		Variables: orderVariables(),
		Markup:    markup,
	}
}

func (h *orderHarness) publish(t *testing.T, kind, key string, c domain.Content) *domain.Template {
	t.Helper()
	ctx := context.Background()
	tpl, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{Name: "Pedido " + key, Key: key, Kind: kind, Content: c})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1); err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}
	return tpl
}

func orderValues(image string) map[string]json.RawMessage {
	items, _ := json.Marshal([]map[string]any{
		{"name": "Lavadora </script><script>alert(1)</script>", "quantity": 2, "unit_price": "949.5", "image_url": image},
	})
	return map[string]json.RawMessage{
		"order_number": json.RawMessage(`"A-100"`), "currency_code": json.RawMessage(`"PEN"`),
		"total": json.RawMessage(`"1899"`), "order_url": json.RawMessage(`"https://tienda.example/pedidos/A-100"`),
		"status": json.RawMessage(`"shipped"`), "items": items,
	}
}

func TestLaClaveNombraLaPlantillaYEsUnicaPorEmpresa(t *testing.T) {
	h := newOrderHarness()
	ctx := context.Background()
	tpl := h.publish(t, domain.KindTransactional, "pedido.confirmado", orderContent(""))
	got, err := h.uc.TemplateByKey(ctx, h.tenant, " pedido.confirmado ")
	if err != nil || got.ID != tpl.ID {
		t.Fatalf("por clave: %v %v", got, err)
	}
	if _, err := h.uc.TemplateByKey(ctx, uuid.New(), "pedido.confirmado"); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("la clave de otra empresa no se ve: %v", err)
	}
	if _, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{
		Name: "Otra", Key: "pedido.confirmado", Kind: domain.KindTransactional, Content: orderContent(""),
	}); !errors.Is(err, domain.ErrTemplateKeyTaken) {
		t.Errorf("clave repetida: %v", err)
	}
	for _, bad := range []string{"Pedido", "pedido..x", ".pedido", "pedido/x", "pedido x", strings.Repeat("a", 65)} {
		if _, err := h.uc.TemplateByKey(ctx, h.tenant, bad); !errors.Is(err, domain.ErrInvalidTemplateKey) {
			t.Errorf("%q: esperaba clave no valida, obtuve %v", bad, err)
		}
	}
	empty := ""
	updated, err := h.uc.UpdateTemplate(ctx, h.tenant, tpl.ID, UpdateTemplateInput{Key: &empty})
	if err != nil || updated.Key != nil {
		t.Fatalf("una clave vacia la quita: %+v %v", updated, err)
	}
	if _, err := h.uc.TemplateByKey(ctx, h.tenant, "pedido.confirmado"); !errors.Is(err, domain.ErrTemplateNotFound) {
		t.Errorf("la clave quitada ya no resuelve: %v", err)
	}
}

func TestLaTarjetaDePedidoSeAnadeSinPoderInyectarNada(t *testing.T) {
	h := newOrderHarness()
	h.kits.Kits[h.tenant] = domain.BrandKit{TenantID: h.tenant, Footer: domain.BrandFooter{Company: "Campovivo SAC"}}
	tpl := h.publish(t, domain.KindTransactional, "pedido.confirmado", orderContent(domain.MarkupOrder))
	out, err := h.uc.Render(context.Background(), h.tenant, tpl.ID, RenderInput{Values: orderValues("https://cdn.tienda.example/l.jpg")})
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(out.HTML, `<script type="application/ld+json">`)
	end := strings.Index(out.HTML, `</script></head>`)
	if start < 0 || end < start || strings.Count(out.HTML, "</script>") != 1 {
		t.Fatalf("el JSON-LD va una sola vez al final del head y ningun valor lo cierra: %s", out.HTML)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out.HTML[start+len(`<script type="application/ld+json">`):end]), &doc); err != nil {
		t.Fatalf("JSON-LD ilegible: %v", err)
	}
	offers := doc["acceptedOffer"].([]any)
	offer := offers[0].(map[string]any)
	if doc["@type"] != "Order" || doc["orderNumber"] != "A-100" || doc["price"] != "1899.00" || doc["priceCurrency"] != "PEN" ||
		doc["merchant"].(map[string]any)["name"] != "Campovivo SAC" || doc["orderStatus"] != "http://schema.org/OrderInTransit" ||
		offer["price"] != "949.50" || offer["eligibleQuantity"].(map[string]any)["value"] != "2" {
		t.Errorf("marcado inesperado: %v", doc)
	}
}

func TestSinDatosSuficientesElCorreoSaleSinTarjeta(t *testing.T) {
	h := newOrderHarness()
	tpl := h.publish(t, domain.KindTransactional, "pedido.confirmado", orderContent(domain.MarkupOrder))
	values := orderValues("")
	values["currency_code"] = json.RawMessage(`"S/"`)
	out, err := h.uc.Render(context.Background(), h.tenant, tpl.ID, RenderInput{
		Values: values, Reserved: map[string]string{domain.ReservedTenantName: "Campovivo"},
	})
	if err != nil {
		t.Fatalf("el marcado es una mejora, no una condicion del envio: %v", err)
	}
	if strings.Contains(out.HTML, "ld+json") {
		t.Errorf("con una moneda que no es ISO 4217 no debe salir marcado: %s", out.HTML)
	}
}

func TestElMarcadoExigeSusVariablesYUnaPlantillaTransaccional(t *testing.T) {
	h := newOrderHarness()
	ctx := context.Background()
	c := orderContent(domain.MarkupOrder)
	c.Variables = c.Variables[1:] // sin order_number
	c.HTML = `<p>{{.currency_code}} {{money .total}}{{range .items}}{{.name}}{{end}}</p>`
	c.Subject = "Pedido"
	if _, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{Name: "a", Kind: domain.KindTransactional, Content: c}); !errors.Is(err, domain.ErrInvalidMarkup) {
		t.Errorf("sin order_number: %v", err)
	}
	if _, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{Name: "b", Kind: domain.KindMarketing, Content: orderContent(domain.MarkupOrder)}); !errors.Is(err, domain.ErrInvalidMarkup) {
		t.Errorf("marketing: %v", err)
	}
	c = orderContent("factura")
	if _, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{Name: "c", Kind: domain.KindTransactional, Content: c}); !errors.Is(err, domain.ErrInvalidMarkup) {
		t.Errorf("marcado desconocido: %v", err)
	}
	tpl, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{Name: "promo", Kind: domain.KindMarketing, Content: orderContent("")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.CreateVersion(ctx, h.tenant, tpl.ID, h.user, orderContent(domain.MarkupOrder)); !errors.Is(err, domain.ErrInvalidMarkup) {
		t.Errorf("version con marcado sobre una plantilla de marketing: %v", err)
	}
}

func TestLasImagenesSalenSoloDeLosServidoresDelKit(t *testing.T) {
	h := newOrderHarness()
	ctx := context.Background()
	tpl := h.publish(t, domain.KindTransactional, "pedido.confirmado", orderContent(""))
	render := func(image string, test bool) error {
		in := RenderInput{Values: orderValues(image), Test: test}
		if test {
			one := 1
			in.Version = &one
		}
		_, err := h.uc.Render(ctx, h.tenant, tpl.ID, in)
		return err
	}
	if err := render("https://cualquiera.example/x.png", false); err != nil {
		t.Fatalf("sin lista en el kit se admite cualquier https: %v", err)
	}
	h.kits.Kits[h.tenant] = domain.BrandKit{TenantID: h.tenant, ImageHosts: []string{"tienda.example"}}
	for image, ok := range map[string]bool{
		"https://tienda.example/a.png":                         true,
		"https://cdn.tienda.example/a.png":                     true,
		"https://mail.plataforma.example/media/public/t/a.png": true,
		"https://otratienda.example/a.png":                     false,
		"https://tienda.example.atacante.example/a.png":        false,
		"": true,
	} {
		err := render(image, false)
		if ok && err != nil {
			t.Errorf("%q: %v", image, err)
		}
		if !ok && !errors.Is(err, domain.ErrInvalidVariables) {
			t.Errorf("%q: esperaba rechazo, obtuve %v", image, err)
		}
	}
	if err := render("https://otratienda.example/a.png", true); err != nil {
		t.Errorf("el envio de prueba no comprueba las imagenes de ejemplo: %v", err)
	}
}

func TestLosServidoresDeImagenDelKitSeValidan(t *testing.T) {
	good, err := domain.NormalizeBrandKit(domain.BrandKit{ImageHosts: []string{" CDN.Tienda.Example. ", "xn--tnda-1qa.pe"}})
	if err != nil || good.ImageHosts[0] != "cdn.tienda.example" {
		t.Fatalf("normalizacion: %v %v", good.ImageHosts, err)
	}
	for _, bad := range [][]string{
		{"https://tienda.example"}, {"tienda.example/img"}, {"tienda.example:443"}, {"localhost"},
		{"*.tienda.example"}, {"tienda.example", "TIENDA.example"}, make([]string, domain.MaxBrandImageHosts+1),
	} {
		if _, err := domain.NormalizeBrandKit(domain.BrandKit{ImageHosts: bad}); !errors.Is(err, domain.ErrInvalidBrandKit) {
			t.Errorf("%v: esperaba rechazo, obtuve %v", bad, err)
		}
	}
}
