package domain

import (
	"strings"
	"unicode/utf8"
)

// maxLineOctets es el largo maximo de una linea de contenido sin el salto (RFC 6350, 3.2; RFC 5545, 3.1).
const maxLineOctets = 75

// rawLine es una linea de contenido ya desdoblada, conservada tal cual para volver a escribirla sin
// tocarla: lo que el traductor no conoce se devuelve con sus parametros y su valor exactos.
type rawLine struct {
	text   string
	group  string
	name   string
	params map[string][]string
	value  string
}

// parseRawLine separa una linea de contenido de vCard o iCalendar. Los parametros quedan con el nombre en
// mayusculas y sus valores separados por comas; un parametro sin "=" (TEL;CELL de vCard 3.0) cuenta como
// valor de TYPE.
func parseRawLine(line string) (rawLine, bool) {
	head, value, ok := splitContentLine(line)
	if !ok {
		return rawLine{}, false
	}
	parts := splitOutsideQuotes(head, ';')
	name := parts[0]
	group := ""
	if g, n, found := strings.Cut(name, "."); found {
		group, name = g, n
	}
	l := rawLine{text: line, group: group, name: strings.ToUpper(name), value: value}
	for _, p := range parts[1:] {
		k, v, found := strings.Cut(p, "=")
		if !found {
			k, v = "TYPE", p
		}
		k = strings.ToUpper(strings.TrimSpace(k))
		if l.params == nil {
			l.params = map[string][]string{}
		}
		for _, item := range splitOutsideQuotes(v, ',') {
			if item = strings.Trim(strings.TrimSpace(item), `"`); item != "" {
				l.params[k] = append(l.params[k], item)
			}
		}
	}
	return l, true
}

// param devuelve el primer valor de un parametro.
func (l rawLine) param(name string) string {
	if v := l.params[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// escapeText escapa un valor de texto segun RFC 6350 (3.4) y RFC 5545 (3.3.11): barra invertida, coma,
// punto y coma y salto de linea. Un salto nunca llega crudo a la salida: no puede abrir una propiedad nueva.
func escapeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case ',':
			b.WriteString(`\,`)
		case ';':
			b.WriteString(`\;`)
		case '\n', '\r':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// splitEscaped parte un valor estructurado (N, ORG) por sep sin cortar en un separador escapado. Cada
// componente queda sin desescapar.
func splitEscaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// foldLine pliega una linea de contenido a 75 octetos sin partir un caracter UTF-8: cada continuacion
// empieza con un espacio, que el lector descarta.
func foldLine(line string) string {
	if len(line) <= maxLineOctets {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + len(line)/maxLineOctets*3)
	limit := maxLineOctets
	for len(line) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n ")
		line = line[cut:]
		limit = maxLineOctets - 1
	}
	b.WriteString(line)
	return b.String()
}

// contentWriter acumula lineas de contenido plegadas y terminadas en CRLF.
type contentWriter struct {
	b strings.Builder
}

func (w *contentWriter) line(s string) {
	w.b.WriteString(foldLine(s))
	w.b.WriteString("\r\n")
}

func (w *contentWriter) String() string { return w.b.String() }

// hasControl dice si s tiene caracteres de control; allowNewline admite el salto de linea (y el CRLF).
func hasControl(s string, allowNewline bool) bool {
	for _, r := range s {
		if r == '\n' && allowNewline {
			continue
		}
		if r == '\r' && allowNewline {
			continue
		}
		if r == '\t' && allowNewline {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == ' ' {
			return true
		}
	}
	return false
}
