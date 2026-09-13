package render

import (
	"html"
	"regexp"
	"strings"
)

// Conversion de HTML renderizado a texto plano para la parte text/plain del correo
// cuando la version no trae una propia. No pretende ser un navegador: quita lo que no se
// lee (head, style, comentarios), conserva la estructura en saltos de linea, deja los
// enlaces como "texto (url)" y limpia entidades y espacios.
var (
	reHead      = regexp.MustCompile(`(?is)<head\b.*?</head>`)
	reStyle     = regexp.MustCompile(`(?is)<style\b.*?</style>`)
	reScript    = regexp.MustCompile(`(?is)<script\b.*?</script>`)
	reComment   = regexp.MustCompile(`(?s)<!--.*?-->`)
	reAnchor    = regexp.MustCompile(`(?is)<a\b[^>]*?\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>(.*?)</a>`)
	reBreak     = regexp.MustCompile(`(?i)<br\s*/?>`)
	reHr        = regexp.MustCompile(`(?i)<hr\b[^>]*>`)
	reListItem  = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	reBlockOpen = regexp.MustCompile(`(?i)<(?:p|div|h[1-6]|table|ul|ol|blockquote|section|article|header|footer|tr)\b[^>]*>`)
	reParaClose = regexp.MustCompile(`(?i)</(?:p|div|h[1-6]|table|ul|ol|blockquote|section|article|header|footer)\s*>`)
	reRowClose  = regexp.MustCompile(`(?i)</tr\s*>`)
	reItemClose = regexp.MustCompile(`(?i)</li\s*>`)
	reCellClose = regexp.MustCompile(`(?i)</t[dh]\s*>`)
	reTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	reHSpace    = regexp.MustCompile(`[ \t\r\f\v\x{00A0}]+`)
	reBlank     = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText genera la parte de texto plano a partir del HTML ya renderizado.
func HTMLToText(src string) string {
	s := reComment.ReplaceAllString(src, "")
	s = reHead.ReplaceAllString(s, "")
	s = reStyle.ReplaceAllString(s, "")
	s = reScript.ReplaceAllString(s, "")
	s = reAnchor.ReplaceAllStringFunc(s, flattenAnchor)
	s = reBreak.ReplaceAllString(s, "\n")
	s = reHr.ReplaceAllString(s, "\n")
	s = reListItem.ReplaceAllString(s, "\n- ")
	s = reBlockOpen.ReplaceAllString(s, "\n")
	s = reParaClose.ReplaceAllString(s, "\n\n")
	s = reRowClose.ReplaceAllString(s, "\n")
	s = reItemClose.ReplaceAllString(s, "")
	s = reCellClose.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return collapseWhitespace(s)
}

// flattenAnchor convierte <a href="u">t</a> en "t (u)". Si el texto ya es la URL o esta
// vacio, queda solo la URL. Las entidades se decodifican despues, una sola vez, sobre
// todo el documento.
func flattenAnchor(anchor string) string {
	m := reAnchor.FindStringSubmatch(anchor)
	if m == nil {
		return anchor
	}
	href := strings.TrimSpace(m[1] + m[2] + m[3])
	text := strings.TrimSpace(reTag.ReplaceAllString(m[4], ""))
	switch {
	case href == "":
		return text
	case text == "" || text == href:
		return href
	default:
		return text + " (" + href + ")"
	}
}

func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(reHSpace.ReplaceAllString(line, " "))
	}
	out := strings.Join(lines, "\n")
	out = reBlank.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}
