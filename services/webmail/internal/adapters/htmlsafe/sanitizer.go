// Package htmlsafe sanea el HTML de los mensajes.
//
// Dos pasadas: bluemonday con una lista blanca estricta (sin scripts, sin manejadores
// on*, sin formularios, sin iframes, sin estilos que carguen recursos) y despues una
// propia sobre su salida, que decide que hacer con cada imagen (cid: a la URL del
// servicio, remotas bloqueadas salvo permiso) y deja en los enlaces solo http, https y
// mailto. La segunda pasada trabaja sobre HTML ya saneado y bien formado.
package htmlsafe

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// Sanitizer implementa ports.HTMLSanitizer. Es seguro para uso concurrente: la politica
// de bluemonday no se modifica despues de construirse.
type Sanitizer struct {
	policy *bluemonday.Policy
}

func New() *Sanitizer { return &Sanitizer{policy: newPolicy()} }

var (
	colorValue    = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{1,30}|rgba?\(\s*[0-9.%\s,]{1,40}\))$`)
	integerValue  = regexp.MustCompile(`^[0-9]{1,4}$`)
	fontFaceValue = regexp.MustCompile(`^[A-Za-z0-9 ,'\-]{1,200}$`)
	fontSizeValue = regexp.MustCompile(`^[+-]?[1-7]$`)
	alignValue    = regexp.MustCompile(`(?i)^(left|right|center|justify)$`)
	valignValue   = regexp.MustCompile(`(?i)^(top|middle|bottom|baseline)$`)
	dirValue      = regexp.MustCompile(`(?i)^(ltr|rtl|auto)$`)
	langValue     = regexp.MustCompile(`^[A-Za-z]{1,8}(-[A-Za-z0-9]{1,8})*$`)
)

// formatElements son los elementos de texto y maquetacion que no cargan ni ejecutan nada.
var formatElements = []string{
	"abbr", "acronym", "address", "article", "aside", "b", "bdi", "bdo", "big", "blockquote",
	"br", "center", "cite", "code", "dd", "del", "details", "dfn", "div", "dl", "dt", "em",
	"figcaption", "figure", "font", "footer", "h1", "h2", "h3", "h4", "h5", "h6", "header",
	"hr", "i", "ins", "kbd", "li", "main", "mark", "ol", "p", "pre", "q", "rp", "rt", "ruby",
	"s", "samp", "section", "small", "span", "strike", "strong", "sub", "summary", "sup",
	"table", "tbody", "td", "tfoot", "th", "thead", "time", "tr", "tt", "u", "ul", "var", "wbr",
}

// styleProperties son las propiedades CSS que se conservan en el atributo style, cada una
// con el validador por defecto de bluemonday, que no admite url() ni expresiones. Quedan
// fuera las que cargan recursos o superponen contenido: background, background-image,
// list-style, border-image, content, cursor, position, z-index, filter y behavior.
var styleProperties = []string{
	"color", "background-color",
	"font-family", "font-size", "font-style", "font-weight", "font-variant",
	"text-align", "text-decoration", "text-transform", "text-indent",
	"line-height", "letter-spacing", "word-spacing", "white-space", "vertical-align",
	"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
	"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"border", "border-top", "border-right", "border-bottom", "border-left",
	"border-color", "border-style", "border-width", "border-radius",
	"border-collapse", "border-spacing",
	"width", "height", "max-width", "min-width", "max-height", "min-height",
	"display", "float", "clear", "direction", "list-style-type", "table-layout", "overflow",
}

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(formatElements...)
	p.AllowLists()
	p.AllowTables()

	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("src").OnElements("img")
	p.AllowAttrs("alt").Matching(bluemonday.Paragraph).OnElements("img")
	p.AllowAttrs("width", "height").Matching(bluemonday.NumberOrPercent).OnElements("img")

	p.AllowAttrs("title").Matching(bluemonday.Paragraph).Globally()
	p.AllowAttrs("dir").Matching(dirValue).Globally()
	p.AllowAttrs("lang").Matching(langValue).Globally()
	p.AllowAttrs("align").Matching(alignValue).OnElements("p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "table", "caption", "img")
	p.AllowAttrs("valign").Matching(valignValue).OnElements("td", "th", "tr")
	p.AllowAttrs("bgcolor").Matching(colorValue).OnElements("table", "tr", "td", "th")
	p.AllowAttrs("color").Matching(colorValue).OnElements("font")
	p.AllowAttrs("face").Matching(fontFaceValue).OnElements("font")
	p.AllowAttrs("size").Matching(fontSizeValue).OnElements("font")
	p.AllowAttrs("border", "cellpadding", "cellspacing").Matching(integerValue).OnElements("table")
	p.AllowStyles(styleProperties...).Globally()

	// URLs absolutas y parseables; los esquemas se afinan en la segunda pasada segun el
	// elemento (una imagen puede ser cid: o data:, un enlace no).
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(false)
	p.AllowURLSchemes("http", "https", "mailto", "cid")
	p.AllowDataURIImages()
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)

	// Elementos cuyo CONTENIDO tampoco se muestra: no es texto del mensaje.
	p.SkipElementsContent("head", "title", "style", "script", "noscript", "template",
		"object", "embed", "applet", "iframe", "frame", "frameset", "svg", "math",
		"textarea", "select", "option", "button", "form")
	return p
}

// Incoming sanea el HTML de un mensaje recibido.
func (s *Sanitizer) Incoming(raw string, opts domain.SanitizeOptions) domain.SanitizedHTML {
	if strings.TrimSpace(raw) == "" {
		return domain.SanitizedHTML{}
	}
	remote := false
	out := rewrite(s.policy.Sanitize(raw), func(src string) string {
		return incomingImage(src, opts, &remote)
	})
	return domain.SanitizedHTML{HTML: out, RemoteImages: remote}
}

