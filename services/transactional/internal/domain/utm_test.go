package domain

import (
	"strings"
	"testing"
)

var utmOn = UTMSettings{Enabled: true, Source: "tienda-acme", Campaign: "otono-2026"}

const utmSuffix = "utm_source=tienda-acme&amp;utm_medium=email&amp;utm_campaign=otono-2026"

func tagger() *LinkTagger {
	return NewLinkTagger([]string{"app.example.com", "Partner.Test."})
}

func TestUTMValueNormaliza(t *testing.T) {
	cases := map[string]string{
		"  Otono 2026  ":                "otono-2026",
		"Campana de Otono / Rebajas!!":  "campana-de-otono-rebajas",
		"ÁRBOL ñandú":                   "arbol-nandu",
		"ya_normal.v2":                  "ya_normal.v2",
		"---":                           "",
		"":                              "",
		"a&b=c?d#e":                     "a-b-c-d-e",
		strings.Repeat("x", 150) + "-y": strings.Repeat("x", MaxUTMValueLen),
	}
	for in, want := range cases {
		if got := UTMValue(in); got != want {
			t.Errorf("UTMValue(%q) = %q, se esperaba %q", in, got, want)
		}
	}
}

func TestParseExcludedDomains(t *testing.T) {
	got, err := ParseExcludedDomains(" Tienda.test, ,partner.example. ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "tienda.test|partner.example" {
		t.Fatalf("dominios: %v", got)
	}
	for _, bad := range []string{"http://x.test", "a..b", "-x.test", "x y.test"} {
		if _, err := ParseExcludedDomains(bad); err == nil {
			t.Errorf("%q deberia rechazarse", bad)
		}
	}
	if got, err := ParseExcludedDomains(""); err != nil || len(got) != 0 {
		t.Fatalf("lista vacia: %v %v", got, err)
	}
}

func TestHostOf(t *testing.T) {
	if got := HostOf("https://App.Example.com:8443/base"); got != "app.example.com" {
		t.Fatalf("host: %q", got)
	}
	if got := HostOf("::no"); got != "" {
		t.Fatalf("host de una URL invalida: %q", got)
	}
}

func TestTagAnadeUTMAEnlacesHTTP(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"sin query", `<a href="https://tienda.test/oferta">x</a>`,
			`<a href="https://tienda.test/oferta?` + utmSuffix + `">x</a>`},
		{"con query escapada", `<a href="https://tienda.test/p?id=7&amp;ref=news">x</a>`,
			`<a href="https://tienda.test/p?id=7&amp;ref=news&amp;` + utmSuffix + `">x</a>`},
		{"ampersand crudo que parece entidad", `<a href="https://tienda.test/p?a=1&copy=2">x</a>`,
			`<a href="https://tienda.test/p?a=1&amp;copy=2&amp;` + utmSuffix + `">x</a>`},
		{"fragmento", `<a href="https://tienda.test/p?x=1#precio">x</a>`,
			`<a href="https://tienda.test/p?x=1&amp;` + utmSuffix + `#precio">x</a>`},
		{"fragmento sin query", `<a href="https://tienda.test/#arriba">x</a>`,
			`<a href="https://tienda.test/?` + utmSuffix + `#arriba">x</a>`},
		{"interrogacion final", `<a href="https://tienda.test/p?">x</a>`,
			`<a href="https://tienda.test/p?` + utmSuffix + `">x</a>`},
		{"mayusculas", `<A HREF='HTTPS://Tienda.TEST/Oferta'>x</A>`,
			`<A HREF='HTTPS://Tienda.TEST/Oferta?` + utmSuffix + `'>x</A>`},
		{"sin comillas", `<a class=boton href=https://tienda.test/o>x</a>`,
			`<a class=boton href="https://tienda.test/o?` + utmSuffix + `">x</a>`},
		{"espacios alrededor", `<a href = " https://tienda.test/o ">x</a>`,
			`<a href = "https://tienda.test/o?` + utmSuffix + `">x</a>`},
		{"area", `<map><area shape="rect" href="http://tienda.test/m"></map>`,
			`<map><area shape="rect" href="http://tienda.test/m?` + utmSuffix + `"></map>`},
		{"variable ya renderizada", `<a href="https://tienda.test/pedido/A-1029?cliente=ana%40example.com">x</a>`,
			`<a href="https://tienda.test/pedido/A-1029?cliente=ana%40example.com&amp;` + utmSuffix + `">x</a>`},
	}
	for _, c := range cases {
		if got := tagger().Tag(c.in, utmOn); got != c.want {
			t.Errorf("%s:\n obtenido %s\n esperado %s", c.name, got, c.want)
		}
	}
}

