package domain

import (
	"bytes"
	"io"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const (
	// UTMMedium es el medio de todo enlace de campana: el canal, no un dato de la campana.
	UTMMedium = "email"
	// MaxUTMValueLen acota cada valor ya normalizado; lo que sobra se corta.
	MaxUTMValueLen = 100
	// MaxUTMInputLen acota lo que se acepta antes de normalizar.
	MaxUTMInputLen = 500
)

// UTMSettings son los parametros de campana que se anaden a los enlaces del HTML de un
// lote de marketing. Con Enabled en false el HTML sale tal cual.
type UTMSettings struct {
	Enabled  bool
	Source   string
	Campaign string
	Content  string
}

var foldDiacritics = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// UTMValue normaliza un valor de UTM: minusculas, sin tildes, y cualquier caracter que no
// sea letra, cifra, punto o guion bajo como un solo guion. Asi el mismo nombre de campana
// da siempre el mismo valor y los informes de analitica web no lo parten en variantes.
func UTMValue(s string) string {
	folded, _, err := transform.String(foldDiacritics, strings.ToLower(strings.TrimSpace(s)))
	if err != nil {
		folded = strings.ToLower(strings.TrimSpace(s))
	}
	var b strings.Builder
	dash := false
	for _, r := range folded {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > MaxUTMValueLen {
		out = strings.TrimRight(out[:MaxUTMValueLen], "-")
	}
	return out
}

// ParseExcludedDomains lee la lista de dominios cuyos enlaces nunca llevan UTM
// (MARKETING_UTM_EXCLUDED_DOMAINS), separados por comas. Un dominio excluye tambien sus
// subdominios.
func ParseExcludedDomains(list string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(list, ",") {
		d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(part)), ".")
		if d == "" {
			continue
		}
		if !validHostname(d) {
			return nil, NewValidationError("dominio excluido de UTM no válido: %q", part)
		}
		out = append(out, d)
	}
	return out, nil
}

func validHostname(d string) bool {
	if len(d) > 253 || strings.HasPrefix(d, ".") || strings.HasPrefix(d, "-") || strings.Contains(d, "..") {
		return false
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// LinkTagger anade los UTM a los enlaces http(s) de un HTML ya renderizado. No toca los
// enlaces de los hosts excluidos (el de la propia plataforma, donde viven la baja y la
// vista en el navegador, y los que configure quien opera), los que no son http(s)
// (mailto, tel, anclas), los que ya traen algun utm_ ni los que conservan marcadores sin
// renderizar.
type LinkTagger struct {
	excluded []string
}

func NewLinkTagger(excludedHosts []string) *LinkTagger {
	hosts := make([]string, 0, len(excludedHosts))
	for _, h := range excludedHosts {
		if h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), "."); h != "" {
			hosts = append(hosts, h)
		}
	}
	return &LinkTagger{excluded: hosts}
}