// Outgoing sanea el HTML que redacta el usuario (sin scripts ni formularios aunque lo
// pegue de otra pagina) y devuelve tambien su version en texto plano.
func (s *Sanitizer) Outgoing(raw string) (string, string) {
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	clean := rewrite(s.policy.Sanitize(raw), outgoingImage)
	return clean, PlainText(clean)
}

// incomingImage decide la fuente de una imagen recibida. Devolver "" quita el atributo.
func incomingImage(src string, opts domain.SanitizeOptions, remote *bool) string {
	lower := strings.ToLower(src)
	switch {
	case strings.HasPrefix(lower, "cid:"):
		// RFC 2392: la URL cid: lleva el Content-ID con escapes de URL.
		cid, err := url.PathUnescape(src[len("cid:"):])
		if err != nil || opts.ResolveCID == nil {
			return ""
		}
		if u, ok := opts.ResolveCID(cid); ok {
			return u
		}
		return ""
	case strings.HasPrefix(lower, "http:"), strings.HasPrefix(lower, "https:"):
		*remote = true
		if opts.AllowRemoteImages {
			return src
		}
		return ""
	case inlineImage.MatchString(lower):
		return src
	}
	return ""
}

// inlineImage acota las imagenes data: a mapas de bits. bluemonday admite tambien
// image/svg+xml, que es un documento con su propio DOM: no se acepta en un correo ajeno.
var inlineImage = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,`)

func outgoingImage(src string) string {
	lower := strings.ToLower(src)
	if inlineImage.MatchString(lower) {
		return src
	}
	for _, scheme := range []string{"cid:", "http:", "https:"} {
		if strings.HasPrefix(lower, scheme) {
			return src
		}
	}
	return ""
}

// InlineImages saca del HTML saliente ya saneado cada imagen data: y la cambia por cid:.
// La misma imagen repetida comparte parte. Una imagen cuyo contenido no es del tipo que
// declara, o demasiadas imagenes, dan un error de validacion del campo html.
func (s *Sanitizer) InlineImages(clean string, newID func() string) (string, []domain.InlineImage, error) {
	var images []domain.InlineImage
	byData := map[string]string{}
	var failure error
	out := rewrite(clean, func(src string) string {
		m := inlineImage.FindStringSubmatch(strings.ToLower(src[:min(len(src), 40)]))
		if m == nil || failure != nil {
			return src
		}
		if cid, ok := byData[src]; ok {
			return "cid:" + cid
		}
		payload := strings.Join(strings.Fields(src[len(m[0]):]), "")
		data, err := base64.StdEncoding.DecodeString(payload)
		contentType := "image/" + m[1]
		if err != nil || !domain.SniffInlineImage(contentType, data) {
			failure = domain.NewValidationError("html", "una imagen del cuerpo no es válida")
			return ""
		}
		if len(images) == domain.MaxInlineImages {
			failure = domain.NewValidationError("html", "demasiadas imágenes en el cuerpo")
			return ""
		}
		cid := newID()
		byData[src] = cid
		images = append(images, domain.InlineImage{ContentID: cid, ContentType: contentType, Data: data})
		return "cid:" + cid
	})
	if failure != nil {
		return "", nil, failure
	}
	return out, images, nil
}

func linkTarget(href string) string {
	lower := strings.ToLower(href)
	for _, scheme := range []string{"http:", "https:", "mailto:"} {
		if strings.HasPrefix(lower, scheme) {
			return href
		}
	}
	return ""
}

// rewrite recorre el HTML ya saneado y reescribe src de img y href de a. El resto de
// tokens se copian tal cual salieron de bluemonday.
func rewrite(fragment string, image func(string) string) string {
	z := html.NewTokenizer(strings.NewReader(fragment))
	var b strings.Builder
	b.Grow(len(fragment))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			raw := string(z.Raw())
			tok := z.Token()
			switch tok.Data {
			case "img":
				tok.Attr = filterAttr(tok.Attr, "src", image)
				b.WriteString(tok.String())
			case "a":
				tok.Attr = filterAttr(tok.Attr, "href", linkTarget)
				b.WriteString(tok.String())
			default:
				b.WriteString(raw)
			}
		default:
			b.Write(z.Raw())
		}
	}
}

// filterAttr pasa el valor del atributo por decide; un resultado vacio lo elimina.
func filterAttr(attrs []html.Attribute, name string, decide func(string) string) []html.Attribute {
	out := attrs[:0]
	for _, a := range attrs {
		if a.Namespace == "" && strings.EqualFold(a.Key, name) {
			v := decide(strings.TrimSpace(a.Val))
			if v == "" {
				continue
			}
			a.Val = v
		}
		out = append(out, a)
	}
	return out
}

// blockElements rompen linea al pasar a texto plano.
var blockElements = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true, "pre": true,
	"table": true, "hr": true, "ul": true, "ol": true, "section": true, "article": true,
	"header": true, "footer": true,
}

// PlainText extrae el texto de un fragmento HTML ya saneado, con saltos de linea en los
// elementos de bloque.
func PlainText(fragment string) string {
	z := html.NewTokenizer(strings.NewReader(fragment))
	var b strings.Builder
	for {
		switch z.Next() {
		case html.ErrorToken:
			return normalizeText(b.String())
		case html.TextToken:
			b.Write(z.Text())
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, _ := z.TagName()
			if blockElements[string(name)] {
				b.WriteByte('\n')
			}
		}
	}
}

var (
	spaceRun   = regexp.MustCompile(`[ \t\r\f\v]+`)
	newlineRun = regexp.MustCompile(`\n{3,}`)
)

func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(newlineRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