func TestTagRespetaLoQueNoDebeTocar(t *testing.T) {
	untouched := []string{
		`<a href="https://app.example.com/api/v1/public/transactional/unsubscribe?t=1&amp;m=2">baja</a>`,
		`<a href="https://app.example.com/ver?m=1">ver en el navegador</a>`,
		`<a href="https://sub.partner.test/x">socio</a>`,
		`<a href="mailto:ventas@tienda.test">correo</a>`,
		`<a href="tel:+5112345678">llamar</a>`,
		`<a href="#arriba">ancla</a>`,
		`<a href="/relativa">rel</a>`,
		`<a href="https://tienda.test/p?utm_source=otra">ya trae</a>`,
		`<a href="https://tienda.test/p?x=1&amp;UTM_Campaign=otra">ya trae</a>`,
		`<a href="https://tienda.test/p?utm%5Fsource=otra">ya trae escapado</a>`,
		`<a href="https://tienda.test/{{.enlace}}">sin renderizar</a>`,
		`<a href="javascript:alert(1)">js</a>`,
		`<a name="sin-href">x</a>`,
		`<link href="https://tienda.test/estilo.css" rel="stylesheet">`,
		`<!--[if mso]><v:roundrect href="https://tienda.test/o"></v:roundrect><![endif]-->`,
		`<style>a[href="https://tienda.test"]{color:red}</style>`,
		`<a href="https://[::1">rota</a>`,
	}
	for _, in := range untouched {
		if got := tagger().Tag(in, utmOn); got != in {
			t.Errorf("no debia cambiar:\n %s\n %s", in, got)
		}
	}
}

func TestTagConservaElDocumentoByteAByte(t *testing.T) {
	doc := "<!DOCTYPE html>\n<HTML><Body BGCOLOR=\"#fff\">\n<!--[if mso]><table><![endif]-->\n" +
		"<p>Precio &euro; 10 &amp; env&iacute;o</p>\n<IMG SRC=\"https://cdn.test/a.png\" ALT='A &amp; B'>\n" +
		"<a data-x=\"1\" href=\"https://tienda.test/o\" TARGET=_blank>Comprar</a>\n</Body></HTML>"
	got := tagger().Tag(doc, utmOn)
	want := strings.Replace(doc, `https://tienda.test/o"`, `https://tienda.test/o?`+utmSuffix+`"`, 1)
	if got != want {
		t.Fatalf("el resto del documento debe copiarse tal cual:\n%s\n---\n%s", got, want)
	}
}

func TestTagUsaSoloElPrimerHref(t *testing.T) {
	in := `<a href="https://tienda.test/a" href="https://tienda.test/b">x</a>`
	want := `<a href="https://tienda.test/a?` + utmSuffix + `" href="https://tienda.test/b">x</a>`
	if got := tagger().Tag(in, utmOn); got != want {
		t.Fatalf("obtenido %s", got)
	}
}

func TestTagConContenidoYDesactivado(t *testing.T) {
	in := `<a href="https://tienda.test/o">x</a>`
	withContent := utmOn
	withContent.Content = "boton-principal"
	if got := tagger().Tag(in, withContent); !strings.Contains(got, "&amp;utm_content=boton-principal") {
		t.Fatalf("utm_content: %s", got)
	}
	off := utmOn
	off.Enabled = false
	if got := tagger().Tag(in, off); got != in {
		t.Fatalf("desactivado no toca el HTML: %s", got)
	}
	if got := tagger().Tag(in, UTMSettings{Enabled: true, Campaign: "x"}); got != in {
		t.Fatalf("sin source no se etiqueta: %s", got)
	}
}

func TestTagDocumentoTruncadoONoHTML(t *testing.T) {
	for _, in := range []string{"", "texto sin enlaces", `<a href="https://tienda.test/o"`, `<a href="https://tienda.test/o`} {
		got := tagger().Tag(in, utmOn)
		if !strings.HasPrefix(got, strings.SplitN(in, "https", 2)[0]) {
			t.Errorf("entrada %q salio %q", in, got)
		}
	}
}
