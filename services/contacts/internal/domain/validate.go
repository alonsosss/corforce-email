package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxNameLength es el limite de first_name y last_name.
	MaxNameLength = 200
	// MaxTags es el tope de etiquetas por contacto.
	MaxTags = 100
	// MaxAttributeDefinitions es el tope de atributos declarados por empresa.
	MaxAttributeDefinitions = 200
	// MaxAttributeString es el limite de un valor de atributo de texto.
	MaxAttributeString = 1000
	// maxNumberLength acota el literal de un atributo numerico; numeric no tiene limite
	// practico y un literal de megabytes no es un dato de marketing.
	maxNumberLength = 40
	// DateLayout es la unica forma aceptada para los atributos de tipo date.
	DateLayout = "2006-01-02"
)

var (
	localeRegex   = regexp.MustCompile(`^[a-z]{2,3}(-([A-Z]{2}|[0-9]{3}))?$`)
	timezoneRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+){0,2}$`)
	tagRegex      = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} _.:\-]{0,63}$`)
	attrKeyRegex  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

// reservedKeys son los campos fijos del contacto y del DSL de segmentos: una clave de
// atributo con ese nombre haria ambiguo "attributes.<key>" frente al campo fijo.
var reservedKeys = map[string]struct{}{
	"id": {}, "tenant_id": {}, "email": {}, "first_name": {}, "last_name": {}, "locale": {},
	"timezone": {}, "attributes": {}, "tags": {}, "status": {}, "source": {}, "consent": {},
	"marketing_consent": {}, "list": {}, "created_at": {}, "updated_at": {},
}

// NormalizeName recorta y comprueba la longitud de first_name o last_name.
func NormalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if utf8.RuneCountInString(name) > MaxNameLength || strings.ContainsAny(name, "\x00\r\n") {
		return "", ErrInvalidName
	}
	return name, nil
}

// NormalizeLocale acepta una etiqueta BCP 47 corta: idioma y, opcionalmente, region
// (es, es-PE, es-419). Vacio = sin locale.
func NormalizeLocale(raw string) (*string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(strings.ReplaceAll(s, "_", "-"), "-")
	if len(parts) > 2 {
		return nil, ErrInvalidLocale
	}
	parts[0] = strings.ToLower(parts[0])
	if len(parts) == 2 {
		parts[1] = strings.ToUpper(parts[1])
	}
	norm := strings.Join(parts, "-")
	if !localeRegex.MatchString(norm) {
		return nil, ErrInvalidLocale
	}
	return &norm, nil
}

// NormalizeTimezone acepta un nombre de zona IANA que el sistema sepa cargar (el binario
// incorpora time/tzdata, asi que no depende de la imagen). "Local" no es una zona: es la
// del servidor, y guardarla no diria nada del contacto. Vacio = sin zona.
func NormalizeTimezone(raw string) (*string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	if s == "Local" || len(s) > 64 || !timezoneRegex.MatchString(s) {
		return nil, ErrInvalidTimezone
	}
	if _, err := time.LoadLocation(s); err != nil {
		return nil, ErrInvalidTimezone
	}
	return &s, nil
}

// NormalizeTag deja una etiqueta en su forma canonica: recortada y en minusculas.
func NormalizeTag(raw string) (string, error) {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if !tagRegex.MatchString(tag) {
		return "", ErrInvalidTag
	}
	return tag, nil
}

// NormalizeTags normaliza, quita repetidas conservando el orden y aplica el tope.
func NormalizeTags(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		tag, err := NormalizeTag(r)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidTag, truncate(r, 64))
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) > MaxTags {
		return nil, ErrTooManyTags
	}
	return out, nil
}

// MergeTags une etiquetas nuevas a las existentes sin repetir y respetando el tope.
func MergeTags(current, add []string) ([]string, error) {
	return NormalizeTags(append(append([]string{}, current...), add...))
}

