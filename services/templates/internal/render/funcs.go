package render

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"text/template"

	"github.com/shopspring/decimal"
)

// Funciones que una plantilla puede invocar. Son las UNICAS junto con allowedBuiltins: el
// arbol se recorre tras el parseo y cualquier otro identificador (incluidos los builtins
// html, js, call, printf, index, len) rechaza la plantilla. Todas reciben `any` porque los
// valores llegan como string, json.Number o bool y text/template no convierte tipos con
// nombre a string. walker.go decide que argumentos admite cada una.
var allowedFuncs = template.FuncMap{
	"upper":   func(v any) string { return strings.ToUpper(str(v)) },
	"lower":   func(v any) string { return strings.ToLower(str(v)) },
	"title":   func(v any) string { return titleCase(str(v)) },
	"default": defaultValue,
	"date":    formatDate,
	"money":   formatMoney,
	"nonzero": nonZero,
	"count":   countItems,
	"take":    takeItems,
	"rest":    restItems,
}

// allowedBuiltins son las funciones propias de text/template que se admiten: comparan o
// combinan condiciones y no producen contenido.
var allowedBuiltins = map[string]struct{}{"eq": {}, "ne": {}, "and": {}, "or": {}, "not": {}}

func isAllowedFunc(name string) bool {
	if _, ok := allowedFuncs[name]; ok {
		return true
	}
	_, ok := allowedBuiltins[name]
	return ok
}

// allowedFuncNames es la lista, ordenada, que se cita al rechazar una funcion.
func allowedFuncNames() []string {
	names := make([]string, 0, len(allowedFuncs)+len(allowedBuiltins))
	for name := range allowedFuncs {
		names = append(names, name)
	}
	for name := range allowedBuiltins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// formatMoney escribe un importe con dos decimales y separador de miles: 2027.7 -> 2,027.70.
// Redondea al alejarse de cero, sin pasar por float64. El valor ya viene validado como
// numero; un opcional ausente (cadena vacia) produce una cadena vacia.
func formatMoney(v any) (string, error) {
	s := strings.TrimSpace(str(v))
	if s == "" {
		return "", nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return "", errors.New("money: el valor debe ser un número")
	}
	fixed := d.Round(2).StringFixed(2)
	sign := ""
	if strings.HasPrefix(fixed, "-") {
		sign, fixed = "-", fixed[1:]
	}
	whole, frac, _ := strings.Cut(fixed, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String() + "." + frac, nil
}

// nonZero dice si un importe es distinto de cero. Hace falta porque un numero llega como
// json.Number, que es texto: {{if .descuento}} seria verdadero tambien con "0".
func nonZero(v any) (bool, error) {
	s := strings.TrimSpace(str(v))
	if s == "" {
		return false, nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return false, errors.New("nonzero: el valor debe ser un número")
	}
	return !d.IsZero(), nil
}

func items(v any) ([]map[string]any, error) {
	list, ok := v.([]map[string]any)
	if !ok {
		return nil, errors.New("se esperaba una lista")
	}
	return list, nil
}

// countItems es el numero de elementos de una lista.
func countItems(v any) (int, error) {
	list, err := items(v)
	return len(list), err
}

// takeItems devuelve los primeros n elementos: lo que el correo muestra de una lista larga.
func takeItems(n int, v any) ([]map[string]any, error) {
	list, err := items(v)
	if err != nil {
		return nil, err
	}
	if n < len(list) {
		list = list[:n]
	}
	return list, nil
}

// restItems es cuantos elementos quedan fuera de take n: "y 3 productos mas".
func restItems(n int, v any) (int, error) {
	list, err := items(v)
	if err != nil || len(list) <= n {
		return 0, err
	}
	return len(list) - n, nil
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
