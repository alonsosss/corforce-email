package landing

import (
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/google/uuid"
)

func TestSanitizeHTMLRetiraLoQueEjecutaOCarga(t *testing.T) {
	in := `<section id="hero" class="hero grande"><h1 onclick="alert(1)">Hola</h1>
<script>alert(1)</script><iframe src="https://evil.test"></iframe>
<form action="https://evil.test"><input name="clave"></form>
<a href="javascript:alert(1)">malo</a><a href="https://acme.pe" target="_blank">bueno</a>
<a href="mailto:hola@acme.pe">correo</a><img src="https://cdn.acme.pe/a.png" alt="logo" onerror="x()">
<object data="x"></object><embed src="x"><style>body{}</style><base href="https://evil.test">
<div style="color:red;background:url(https://evil.test/x)">texto</div>
<svg><script>alert(1)</script></svg><meta http-equiv="refresh" content="0;url=https://evil.test"></section>`
	out, err := SanitizeHTML(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script", "onclick", "onerror", "<iframe", "<form", "<input", "javascript:", "<object",
		"<embed", "<style", "<base", "url(", "<svg", "<meta", "evil.test"} {
		if strings.Contains(strings.ToLower(out), bad) {
			t.Errorf("el saneado deja %q: %s", bad, out)
		}
	}
	for _, good := range []string{`id="hero"`, `class="hero grande"`, "<h1>Hola</h1>", `href="https://acme.pe"`,
		`href="mailto:hola@acme.pe"`, `src="https://cdn.acme.pe/a.png"`, `alt="logo"`, "color: red"} {
		if !strings.Contains(out, good) {
			t.Errorf("el saneado retira %q: %s", good, out)
		}
	}
	if !strings.Contains(out, `rel="noreferrer`) {
		t.Errorf("los enlaces externos no llevan noreferrer: %s", out)
	}
}

func TestSanitizeHTMLConservaElMarcadorDeFormulario(t *testing.T) {
	key := uuid.NewString() + "." + uuid.NewString()
	out, err := SanitizeHTML(`<div data-cf-form="` + key + `" data-cf-height="600">Formulario</div><div data-cf-form="../x">y</div>`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `data-cf-form="`+key+`"`) || !strings.Contains(out, `data-cf-height="600"`) {
		t.Fatalf("marcador retirado: %s", out)
	}
	if strings.Contains(out, "../x") {
		t.Fatalf("un marcador mal formado sobrevive: %s", out)
	}
}

func TestSanitizeHTMLTopesYVacio(t *testing.T) {
	if _, err := SanitizeHTML(strings.Repeat("a", domain.MaxPageHTMLBytes+1)); !errors.Is(err, domain.ErrInvalidPage) {
		t.Fatal("HTML por encima del tope")
	}
	if _, err := SanitizeHTML(`<script>alert(1)</script>`); !errors.Is(err, domain.ErrInvalidPage) {
		t.Fatal("una pagina que solo tenia scripts queda vacia y se rechaza")
	}
}

func TestCheckCSS(t *testing.T) {
	good := `.hero{background-image:url("https://cdn.acme.pe/f.png");color:#111}/* url(http://x) */
@media (max-width:600px){.hero{padding:8px}} .i{background:url(data:image/png;base64,iVBORw0KGgo=)}`
	if err := CheckCSS(good); err != nil {
		t.Fatalf("CSS valido rechazado: %v", err)
	}
	for _, bad := range []string{
		`@import url("https://evil.test/x.css");`,
		`.a{background:url(http://evil.test/x.png)}`,
		`.a{background:url(//evil.test/x.png)}`,
		`.a{width:expression(alert(1))}`,
		`.a{background:url(javascript:alert(1))}`,
		`.a{behavior:url(x.htc)}`,
		`.a{-moz-binding:url(x)}`,
		`.a{content:"\3c/style\3e"}`,
		`</style><script>alert(1)</script>`,
		`.a{background:url(data:text/html;base64,PHNjcmlwdD4=)}`,
		`@IMPORT "x.css";`,
	} {
		if err := CheckCSS(bad); !errors.Is(err, domain.ErrInvalidPage) {
			t.Errorf("CSS aceptado: %s", bad)
		}
	}
	if err := CheckCSS(strings.Repeat("a", domain.MaxPageCSSBytes+1)); !errors.Is(err, domain.ErrInvalidPage) {
		t.Fatal("CSS por encima del tope")
	}
}

func TestDocumentSustituyeSoloLosFormulariosDeLaEmpresa(t *testing.T) {
	tenant := uuid.New()
	own := tenant.String() + "." + uuid.NewString()
	foreign := uuid.NewString() + "." + uuid.NewString()
	body, _ := SanitizeHTML(`<p>Hola</p><section><div data-cf-form="` + own + `" data-cf-height="600">x</div></section><div data-cf-form="` + foreign + `">y</div>`)
	doc, err := Document(DocumentInput{
		TenantID: tenant, Title: `Oferta <"&>`, Description: "Descuento", NoIndex: true, HTML: body,
		CSS: ".a{color:red}", PublicBaseURL: "https://app.plataforma.io",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(doc)
	if !strings.Contains(s, `<iframe src="https://app.plataforma.io/api/v1/public/contacts/forms/`+own+`/embed"`) {
		t.Fatalf("el formulario de la empresa no se incrusta: %s", s)
	}
	if strings.Contains(s, foreign) {
		t.Fatalf("se incrusta el formulario de otra empresa: %s", s)
	}
	if !strings.Contains(s, "height:600px") || !strings.Contains(s, `sandbox="allow-forms allow-same-origin allow-top-navigation-by-user-activation"`) {
		t.Fatalf("iframe: %s", s)
	}
	if strings.Contains(s, "allow-scripts") {
		t.Fatal("el iframe permite scripts")
	}
	for _, want := range []string{"<title>Oferta &lt;&#34;&amp;&gt;</title>", `<meta name="robots" content="noindex, nofollow">`,
		`<meta name="description" content="Descuento">`, "<style>\n.a{color:red}\n</style>", "<p>Hola</p>"} {
		if !strings.Contains(s, want) {
			t.Errorf("el documento no lleva %q", want)
		}
	}
	doc, _ = Document(DocumentInput{TenantID: tenant, Title: "T", HTML: "<p>x</p>", PublicBaseURL: "https://app.plataforma.io"})
	if strings.Contains(string(doc), "noindex") {
		t.Fatal("noindex sin pedirlo")
	}
}

func TestCSP(t *testing.T) {
	csp := CSP("https://app.plataforma.io")
	for _, want := range []string{"default-src 'none'", "frame-src https://app.plataforma.io", "form-action 'none'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("la CSP no lleva %q", want)
		}
	}
	if strings.Contains(csp, "script-src") || strings.Contains(csp, "unsafe-eval") {
		t.Fatal("la pagina publica no ejecuta scripts")
	}
	if !strings.Contains(CSP(""), "frame-src 'none'") {
		t.Fatal("sin plataforma no hay marcos")
	}
}
