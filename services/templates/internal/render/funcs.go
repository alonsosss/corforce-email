package render

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"text/template"
)

// Funciones que una plantilla puede invocar. Son las UNICAS: el arbol se recorre tras el
// parseo y cualquier otro identificador (incluidos los builtins html, js, call, printf)
// rechaza la plantilla. Todas reciben `any` porque los valores llegan como string,
// json.Number o bool y text/template no convierte tipos con nombre a string.
var allowedFuncs = template.FuncMap{
	"upper":   func(v any) string { return strings.ToUpper(str(v)) },
	"lower":   func(v any) string { return strings.ToLower(str(v)) },
	"title":   func(v any) string { return titleCase(str(v)) },
	"default": defaultValue,
	"date":    formatDate,
}

func isAllowedFunc(name string) bool {
	_, ok := allowedFuncs[name]
	return ok
}

func str(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	default:
		return fmt.Sprint(v)
	}
}

// defaultValue devuelve def cuando v esta vacio (nil, cadena vacia, cero o false).
func defaultValue(def, v any) any {
	if v == nil {
		return def
	}
	rv := reflect.ValueOf(v)
	if rv.IsZero() || (rv.Kind() == reflect.String && strings.TrimSpace(rv.String()) == "") {
		return def
	}
	return v
}

// formatDate interpreta v como RFC 3339 y lo escribe con el layout indicado. Una cadena
// vacia produce una cadena vacia: es lo que corresponde a una fecha opcional ausente.
func formatDate(layout string, v any) (string, error) {
	s := strings.TrimSpace(str(v))
	if s == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", errors.New("date: el valor debe ser una fecha RFC 3339")
	}
	return t.Format(layout), nil
}

// titleCase pone en mayuscula la primera letra de cada palabra sin tocar el resto.
func titleCase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	startOfWord := true
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if startOfWord && unicode.IsLetter(r) {
			b.WriteRune(unicode.ToUpper(r))
			startOfWord = false
		} else {
			b.WriteRune(r)
			startOfWord = !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
		}
		i += size
	}
	return b.String()
}
