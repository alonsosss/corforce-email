package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/templates/internal/deliverability"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/google/uuid"
)

type editorHarness struct {
	*harness
	kits    *apptest.BrandKits
	assets  *apptest.Assets
	store   *apptest.Store
	scanner *apptest.Scanner
	spam    *apptest.Spam
}

func newEditorHarness() *editorHarness {
	h := &editorHarness{
		harness: newHarness(), kits: apptest.NewBrandKits(), assets: apptest.NewAssets(),
		store: apptest.NewStore(), scanner: &apptest.Scanner{}, spam: &apptest.Spam{Err: errors.New("sin mail-security")},
	}
	h.uc = New(Deps{
		Repo: h.repo, Tx: h.tx, Renderer: h.renderer, Events: h.events,
		BrandKits: h.kits, Assets: h.assets, Store: h.store, Scanner: h.scanner, Spam: h.spam,
	})
	return h
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	testAddress   = "Av. Siempre Viva 742, Lima"
	marketingHTML = `<div style="display:none">Novedades</div><p>{{.name}}</p><p>` + testAddress +
		`</p><a href="{{.unsubscribe_url}}">Baja</a>`
)

func TestLaVersionGuardaElDocumentoDelEditor(t *testing.T) {
	h := newEditorHarness()
	tpl := h.create(t, "Bienvenida")
	c := content("<p>{{.name}}</p>")
	c.Editor = &domain.EditorDocument{Kind: domain.EditorKindGrapesJSMJML, Project: json.RawMessage(`{ "pages": [] }`), MJML: "<mjml/>"}
	v, err := h.uc.CreateVersion(context.Background(), h.tenant, tpl.ID, h.user, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.uc.GetVersion(context.Background(), h.tenant, tpl.ID, v.Version)
	if err != nil || got.Editor == nil || string(got.Editor.Project) != `{"pages":[]}` || got.Editor.MJML != "<mjml/>" {
		t.Fatalf("editor guardado: %+v %v", got, err)
	}
	c.Editor = &domain.EditorDocument{Kind: "otro", Project: json.RawMessage(`{}`)}
	if _, err := h.uc.CreateVersion(context.Background(), h.tenant, tpl.ID, h.user, c); !errors.Is(err, domain.ErrInvalidEditor) {
		t.Fatalf("editor invalido: %v", err)
	}
}

func TestPublicarMarketingExigeLaVerificacion(t *testing.T) {
	h := newEditorHarness()
	ctx := context.Background()
	c := content(marketingHTML)
	tpl, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{Name: "Promo", Kind: domain.KindMarketing, Content: c})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1)
	de, ok := IsDeliverabilityError(err)
	if !ok || !errors.Is(err, domain.ErrDeliverabilityFailed) {
		t.Fatalf("sin direccion en el kit no se publica: %v", err)
	}
	if len(de.Report.Errors()) != 1 || de.Report.Errors()[0].Code != deliverability.CodeMissingPhysicalAddress {
		t.Fatalf("incidencias: %+v", de.Report.Issues)
	}
	if len(h.events.Published) != 0 {
		t.Fatal("un rechazo no publica evento")
	}

	h.kits.Kits[h.tenant] = domain.BrandKit{TenantID: h.tenant, Footer: domain.BrandFooter{Address: testAddress}}
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1); err != nil {
		t.Fatalf("con la direccion se publica: %v", err)
	}
	if h.spam.Last.UnsubscribeURL != sampleUnsubscribeURL || !h.spam.Last.Marketing ||
		!strings.Contains(h.spam.Last.HTML, sampleUnsubscribeURL) {
		t.Fatalf("muestra antispam: %+v", h.spam.Last)
	}

	// Transaccional: la baja y la direccion no aplican.
	trx := h.create(t, "Codigo")
	if _, err := h.uc.PublishVersion(ctx, h.tenant, trx.ID, 1); err != nil {
		t.Fatalf("transaccional: %v", err)
	}
}

