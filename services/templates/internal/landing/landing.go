// Package landing sanea el contenido de las paginas de aterrizaje y arma el documento que se
// sirve en su direccion publica.
//
// El HTML pasa por bluemonday con una lista blanca estricta: sin scripts, sin manejadores on*,
// sin formularios, sin marcos, sin objetos y con enlaces solo http, https, mailto y tel. El
// formulario de suscripcion no es HTML del usuario: es un marcador <div data-cf-form="clave">
// que al servir se sustituye por el iframe del formulario, solo si la clave es de la misma
// empresa. El CSS no se reescribe: lo que podria cargar o ejecutar algo se rechaza entero. La
// pagina se sirve ademas con una CSP que no ejecuta ningun script.
package landing

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/google/uuid"
	"github.com/microcosm-cc/bluemonday"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// FormsEmbedPath es donde sirve contacts el iframe de cada formulario, bajo PUBLIC_BASE_URL:
// <FormsEmbedPath>/<clave>/embed. Es el contrato de la ruta publica declarada en el gateway.
const FormsEmbedPath = "/api/v1/public/contacts/forms"

const (
	defaultFormHeight = 480
	minFormHeight     = 120
	maxFormHeight     = 2000
)

var (
	uuidPart     = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`
	formKeyValue = regexp.MustCompile(`^` + uuidPart + `\.` + uuidPart + `$`)
	heightValue  = regexp.MustCompile(`^[0-9]{2,4}$`)
	classValue   = regexp.MustCompile(`^[A-Za-z0-9_\- ]{1,300}$`)
	idValue      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\-]{0,99}$`)
	targetValue  = regexp.MustCompile(`^_blank$`)
)

var elements = []string{
	"a", "abbr", "address", "article", "aside", "b", "blockquote", "br", "cite", "code", "dd",
	"del", "div", "dl", "dt", "em", "figcaption", "figure", "footer", "h1", "h2", "h3", "h4",
	"h5", "h6", "header", "hr", "i", "img", "ins", "li", "main", "mark", "nav", "ol", "p", "pre",
	"q", "s", "section", "small", "span", "strong", "sub", "sup", "table", "tbody", "td", "tfoot",
	"th", "thead", "tr", "u", "ul",
}

// styleProperties se conservan en el atributo style con el validador de bluemonday, que no
// admite url() ni expresiones; quedan fuera las que cargan recursos (background, content...).
var styleProperties = []string{
	"color", "background-color", "font-family", "font-size", "font-style", "font-weight",
	"text-align", "text-decoration", "text-transform", "line-height", "letter-spacing",
	"vertical-align", "white-space", "margin", "margin-top", "margin-right", "margin-bottom",
	"margin-left", "padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"border", "border-top", "border-right", "border-bottom", "border-left", "border-color",
	"border-style", "border-width", "border-radius", "width", "height", "max-width",
	"min-width", "max-height", "min-height", "display", "float", "clear",
}

var policy = newPolicy()

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(elements...)
	p.AllowLists()
	p.AllowTables()
	p.AllowAttrs("id").Matching(idValue).Globally()
	p.AllowAttrs("class").Matching(classValue).Globally()
	p.AllowAttrs("title").Matching(bluemonday.Paragraph).Globally()
	p.AllowStyles(styleProperties...).Globally()
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("target").Matching(targetValue).OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto", "tel")
	p.RequireParseableURLs(true)
	p.RequireNoReferrerOnFullyQualifiedLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(false)
	p.AllowAttrs("src").OnElements("img")
	p.AllowDataURIImages()
	p.AllowAttrs("alt").Matching(bluemonday.Paragraph).OnElements("img")
	p.AllowAttrs("width", "height").Matching(bluemonday.NumberOrPercent).OnElements("img")
	p.AllowAttrs("colspan", "rowspan").Matching(bluemonday.Integer).OnElements("td", "th")
	p.AllowAttrs("data-cf-form").Matching(formKeyValue).OnElements("div")
	p.AllowAttrs("data-cf-height").Matching(heightValue).OnElements("div")
	return p
}

// SanitizeHTML devuelve el cuerpo de la pagina sin nada que ejecute, cargue un marco o envie
// datos. Lo no permitido se retira; el resultado es lo que se guarda y se sirve.
func SanitizeHTML(raw string) (string, error) {
	if len(raw) > domain.MaxPageHTMLBytes {
		return "", fmt.Errorf("%w: el HTML supera %d bytes", domain.ErrInvalidPage, domain.MaxPageHTMLBytes)
	}
	out := policy.Sanitize(raw)
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%w: la página no tiene contenido", domain.ErrInvalidPage)
	}
	return out, nil
}

