package deliverability

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/publicsuffix"
)

// linkShorteners son los acortadores publicos que la regla link_shortener rechaza, tambien en
// sus subdominios.
var linkShorteners = []string{
	"bit.ly", "tinyurl.com", "t.co", "goo.gl", "ow.ly", "is.gd", "buff.ly", "cutt.ly", "rebrand.ly", "shorturl.at",
}

// webFontHosts sirven la hoja de estilo de una fuente web del kit de marca (mj-font de MJML): los
// clientes que no la cargan usan la alternativa de la pila, asi que no cuenta como hoja externa.
var webFontHosts = []string{"fonts.googleapis.com"}

// forbiddenElements son los que ya rechaza el render; aqui se vuelven a buscar sobre el HTML
// renderizado por si alguno llegara por otra via.
var forbiddenElements = map[atom.Atom]bool{
	atom.Script: true, atom.Iframe: true, atom.Form: true, atom.Object: true, atom.Embed: true,
}

// invisibleElements no aportan texto visible.
var invisibleElements = map[atom.Atom]bool{
	atom.Head: true, atom.Style: true, atom.Script: true, atom.Title: true, atom.Noscript: true, atom.Template: true,
}

var (
	reSpaces      = regexp.MustCompile(`\s+`)
	reImport      = regexp.MustCompile(`(?i)@import\s+(?:url\(\s*)?["']?([^"'\s);]*)`)
	reStyleWidth  = regexp.MustCompile(`(?i)(?:^|[;\s])(?:min-)?width\s*:\s*(\d+(?:\.\d+)?)\s*px`)
	reBareDomain  = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}(?:[/:?#]\S*)?$`)
	reHiddenStyle = regexp.MustCompile(`(?i)display\s*:\s*none|visibility\s*:\s*hidden|opacity\s*:\s*0(?:\.0+)?\s*(?:;|$|!)|max-height\s*:\s*0(?:px)?\s*(?:;|$|!)`)
)

// document es lo que las reglas necesitan del HTML, reunido en una sola pasada.
type document struct {
	text           string
	textChars      int
	images         int
	missingAlt     int
	links          int
	insecure       int
	shorteners     int
	deceptive      int
	forbidden      int
	wideElements   int
	externalStyles int
	hasUnsubscribe bool
	hasPreheader   bool
}

type walker struct {
	doc            *document
	unsubscribeURL string
	text           strings.Builder
	// seenContent: ya aparecio texto visible o una imagen; un texto oculto posterior no es el
	// preheader.
	seenContent bool
}

func inspect(src, unsubscribeURL string) document {
	var doc document
	root, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return doc
	}
	w := &walker{doc: &doc, unsubscribeURL: strings.TrimSpace(unsubscribeURL)}
	w.walk(root, false, false)
	doc.text = collapse(w.text.String())
	doc.textChars = utf8.RuneCountInString(doc.text)
	return doc
}

func (w *walker) walk(n *html.Node, invisible, hidden bool) {
	switch n.Type {
	case html.TextNode:
		if invisible || hidden {
			return
		}
		if strings.TrimSpace(n.Data) != "" {
			w.seenContent = true
		}
		w.text.WriteString(n.Data)
		w.text.WriteByte(' ')
		return
	case html.ElementNode:
		w.element(n)
		if invisibleElements[n.DataAtom] {
			invisible = true
		}
		if !hidden && !invisible && isHidden(n) {
			hidden = true
			if !w.seenContent && strings.TrimSpace(textOf(n)) != "" {
				w.doc.hasPreheader = true
			}
		}
		if n.DataAtom == atom.Img && !invisible && !hidden {
			w.seenContent = true
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, invisible, hidden)
	}
}