// ValidateAttributeKey comprueba la forma de la clave y que no pise un campo fijo.
func ValidateAttributeKey(key string) error {
	if !attrKeyRegex.MatchString(key) {
		return ErrInvalidAttributeKey
	}
	if _, reserved := reservedKeys[key]; reserved {
		return ErrReservedAttributeKey
	}
	return nil
}

func ParseAttrType(s string) (AttrType, error) {
	for _, t := range AttrTypes() {
		if AttrType(s) == t {
			return t, nil
		}
	}
	return "", ErrInvalidAttributeType
}

// RawAttributes son los valores tal como llegan en el JSON, sin interpretar: el tipo
// con el que se leen lo decide la definicion del atributo, no el cliente.
type RawAttributes map[string]json.RawMessage

// ParseAttributeValue interpreta un valor segun el tipo declarado. JSON null devuelve
// (nil, nil): el atributo no tiene valor.
func ParseAttributeValue(t AttrType, raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, ErrAttributeValue
	}
	switch t {
	case AttrString:
		s, ok := v.(string)
		if !ok || utf8.RuneCountInString(s) > MaxAttributeString || strings.ContainsRune(s, 0) {
			return nil, ErrAttributeValue
		}
		return s, nil
	case AttrNumber:
		n, ok := v.(json.Number)
		if !ok || len(n) > maxNumberLength {
			return nil, ErrAttributeValue
		}
		// Un literal que desborda float64 (1e400) no es NaN ni Inf en el JSON, pero lo
		// seria en cualquier consumidor que lo lea como numero: se rechaza al entrar.
		f, err := strconv.ParseFloat(n.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, ErrAttributeValue
		}
		return n, nil
	case AttrBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, ErrAttributeValue
		}
		return b, nil
	case AttrDate:
		s, ok := v.(string)
		if !ok {
			return nil, ErrAttributeValue
		}
		if _, err := time.Parse(DateLayout, s); err != nil {
			return nil, ErrAttributeValue
		}
		return s, nil
	}
	return nil, ErrInvalidAttributeType
}

// Definitions indexa las definiciones de atributos por clave.
type Definitions map[string]AttributeDefinition

func IndexDefinitions(defs []AttributeDefinition) Definitions {
	out := make(Definitions, len(defs))
	for _, d := range defs {
		out[d.Key] = d
	}
	return out
}

// MergeAttributes valida los valores recibidos contra las definiciones y los aplica
// sobre current (que no se modifica). Un null quita el atributo, salvo que sea
// obligatorio. Devuelve el mapa resultante y las claves que cambiaron.
func MergeAttributes(current map[string]any, in RawAttributes, defs Definitions) (map[string]any, []string, error) {
	out := make(map[string]any, len(current)+len(in))
	for k, v := range current {
		out[k] = v
	}
	var changed []string
	for key, raw := range in {
		def, ok := defs[key]
		if !ok {
			return nil, nil, fmt.Errorf("%w: attributes.%s", ErrUndeclaredAttribute, truncate(key, 63))
		}
		v, err := ParseAttributeValue(def.Type, raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: attributes.%s es %s", err, key, def.Type)
		}
		if v == nil {
			if def.Required {
				return nil, nil, fmt.Errorf("%w: attributes.%s", ErrRequiredAttribute, key)
			}
			if _, had := out[key]; had {
				delete(out, key)
				changed = append(changed, key)
			}
			continue
		}
		if prev, had := out[key]; !had || !sameValue(prev, v) {
			out[key] = v
			changed = append(changed, key)
		}
	}
	return out, changed, nil
}

// CheckRequired exige, al crear un contacto, cada atributo declarado como obligatorio.
func CheckRequired(attrs map[string]any, defs Definitions) error {
	for key, def := range defs {
		if !def.Required {
			continue
		}
		if _, ok := attrs[key]; !ok {
			return fmt.Errorf("%w: attributes.%s", ErrRequiredAttribute, key)
		}
	}
	return nil
}

func sameValue(a, b any) bool {
	switch av := a.(type) {
	case json.Number:
		bv, ok := b.(json.Number)
		return ok && av.String() == bv.String()
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	}
	return false
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