var (
	cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssURL     = regexp.MustCompile(`(?i)url\(\s*([^)]*?)\s*\)`)
	dataImage  = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/=]+$`)
	// cssForbidden son construcciones que cargan otra hoja, ejecutan codigo en navegadores
	// antiguos o escapan del bloque <style>. Una barra invertida se rechaza entera: con escapes
	// CSS se escribe cualquiera de las otras sin que se lean.
	cssForbidden = []string{"\\", "<", "@import", "@namespace", "expression(", "javascript:", "vbscript:", "behavior", "-moz-binding"}
)

// CheckCSS rechaza una hoja que pueda cargar recursos no https, importar otras hojas o
// ejecutar algo. No la reescribe: lo que se guarda es exactamente lo que se envio.
func CheckCSS(css string) error {
	if len(css) > domain.MaxPageCSSBytes {
		return fmt.Errorf("%w: el CSS supera %d bytes", domain.ErrInvalidPage, domain.MaxPageCSSBytes)
	}
	plain := strings.ToLower(cssComment.ReplaceAllString(css, " "))
	for _, bad := range cssForbidden {
		if strings.Contains(plain, bad) {
			return fmt.Errorf("%w: el CSS no puede contener %q", domain.ErrInvalidPage, bad)
		}
	}
	for _, m := range cssURL.FindAllStringSubmatch(cssComment.ReplaceAllString(css, " "), -1) {
		target := strings.Trim(strings.TrimSpace(m[1]), `"'`)
		lower := strings.ToLower(target)
		if strings.HasPrefix(lower, "https://") || dataImage.MatchString(target) {
			continue
		}
		return fmt.Errorf("%w: url() del CSS solo admite https o imágenes data:", domain.ErrInvalidPage)
	}
	return nil
}

// DocumentInput es lo que hace falta para servir una version publicada.
type DocumentInput struct {
	TenantID    uuid.UUID
	Title       string
	Description string
	NoIndex     bool
	HTML        string
	CSS         string
	// PublicBaseURL es la base de la plataforma, sin barra final: de ella cuelga el iframe
	// de los formularios.
	PublicBaseURL string
}

// Document arma la pagina completa. El HTML ya esta saneado; aqui solo se sustituyen los
// marcadores de formulario de la misma empresa por su iframe y se retiran los de otra.
func Document(in DocumentInput) ([]byte, error) {
	body, err := embedForms(in.HTML, in.TenantID, in.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>" + html.EscapeString(in.Title) + "</title>\n")
	if in.Description != "" {
		b.WriteString("<meta name=\"description\" content=\"" + html.EscapeString(in.Description) + "\">\n")
	}
	if in.NoIndex {
		b.WriteString("<meta name=\"robots\" content=\"noindex, nofollow\">\n")
	}
	if strings.TrimSpace(in.CSS) != "" {
		b.WriteString("<style>\n" + in.CSS + "\n</style>\n")
	}
	b.WriteString("</head>\n<body>\n")
	b.WriteString(body)
	b.WriteString("\n</body>\n</html>\n")
	return b.Bytes(), nil
}

// CSP es la politica de la pagina publicada: ningun script, imagenes https o data:, estilos
// en linea, y como unico marco el formulario de la plataforma.
func CSP(platformOrigin string) string {
	frame := "'none'"
	if platformOrigin != "" {
		frame = platformOrigin
	}
	return "default-src 'none'; img-src https: data:; style-src 'unsafe-inline'; font-src https: data:; " +
		"frame-src " + frame + "; form-action 'none'; base-uri 'none'; frame-ancestors 'none'"
}

func embedForms(body string, tenantID uuid.UUID, base string) (string, error) {
	if !strings.Contains(body, "data-cf-form") {
		return body, nil
	}
	ctx := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := xhtml.ParseFragment(strings.NewReader(body), ctx)
	if err != nil {
		return "", err
	}
	root := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	replaceMarkers(root, tenantID.String()+".", base)
	var out bytes.Buffer
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := xhtml.Render(&out, c); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

func replaceMarkers(n *xhtml.Node, prefix, base string) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if key, height, ok := marker(c); ok {
			if strings.HasPrefix(key, prefix) && base != "" {
				n.InsertBefore(formIframe(key, height, base), c)
			}
			n.RemoveChild(c)
		} else {
			replaceMarkers(c, prefix, base)
		}
		c = next
	}
}

func marker(n *xhtml.Node) (key string, height int, ok bool) {
	if n.Type != xhtml.ElementNode || n.DataAtom != atom.Div {
		return "", 0, false
	}
	height = defaultFormHeight
	for _, a := range n.Attr {
		switch a.Key {
		case "data-cf-form":
			if formKeyValue.MatchString(a.Val) {
				key, ok = a.Val, true
			}
		case "data-cf-height":
			if v, err := strconv.Atoi(a.Val); err == nil && v >= minFormHeight && v <= maxFormHeight {
				height = v
			}
		}
	}
	return key, height, ok
}

func formIframe(key string, height int, base string) *xhtml.Node {
	return &xhtml.Node{Type: xhtml.ElementNode, Data: "iframe", DataAtom: atom.Iframe, Attr: []xhtml.Attribute{
		{Key: "src", Val: base + FormsEmbedPath + "/" + key + "/embed"},
		{Key: "title", Val: "Formulario de suscripción"},
		{Key: "loading", Val: "lazy"},
		{Key: "style", Val: "width:100%;border:0;height:" + strconv.Itoa(height) + "px"},
		{Key: "sandbox", Val: "allow-forms allow-same-origin allow-top-navigation-by-user-activation"},
	}}
}
