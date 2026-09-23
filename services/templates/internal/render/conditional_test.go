package render

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

// Lo que genera MJML para Outlook de escritorio: tablas de respaldo en comentarios
// condicionales y un bloque visible para todos los demas.
const mjmlLike = `<!doctype html><html><head><!--[if mso]><noscript><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml></noscript><![endif]--><!--[if lte mso 11]><style type="text/css">.mj-outlook-group-fix { width:100% !important; }</style><![endif]--></head><body>` +
	`<!--[if mso | IE]><table align="center" border="0" cellpadding="0" cellspacing="0" style="width:600px;" width="600"><tr><td><![endif]-->` +
	`<div style="margin:0 auto;max-width:600px;"><p>Hola {{.name}}</p></div>` +
	`<!--[if mso | IE]></td></tr></table><![endif]-->` +
	`<!--[if !mso]><!--><p class="solo-otros">{{.name}}</p><!--<![endif]-->` +
	`<a href="{{.unsubscribe_url}}">Darse de baja</a></body></html>`

func TestLosComentariosCondicionalesDeOutlookSeConservan(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "s",
		HTML:      mjmlLike,
		Variables: vars(domain.Variable{Name: "name", Type: domain.VarString, Required: true}),
	})
	out := mustRender(t, c, map[string]json.RawMessage{"name": raw(`"<b>Ana</b>"`)}, nil)
	for _, want := range []string{
		`<!--[if mso]><noscript><xml><o:OfficeDocumentSettings>`,
		`<!--[if lte mso 11]><style type="text/css">.mj-outlook-group-fix { width:100% !important; }</style><![endif]-->`,
		`<!--[if mso | IE]><table align="center" border="0" cellpadding="0" cellspacing="0" style="width:600px;" width="600"><tr><td><![endif]-->`,
		`<!--[if mso | IE]></td></tr></table><![endif]-->`,
		`<!--[if !mso]><!-->`, `<!--<![endif]-->`,
	} {
		if !strings.Contains(out.HTML, want) {
			t.Errorf("falta %q en\n%s", want, out.HTML)
		}
	}
	if strings.Contains(out.HTML, conditionalPrefix) {
		t.Fatal("quedo una marca sin reponer")
	}
	if !strings.Contains(out.HTML, "&lt;b&gt;Ana&lt;/b&gt;") || strings.Contains(out.HTML, "<b>Ana</b>") {
		t.Fatal("el contenido fuera de los comentarios se sigue escapando")
	}
	if strings.Contains(out.Text, "mso") || strings.Contains(out.Text, "OfficeDocumentSettings") {
		t.Fatalf("el texto plano no lleva lo que solo ve Outlook: %q", out.Text)
	}
}

// El contenido de un bloque para Outlook no pasa por el escapador: una variable ahi saldria sin
// escapar, asi que se rechaza al compilar.
func TestUnComentarioCondicionalNoAdmiteVariables(t *testing.T) {
	_, err := New().Compile(domain.Content{
		Subject:   "s",
		HTML:      `<!--[if mso]><td>{{.name}}</td><![endif]-->`,
		Variables: vars(domain.Variable{Name: "name", Type: domain.VarString}),
	})
	if !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Fatalf("se esperaba ErrInvalidTemplate: %v", err)
	}
}

// Las construcciones prohibidas se buscan tambien dentro de los comentarios condicionales.
func TestUnComentarioCondicionalNoEscondeScript(t *testing.T) {
	_, err := New().Compile(domain.Content{Subject: "s", HTML: `<!--[if mso]><script>alert(1)</script><![endif]-->`})
	if !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Fatalf("se esperaba ErrInvalidTemplate: %v", err)
	}
}

// Un dato del contacto no puede adivinar la marca y hacer aparecer un comentario: es aleatoria.
func TestUnDatoNoPuedeReponerUnComentario(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "s",
		HTML:      `<!--[if mso]><table><tr><td><![endif]--><p>{{.name}}</p>`,
		Variables: vars(domain.Variable{Name: "name", Type: domain.VarString}),
	})
	guess := conditionalPrefix + "0000000000000000N0X"
	out := mustRender(t, c, map[string]json.RawMessage{"name": raw(`"` + guess + `"`)}, nil)
	if strings.Count(out.HTML, "<!--[if mso]>") != 1 || !strings.Contains(out.HTML, guess) {
		t.Fatalf("el dato se trata como texto: %s", out.HTML)
	}
}