// element aplica las reglas que dependen de un elemento y de sus atributos.
func (w *walker) element(n *html.Node) {
	d := w.doc
	if forbiddenElements[n.DataAtom] {
		d.forbidden++
	}
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if strings.HasPrefix(key, "on") {
			d.forbidden++
		}
		if (key == "href" || key == "src" || key == "action" || key == "background") && isJavascriptURL(a.Val) {
			d.forbidden++
		}
	}
	if isWide(n) {
		d.wideElements++
	}
	if style, ok := attr(n, "style"); ok && hasExternalImport(style) {
		d.externalStyles++
	}

	switch n.DataAtom {
	case atom.Img:
		d.images++
		if _, ok := attr(n, "alt"); !ok {
			d.missingAlt++
		}
	case atom.A:
		href, ok := attr(n, "href")
		href = strings.TrimSpace(href)
		if !ok || href == "" {
			return
		}
		d.links++
		if w.unsubscribeURL != "" && strings.Contains(href, w.unsubscribeURL) {
			d.hasUnsubscribe = true
		}
		u, err := url.Parse(href)
		if err != nil {
			return
		}
		if strings.EqualFold(u.Scheme, "http") {
			d.insecure++
		}
		host := normalizeHost(u.Hostname())
		if host == "" {
			return
		}
		if isShortener(host) {
			d.shorteners++
		}
		if shown := shownHost(collapse(textOf(n))); shown != "" && !sameSite(shown, host) {
			d.deceptive++
		}
	case atom.Link:
		rel, _ := attr(n, "rel")
		href, _ := attr(n, "href")
		if strings.Contains(strings.ToLower(rel), "stylesheet") && !isWebFont(href) {
			d.externalStyles++
		}
	case atom.Style:
		if hasExternalImport(textOf(n)) {
			d.externalStyles++
		}
	}
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return b.String()
}

func collapse(s string) string {
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

func isHidden(n *html.Node) bool {
	if _, ok := attr(n, "hidden"); ok {
		return true
	}
	style, _ := attr(n, "style")
	return reHiddenStyle.MatchString(style)
}

func isJavascriptURL(v string) bool {
	v = strings.Map(func(r rune) rune {
		if r <= ' ' {
			return -1
		}
		return r
	}, v)
	return strings.HasPrefix(strings.ToLower(v), "javascript:")
}

// isWide detecta un ancho fijo en pixeles por encima del tope, en el atributo width o en el
// estilo en linea. Los porcentajes y max-width no fijan el ancho.
func isWide(n *html.Node) bool {
	if v, ok := attr(n, "width"); ok {
		v = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(v)), "px")
		if px, err := strconv.ParseFloat(v, 64); err == nil && px > MaxFixedWidthPx {
			return true
		}
	}
	if style, ok := attr(n, "style"); ok {
		for _, m := range reStyleWidth.FindAllStringSubmatch(style, -1) {
			if px, err := strconv.ParseFloat(m[1], 64); err == nil && px > MaxFixedWidthPx {
				return true
			}
		}
	}
	return false
}

func hasExternalImport(css string) bool {
	for _, m := range reImport.FindAllStringSubmatch(css, -1) {
		if !isWebFont(m[1]) {
			return true
		}
	}
	return false
}

func isWebFont(ref string) bool {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return false
	}
	host := normalizeHost(u.Hostname())
	for _, h := range webFontHosts {
		if host == h {
			return true
		}
	}
	return false
}

func normalizeHost(h string) string {
	return strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(h), "."), "www.")
}

func isShortener(host string) bool {
	for _, s := range linkShorteners {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// shownHost devuelve el host del texto visible de un enlace si ese texto es una URL o un
// dominio ("https://banco.pe/acceso", "www.banco.pe"); vacio si es texto corriente.
func shownHost(text string) string {
	if text == "" || strings.ContainsAny(text, " @") {
		return ""
	}
	lower := strings.ToLower(text)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		if !reBareDomain.MatchString(text) {
			return ""
		}
		text = "https://" + text
	}
	u, err := url.Parse(text)
	if err != nil {
		return ""
	}
	return normalizeHost(u.Hostname())
}

// sameSite compara por dominio registrable: www.acme.pe y tienda.acme.pe son el mismo sitio.
func sameSite(a, b string) bool {
	return registrable(a) == registrable(b)
}

func registrable(host string) string {
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// hasAddress dice si el texto visible contiene la direccion del kit, sin distinguir mayusculas
// ni espacios o saltos de linea.
func hasAddress(text, address string) bool {
	address = strings.ToLower(collapse(address))
	if address == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), address)
}
