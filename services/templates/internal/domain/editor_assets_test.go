package domain

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeEditor(t *testing.T) {
	if e, err := NormalizeEditor(nil); e != nil || err != nil {
		t.Fatalf("sin editor: %v %v", e, err)
	}
	e, err := NormalizeEditor(&EditorDocument{Kind: EditorKindGrapesJSMJML, Project: json.RawMessage(" {\"pages\": [ 1 ] } "), MJML: "<mjml></mjml>"})
	if err != nil || string(e.Project) != `{"pages":[1]}` {
		t.Fatalf("el proyecto se guarda compacto: %v %s", err, e.Project)
	}
	cases := map[string]*EditorDocument{
		"tipo desconocido":   {Kind: "unlayer", Project: json.RawMessage(`{}`)},
		"proyecto ausente":   {Kind: EditorKindGrapesJSMJML},
		"proyecto no objeto": {Kind: EditorKindGrapesJSMJML, Project: json.RawMessage(`[1]`)},
		"demasiado grande": {Kind: EditorKindGrapesJSMJML, Project: json.RawMessage(`{}`),
			MJML: strings.Repeat("a", MaxEditorBytes)},
	}
	for name, doc := range cases {
		if _, err := NormalizeEditor(doc); !errors.Is(err, ErrInvalidEditor) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNormalizeBrandKit(t *testing.T) {
	logo := uuid.New()
	k, err := NormalizeBrandKit(BrandKit{
		LogoAssetID: &logo,
		Colors:      []string{" #0b5fff ", "#111827"},
		Fonts:       []string{"Inter", "Georgia"},
		Footer: BrandFooter{Company: " Acme SAC ", Address: "Av. Siempre Viva 742\nLima", Website: "https://acme.pe",
			SupportEmail: "hola@acme.pe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if k.Colors[0] != "#0B5FFF" || k.Footer.Company != "Acme SAC" || *k.LogoAssetID != logo {
		t.Fatalf("normalizacion: %+v", k)
	}
	manyColors := make([]string, MaxBrandColors+1)
	for i := range manyColors {
		manyColors[i] = "#00000" + string(rune('0'+i%10))
	}
	bad := map[string]BrandKit{
		"color sin forma":       {Colors: []string{"blue"}},
		"color repetido":        {Colors: []string{"#FFFFFF", "#ffffff"}},
		"demasiados colores":    {Colors: manyColors},
		"fuente fuera de lista": {Fonts: []string{"Comic Sans MS"}},
		"fuente repetida":       {Fonts: []string{"Arial", "Arial"}},
		"demasiadas fuentes":    {Fonts: []string{"Arial", "Helvetica", "Georgia", "Verdana", "Tahoma", "Lato", "Roboto"}},
		"web no absoluta":       {Footer: BrandFooter{Website: "acme.pe"}},
		"web javascript":        {Footer: BrandFooter{Website: "javascript:alert(1)"}},
		"correo invalido":       {Footer: BrandFooter{SupportEmail: "Acme <hola@acme.pe>"}},
		"html en el pie":        {Footer: BrandFooter{Company: "<b>Acme</b>"}},
		"control en el pie":     {Footer: BrandFooter{Address: "Lima\x00"}},
		"direccion larga":       {Footer: BrandFooter{Address: strings.Repeat("a", MaxFooterAddress+1)}},
	}
	for name, kit := range bad {
		if _, err := NormalizeBrandKit(kit); !errors.Is(err, ErrInvalidBrandKit) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLasTipografiasTienenAlternativa(t *testing.T) {
	for _, f := range BrandFonts() {
		if !strings.HasSuffix(f.Stack, "serif") && !strings.HasSuffix(f.Stack, "monospace") {
			t.Errorf("%s no termina en una familia generica: %q", f.Name, f.Stack)
		}
	}
}

func encoded(t *testing.T, format string, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.White, color.Black})
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	case "webp":
		// Cabecera VP8L (sin perdida): basta para DecodeConfig.
		bits := uint32(w-1) | uint32(h-1)<<14
		payload := append([]byte{0x2f}, binary.LittleEndian.AppendUint32(nil, bits)...)
		payload = append(payload, 0)
		chunk := append([]byte("VP8L"), binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))...)
		chunk = append(chunk, payload...)
		buf.WriteString("RIFF")
		buf.Write(binary.LittleEndian.AppendUint32(nil, uint32(4+len(chunk))))
		buf.WriteString("WEBP")
		buf.Write(chunk)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInspectImageReconoceLaFirma(t *testing.T) {
	for format, want := range map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"} {
		info, err := InspectImage(encoded(t, format, 120, 40))
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if info.ContentType != want || info.Width != 120 || info.Height != 40 {
			t.Fatalf("%s: %+v", format, info)
		}
	}
}

func TestInspectImageRechaza(t *testing.T) {
	pngData := encoded(t, "png", 10, 10)
	cases := map[string][]byte{
		"vacio":               nil,
		"svg":                 []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"html con extension":  []byte("<html><script>alert(1)</script></html>"),
		"cabecera truncada":   pngData[:12],
		"demasiado grande":    append(append([]byte(nil), pngData...), make([]byte, MaxAssetBytes)...),
		"demasiado ancha":     encoded(t, "png", MaxAssetDimension+1, 1),
		"webp demasiado alto": encoded(t, "webp", 1, MaxAssetDimension+1),
	}
	for name, data := range cases {
		if _, err := InspectImage(data); !errors.Is(err, ErrInvalidAsset) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNombreYClaveDeLaImagen(t *testing.T) {
	tenant := uuid.MustParse("8f1b4b1e-6c1e-4f55-9a0c-3d2e1f0a9b7c")
	if got := AssetObjectKey(tenant, "ab12", "png"); got != "public/8f1b4b1e-6c1e-4f55-9a0c-3d2e1f0a9b7c/templates/ab12.png" {
		t.Fatalf("clave: %s", got)
	}
	for in, want := range map[string]string{
		`C:\fotos\portada.png`:   "portada.png",
		"../../etc/passwd":       "passwd",
		"  ":                     "imagen.png",
		"logo\x00\x1b.png":       "logo.png",
		strings.Repeat("n", 300): strings.Repeat("n", MaxAssetNameLength),
	} {
		if got := NormalizeAssetName(in, "png"); got != want {
			t.Errorf("%q: %q, se esperaba %q", in, got, want)
		}
	}
}