func TestLaPuntuacionDeRechazoBloqueaLaPublicacion(t *testing.T) {
	h := newEditorHarness()
	ctx := context.Background()
	h.kits.Kits[h.tenant] = domain.BrandKit{TenantID: h.tenant, Footer: domain.BrandFooter{Address: testAddress}}
	h.spam.Err, h.spam.Result = nil, deliverability.Spam{Available: true, Score: 20, Required: 15, Action: "reject"}
	tpl, _, err := h.uc.CreateTemplate(ctx, h.tenant, h.user, CreateTemplateInput{Name: "Promo", Kind: domain.KindMarketing, Content: content(marketingHTML)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.PublishVersion(ctx, h.tenant, tpl.ID, 1); !errors.Is(err, domain.ErrDeliverabilityFailed) {
		t.Fatalf("rechazo del antispam: %v", err)
	}
	report, err := h.uc.CheckVersion(ctx, h.tenant, tpl.ID, 1)
	if err != nil || !report.Spam.Available || report.Passed {
		t.Fatalf("verificacion de la version: %+v %v", report, err)
	}
}

func TestCheckContentAdmiteVariablesSinDeclarar(t *testing.T) {
	h := newEditorHarness()
	report, err := h.uc.CheckContent(context.Background(), h.tenant, CheckInput{
		Kind: domain.KindTransactional, Subject: "Hola", HTML: "<p>{{.first_name}}</p>",
		Values: map[string]json.RawMessage{"name": json.RawMessage(`"Ana"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Spam.Available {
		t.Fatal("sin mail-security la verificacion sigue sin puntuacion")
	}
	if _, err := h.uc.CheckContent(context.Background(), h.tenant, CheckInput{Kind: "newsletter", Subject: "x", HTML: "x"}); !errors.Is(err, domain.ErrInvalidKind) {
		t.Fatalf("tipo invalido: %v", err)
	}
	if _, err := h.uc.CheckContent(context.Background(), h.tenant, CheckInput{Kind: domain.KindMarketing, Subject: "x", HTML: "INVALID"}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Fatalf("plantilla invalida: %v", err)
	}
}

func TestSampleValuesCompletaLoQueFalta(t *testing.T) {
	declared := []domain.Variable{
		{Name: "a", Type: domain.VarString, Required: true},
		{Name: "b", Type: domain.VarURL, Required: true},
		{Name: "c", Type: domain.VarNumber, Default: json.RawMessage(`3`)},
		{Name: "d", Type: domain.VarEmail},
		{Name: "e", Type: domain.VarImage},
		{Name: "items", Type: domain.VarList, Fields: []domain.Field{
			{Name: "name", Type: domain.VarString, Required: true},
			{Name: "price", Type: domain.VarNumber},
			{Name: "image_url", Type: domain.VarImage},
		}},
	}
	got := sampleValues(declared, map[string]json.RawMessage{"a": json.RawMessage(`"Ana"`), "b": json.RawMessage(`null`)},
		func(name string) int { return 20 })
	values, err := domain.ResolveValues(declared, got, nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["a"] != "Ana" || values["b"] != sampleURL || values["c"] != json.Number("3") ||
		values["d"] != sampleRecipientEmail || values["e"] != sampleImageURL {
		t.Fatalf("valores: %v", values)
	}
	items, ok := values["items"].([]map[string]any)
	if !ok || len(items) != 20 || items[0]["name"] != sampleString || items[0]["image_url"] != sampleImageURL {
		t.Fatalf("la lista de ejemplo debe llevar los elementos pedidos con valores de su tipo: %v", values["items"])
	}
}

func TestSubirImagen(t *testing.T) {
	h := newEditorHarness()
	ctx := context.Background()
	data := pngBytes(t, 64, 32)

	view, created, err := h.uc.UploadAsset(ctx, h.tenant, h.user, "portada.png", data)
	if err != nil || !created {
		t.Fatalf("subida: %v %v", created, err)
	}
	a := view.Asset
	if a.ContentType != "image/png" || a.Width != 64 || a.Height != 32 || len(a.SHA256) != 64 ||
		a.ObjectKey != domain.AssetObjectKey(h.tenant, a.SHA256, "png") || view.URL != h.store.PublicURL(a.ObjectKey) {
		t.Fatalf("imagen: %+v %s", a, view.URL)
	}
	if !bytes.Equal(h.store.Objects[a.ObjectKey], data) || h.scanner.Calls != 1 {
		t.Fatal("se guarda tras analizarla")
	}

	again, created, err := h.uc.UploadAsset(ctx, h.tenant, h.user, "copia.png", data)
	if err != nil || created || again.Asset.ID != a.ID {
		t.Fatalf("la misma imagen no se guarda dos veces: %v %v", created, err)
	}

	if err := h.uc.DeleteAsset(ctx, h.tenant, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.uc.DeleteAsset(ctx, h.tenant, a.ID); !errors.Is(err, domain.ErrAssetNotFound) {
		t.Fatalf("borrar dos veces: %v", err)
	}
	restored, created, err := h.uc.UploadAsset(ctx, h.tenant, h.user, "vuelta.png", data)
	if err != nil || !created || restored.Asset.ID != a.ID || restored.Asset.Name != "vuelta.png" {
		t.Fatalf("resubir una retirada la reactiva: %+v %v %v", restored, created, err)
	}

	if _, _, err := h.uc.UploadAsset(ctx, h.tenant, h.user, "x.svg", []byte("<svg/>")); !errors.Is(err, domain.ErrInvalidAsset) {
		t.Fatalf("svg: %v", err)
	}
	h.scanner.Err = domain.ErrAssetRejected
	if _, _, err := h.uc.UploadAsset(ctx, h.tenant, h.user, "eicar.png", pngBytes(t, 3, 3)); !errors.Is(err, domain.ErrAssetRejected) {
		t.Fatalf("infectada: %v", err)
	}
	if len(h.store.Objects) != 1 {
		t.Fatal("una imagen rechazada no llega al almacen")
	}
}

func TestSubidasSinAlmacenOSinAntivirus(t *testing.T) {
	h := newEditorHarness()
	h.uc.scanner = nil
	if _, _, err := h.uc.UploadAsset(context.Background(), h.tenant, h.user, "a.png", pngBytes(t, 1, 1)); !errors.Is(err, domain.ErrScannerUnavailable) {
		t.Fatalf("sin ClamAV: %v", err)
	}
	h.uc.store = nil
	if err := h.uc.AssetUploadsAvailable(); !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatalf("sin almacen: %v", err)
	}
}

func TestListarImagenesPorPaginas(t *testing.T) {
	h := newEditorHarness()
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		if _, _, err := h.uc.UploadAsset(ctx, h.tenant, h.user, "img.png", pngBytes(t, i, 1)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for page := 0; page < 3; page++ {
		items, next, err := h.uc.ListAssets(ctx, h.tenant, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range items {
			if seen[v.Asset.ID] {
				t.Fatal("una imagen repetida entre paginas")
			}
			seen[v.Asset.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Fatalf("se recorrieron %d imagenes de 5", len(seen))
	}
	if _, _, err := h.uc.ListAssets(ctx, h.tenant, 2, "no-es-un-cursor"); !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("cursor invalido: %v", err)
	}
	if items, _, _ := h.uc.ListAssets(ctx, uuid.New(), 10, ""); len(items) != 0 {
		t.Fatal("otra empresa no ve las imagenes")
	}
}

func TestKitDeMarca(t *testing.T) {
	h := newEditorHarness()
	ctx := context.Background()
	view, err := h.uc.GetBrandKit(ctx, h.tenant)
	if err != nil || view.Kit.UpdatedAt != nil || len(view.Kit.Colors) != 0 {
		t.Fatalf("sin kit: %+v %v", view, err)
	}
	logo, _, err := h.uc.UploadAsset(ctx, h.tenant, h.user, "logo.png", pngBytes(t, 10, 10))
	if err != nil {
		t.Fatal(err)
	}
	id := logo.Asset.ID
	view, err = h.uc.UpdateBrandKit(ctx, h.tenant, h.user, domain.BrandKit{LogoAssetID: &id, Colors: []string{"#0b5fff"}, Fonts: []string{"Inter"}})
	if err != nil || view.Kit.UpdatedAt == nil || view.LogoURL != logo.URL || view.Kit.Colors[0] != "#0B5FFF" {
		t.Fatalf("kit guardado: %+v %v", view, err)
	}
	other := uuid.New()
	if _, err := h.uc.UpdateBrandKit(ctx, h.tenant, h.user, domain.BrandKit{LogoAssetID: &other}); !errors.Is(err, domain.ErrInvalidBrandKit) {
		t.Fatalf("logo de otra empresa: %v", err)
	}
	if _, err := h.uc.UpdateBrandKit(ctx, h.tenant, uuid.Nil, domain.BrandKit{}); !errors.Is(err, domain.ErrMissingCreator) {
		t.Fatalf("sin usuario: %v", err)
	}
}