// HostOf devuelve el host de una URL base (PUBLIC_BASE_URL), o "" si no lo tiene.
func HostOf(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (t *LinkTagger) excludedHost(host string) bool {
	for _, d := range t.excluded {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Tag devuelve el documento con los UTM en el href de cada <a> y <area>. Solo cambia el
// valor de esos atributos: el resto del documento (comentarios condicionales de Outlook,
// entidades, espacios) se copia byte a byte desde el original.
func (t *LinkTagger) Tag(doc string, s UTMSettings) string {
	if !s.Enabled || s.Source == "" || s.Campaign == "" {
		return doc
	}
	params := utmQuery(s)
	z := html.NewTokenizer(strings.NewReader(doc))
	var b bytes.Buffer
	b.Grow(len(doc) + len(doc)/8)
	for {
		tt := z.Next()
		raw := z.Raw()
		if tt == html.ErrorToken {
			if z.Err() != io.EOF {
				return doc
			}
			b.Write(raw)
			return b.String()
		}
		if tt == html.StartTagToken || tt == html.SelfClosingTagToken {
			// TagName y TagAttr pasan a minusculas y desescapan sobre el propio bufer del
			// tokenizador: sin la copia, la etiqueta saldria alterada aunque no se toque.
			raw = bytes.Clone(raw)
			if name, hasAttr := z.TagName(); hasAttr && (string(name) == "a" || string(name) == "area") {
				if href, ok := firstHref(z); ok {
					if tagged, ok := t.tagURL(href, params); ok {
						raw = replaceHref(raw, tagged)
					}
				}
			}
		}
		b.Write(raw)
	}
}

func utmQuery(s UTMSettings) string {
	q := "utm_source=" + url.QueryEscape(s.Source) +
		"&utm_medium=" + UTMMedium +
		"&utm_campaign=" + url.QueryEscape(s.Campaign)
	if s.Content != "" {
		q += "&utm_content=" + url.QueryEscape(s.Content)
	}
	return q
}

func isHTMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// firstHref devuelve el primer href de la etiqueta, el que vale para el navegador, ya
// desescapado con las reglas de los atributos (un "&copy=" crudo no es una entidad).
func firstHref(z *html.Tokenizer) (string, bool) {
	for {
		key, val, more := z.TagAttr()
		if string(key) == "href" {
			return string(val), true
		}
		if !more {
			return "", false
		}
	}
}

// replaceHref localiza en la etiqueta en crudo el primer atributo href y sustituye solo
// su valor. Ante una etiqueta que no sabe leer la devuelve intacta.
func replaceHref(raw []byte, tagged string) []byte {
	n := len(raw)
	i := 1
	for i < n && !isHTMLSpace(raw[i]) && raw[i] != '>' && raw[i] != '/' {
		i++
	}
	for i < n {
		for i < n && (isHTMLSpace(raw[i]) || raw[i] == '/') {
			i++
		}
		if i >= n || raw[i] == '>' {
			return raw
		}
		start := i
		i++
		for i < n && !isHTMLSpace(raw[i]) && raw[i] != '=' && raw[i] != '>' && raw[i] != '/' {
			i++
		}
		name := strings.ToLower(string(raw[start:i]))
		j := i
		for j < n && isHTMLSpace(raw[j]) {
			j++
		}
		if j >= n || raw[j] != '=' {
			i = j
			continue
		}
		j++
		for j < n && isHTMLSpace(raw[j]) {
			j++
		}
		if j >= n {
			return raw
		}
		var valStart, valEnd int
		quote := byte(0)
		if raw[j] == '"' || raw[j] == '\'' {
			quote = raw[j]
			valStart = j + 1
			k := bytes.IndexByte(raw[valStart:], quote)
			if k < 0 {
				return raw
			}
			valEnd = valStart + k
			i = valEnd + 1
		} else {
			valStart = j
			valEnd = j
			for valEnd < n && !isHTMLSpace(raw[valEnd]) && raw[valEnd] != '>' {
				valEnd++
			}
			i = valEnd
		}
		if name != "href" {
			continue
		}
		escaped := html.EscapeString(tagged)
		out := make([]byte, 0, n+len(escaped))
		if quote == 0 {
			out = append(out, raw[:valStart]...)
			out = append(out, '"')
			out = append(out, escaped...)
			out = append(out, '"')
		} else {
			out = append(out, raw[:valStart]...)
			out = append(out, escaped...)
		}
		return append(out, raw[valEnd:]...)
	}
	return raw
}

// tagURL anade los parametros antes del fragmento, respetando la query que ya tenga.
func (t *LinkTagger) tagURL(href, params string) (string, bool) {
	u := strings.TrimSpace(href)
	lower := strings.ToLower(u)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "", false
	}
	if strings.Contains(u, "{{") || strings.Contains(u, "}}") {
		return "", false
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if t.excludedHost(strings.ToLower(parsed.Hostname())) || hasUTM(parsed.RawQuery) {
		return "", false
	}
	base, fragment, hasFragment := strings.Cut(u, "#")
	switch {
	case strings.HasSuffix(base, "?") || strings.HasSuffix(base, "&"):
		base += params
	case strings.Contains(base, "?"):
		base += "&" + params
	default:
		base += "?" + params
	}
	if hasFragment {
		return base + "#" + fragment, true
	}
	return base, true
}

func hasUTM(rawQuery string) bool {
	for _, pair := range strings.FieldsFunc(rawQuery, func(r rune) bool { return r == '&' || r == ';' }) {
		key, _, _ := strings.Cut(pair, "=")
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "utm_") {
			return true
		}
	}
	return false
}
