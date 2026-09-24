// Package segment define el DSL de los segmentos dinamicos y lo compila a una clausula
// WHERE parametrizada sobre contacts.contacts (alias c).
//
// Garantias de seguridad:
//   - Ningun texto del usuario llega al SQL: los campos salen de una lista blanca que
//     resuelve a columnas fijas, las claves de atributo y todos los valores viajan como
//     argumentos ($n).
//   - Cada predicado compilado es de dos valores (nunca NULL): los segmentos se combinan
//     con OR y se excluyen con NOT, y un NULL dentro de un NOT sacaria en silencio a
//     contactos que no estan en la exclusion.
//
// Las reglas de interaccion (campaign, last_campaigns, last_days) leen la proyeccion
// contacts.engagement que mantiene el consumidor de transactional.email.*. Una apertura
// la registra el pixel de seguimiento, y Apple Mail (Mail Privacy Protection) y algunos
// antivirus lo descargan sin que la persona abra el correo: "abrio" sobrestima y "no
// abrio" subestima. El clic es la senal fiable; el editor lo advierte.
//
// Sin dependencias fuera de la biblioteca estandar.
package segment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxDepth es la profundidad maxima: el grupo raiz cuenta como 1.
	MaxDepth = 5
	// MaxRules es el total de reglas (condiciones y subgrupos) de una definicion.
	MaxRules = 50
	// MaxInValues es el tope de valores del operador in.
	MaxInValues = 500
	// MaxStringValue es el limite de un valor de texto.
	MaxStringValue = 500
	// MaxDefinitionBytes es el tamano maximo de una definicion serializada.
	MaxDefinitionBytes = 64 << 10
	// MaxLastCampaigns es el tope de N en las reglas de las ultimas N campanas.
	MaxLastCampaigns = 50
	// MaxLastDays es el tope de N en las reglas de los ultimos N dias. La retencion de la
	// proyeccion (CONTACTS_ENGAGEMENT_RETENTION_DAYS) no puede ser menor.
	MaxLastDays     = 365
	maxNumberLength = 40
	dateLayout      = "2006-01-02"
)

// ErrInvalid envuelve todo error de validacion de una definicion.
var ErrInvalid = errors.New("definicion de segmento no valida")

func invalid(path, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if path != "" {
		msg = path + ": " + msg
	}
	return fmt.Errorf("%w: %s", ErrInvalid, msg)
}

type Match string

const (
	MatchAll Match = "all"
	MatchAny Match = "any"
)

type Op string

const (
	OpEq         Op = "eq"
	OpNeq        Op = "neq"
	OpContains   Op = "contains"
	OpStartsWith Op = "starts_with"
	OpGt         Op = "gt"
	OpGte        Op = "gte"
	OpLt         Op = "lt"
	OpLte        Op = "lte"
	OpIn         Op = "in"
	OpExists     Op = "exists"
	OpNotExists  Op = "not_exists"
	OpHasTag     Op = "has_tag"
	OpInList     Op = "in_list"
	OpNotInList  Op = "not_in_list"
	OpOpened     Op = "opened"
	OpClicked    Op = "clicked"
	OpNotOpened  Op = "not_opened"
)

// AttrType es el tipo declarado de un atributo (mismos nombres que en el dominio).
type AttrType string

const (
	AttrString  AttrType = "string"
	AttrNumber  AttrType = "number"
	AttrBoolean AttrType = "boolean"
	AttrDate    AttrType = "date"
)

// Schema es lo que la definicion puede usar: los atributos declarados de la empresa con
// su tipo y los valores admitidos de los campos enumerados (status, source, consent).
type Schema struct {
	Attributes map[string]AttrType
	Enums      map[string][]string
}

// Definition es un grupo de reglas. La raiz de un segmento es siempre un grupo.
type Definition struct {
	Match Match  `json:"match"`
	Rules []Rule `json:"rules"`
}

// Rule es una condicion (Field, Op, Value) o un subgrupo (Group).
type Rule struct {
	Field string
	Op    Op
	Value json.RawMessage
	Group *Definition
}

func (r Rule) MarshalJSON() ([]byte, error) {
	if r.Group != nil {
		return json.Marshal(r.Group)
	}
	leaf := struct {
		Field string          `json:"field"`
		Op    Op              `json:"op"`
		Value json.RawMessage `json:"value,omitempty"`
	}{Field: r.Field, Op: r.Op, Value: r.Value}
	return json.Marshal(leaf)
}

// Parse lee una definicion con reglas estrictas: claves desconocidas, una regla que es
// a la vez condicion y grupo, o un cuerpo que no es un objeto, son errores.
func Parse(raw []byte) (Definition, error) {
	if len(raw) > MaxDefinitionBytes {
		return Definition{}, invalid("", "supera el tamano maximo de %d KB", MaxDefinitionBytes>>10)
	}
	return parseGroup(raw, "definition")
}

func parseGroup(raw []byte, path string) (Definition, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		return Definition{}, invalid(path, "debe ser un objeto {match, rules}")
	}
	for k := range probe {
		if k != "match" && k != "rules" {
			return Definition{}, invalid(path, "clave no admitida en un grupo: %q", clip(k))
		}
	}
	var def Definition
	if m, ok := probe["match"]; ok {
		var s string
		if err := json.Unmarshal(m, &s); err != nil {
			return Definition{}, invalid(path, "match debe ser all o any")
		}
		def.Match = Match(s)
	}
	var rules []json.RawMessage
	if rr, ok := probe["rules"]; ok {
		if err := json.Unmarshal(rr, &rules); err != nil {
			return Definition{}, invalid(path, "rules debe ser una lista")
		}
	}
	def.Rules = make([]Rule, 0, len(rules))
	for i, rr := range rules {
		rule, err := parseRule(rr, fmt.Sprintf("%s.rules[%d]", path, i))
		if err != nil {
			return Definition{}, err
		}
		def.Rules = append(def.Rules, rule)
	}
	return def, nil
}

func parseRule(raw []byte, path string) (Rule, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		return Rule{}, invalid(path, "cada regla debe ser un objeto")
	}
	_, hasMatch := probe["match"]
	_, hasRules := probe["rules"]
	if hasMatch || hasRules {
		for k := range probe {
			if k != "match" && k != "rules" {
				return Rule{}, invalid(path, "una regla no puede ser a la vez condicion y grupo")
			}
		}
		g, err := parseGroup(raw, path)
		if err != nil {
			return Rule{}, err
		}
		return Rule{Group: &g}, nil
	}
	for k := range probe {
		if k != "field" && k != "op" && k != "value" {
			return Rule{}, invalid(path, "clave no admitida en una condicion: %q", clip(k))
		}
	}
	var r Rule
	if err := json.Unmarshal(probe["field"], &r.Field); err != nil {
		return Rule{}, invalid(path, "field debe ser texto")
	}
	var op string
	if err := json.Unmarshal(probe["op"], &op); err != nil {
		return Rule{}, invalid(path, "op debe ser texto")
	}
	r.Op = Op(op)
	if v, ok := probe["value"]; ok && !isNull(v) {
		var buf bytes.Buffer
		if err := json.Compact(&buf, v); err != nil {
			return Rule{}, invalid(path, "value no es JSON valido")
		}
		r.Value = buf.Bytes()
	}
	return r, nil
}

// Args acumula los argumentos de una consulta y numera sus placeholders. Una misma Args
// puede recibir varias definiciones (la audiencia une y excluye segmentos en una sola
// consulta) y argumentos propios del que la usa.
type Args struct {
	values []any
}

// NewArgs arranca con los argumentos que el llamador ya uso ($1..$n).
func NewArgs(initial ...any) *Args {
	return &Args{values: append([]any(nil), initial...)}
}

// Add registra un argumento y devuelve su placeholder.
func (a *Args) Add(v any) string {
	a.values = append(a.values, v)
	return "$" + strconv.Itoa(len(a.values))
}

func (a *Args) Values() []any { return a.values }

// Compile valida la definicion contra el esquema y devuelve la clausula (sin WHERE) que
// filtra contacts.contacts c. Los argumentos quedan en args.
func Compile(def Definition, schema Schema, args *Args) (string, error) {
	c := &compiler{schema: schema, args: args}
	return c.group(def, 1, "definition")
}

// Validate comprueba la definicion sin usar el SQL resultante.
func Validate(def Definition, schema Schema) error {
	_, err := Compile(def, schema, NewArgs())
	return err
}

// References son los atributos y listas que usa una definicion: una clave o una lista
// referenciadas no se pueden borrar sin cambiar lo que el segmento selecciona.
type References struct {
	Attributes []string
	Lists      []string
}

func Refs(def Definition) References {
	var refs References
	seenA, seenL := map[string]bool{}, map[string]bool{}
	var walk func(d Definition)
	walk = func(d Definition) {
		for _, r := range d.Rules {
			if r.Group != nil {
				walk(*r.Group)
				continue
			}
			if key, ok := strings.CutPrefix(r.Field, attrPrefix); ok && !seenA[key] {
				seenA[key] = true
				refs.Attributes = append(refs.Attributes, key)
			}
			if r.Field == "list" {
				var id string
				if json.Unmarshal(r.Value, &id) == nil {
					id = strings.ToLower(strings.TrimSpace(id))
					if !seenL[id] {
						seenL[id] = true
						refs.Lists = append(refs.Lists, id)
					}
				}
			}
		}
	}
	walk(def)
	return refs
}

// ── Compilacion ──────────────────────────────────────────────────────────────

const attrPrefix = "attributes."

var (
	attrKeyRegex = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	uuidRegex    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	likeEscaper  = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
)

type kind int

const (
	kindEmail         kind = iota // NOT NULL, en minusculas
	kindText                      // NOT NULL; '' = sin valor
	kindNullableText              // puede ser NULL
	kindEnum                      // NOT NULL, valores del Schema.Enums
	kindTimestamp                 // NOT NULL
	kindTags                      // text[] NOT NULL
	kindList                      // pertenencia a lista
	kindCampaign                  // interaccion con una campana concreta
	kindLastCampaigns             // interaccion con las ultimas N campanas recibidas
	kindLastDays                  // interaccion en los ultimos N dias
)

type fixedField struct {
	name   string
	column string
	kind   kind
}

// fieldSpecs es la lista blanca de campos fijos, en el orden en que se presentan. La
// columna es SIEMPRE uno de estos literales; el nombre que manda el usuario solo sirve
// para buscarla aqui.
var fieldSpecs = []fixedField{
	{"email", "c.email", kindEmail},
	{"first_name", "c.first_name", kindText},
	{"last_name", "c.last_name", kindText},
	{"locale", "c.locale", kindNullableText},
	{"timezone", "c.timezone", kindNullableText},
	{"status", "c.status", kindEnum},
	{"source", "c.source", kindEnum},
	{"consent", "c.marketing_consent", kindEnum},
	{"created_at", "c.created_at", kindTimestamp},
	{"tags", "c.tags", kindTags},
	{"list", "", kindList},
	{"campaign", "", kindCampaign},
	{"last_campaigns", "", kindLastCampaigns},
	{"last_days", "", kindLastDays},
}

var fixedFields = indexFields(fieldSpecs)

func indexFields(specs []fixedField) map[string]fixedField {
	out := make(map[string]fixedField, len(specs))
	for _, f := range specs {
		out[f.name] = f
	}
	return out
}

type compiler struct {
	schema Schema
	args   *Args
	rules  int
}

func (c *compiler) group(def Definition, depth int, path string) (string, error) {
	if depth > MaxDepth {
		return "", invalid(path, "supera la profundidad maxima de %d niveles", MaxDepth)
	}
	var joiner string
	switch def.Match {
	case MatchAll:
		joiner = " AND "
	case MatchAny:
		joiner = " OR "
	default:
		return "", invalid(path, "match debe ser all o any")
	}
	if len(def.Rules) == 0 {
		return "", invalid(path, "rules no puede estar vacio")
	}
	parts := make([]string, 0, len(def.Rules))
	for i, r := range def.Rules {
		c.rules++
		if c.rules > MaxRules {
			return "", invalid("", "supera el maximo de %d reglas", MaxRules)
		}
		rpath := fmt.Sprintf("%s.rules[%d]", path, i)
		var (
			sql string
			err error
		)
		if r.Group != nil {
			sql, err = c.group(*r.Group, depth+1, rpath)
		} else {
			sql, err = c.leaf(r, rpath)
		}
		if err != nil {
			return "", err
		}
		parts = append(parts, sql)
	}
	return "(" + strings.Join(parts, joiner) + ")", nil
}

func (c *compiler) leaf(r Rule, path string) (string, error) {
	if key, ok := strings.CutPrefix(r.Field, attrPrefix); ok {
		if !attrKeyRegex.MatchString(key) {
			return "", invalid(path, "clave de atributo no valida")
		}
		typ, declared := c.schema.Attributes[key]
		if !declared {
			return "", invalid(path, "el atributo %q no esta declarado", key)
		}
		return c.attribute(key, typ, r, path)
	}
	f, ok := fixedFields[r.Field]
	if !ok {
		return "", invalid(path, "campo no permitido: %q", clip(r.Field))
	}
	switch f.kind {
	case kindEmail:
		return c.email(f.column, r, path)
	case kindText:
		return c.text(f.column, r, path)
	case kindNullableText:
		return c.nullableText(f.column, r, path)
	case kindEnum:
		return c.enum(r.Field, f.column, r, path)
	case kindTimestamp:
		return c.timestamp(f.column, r, path)
	case kindTags:
		return c.tags(f.column, r, path)
	case kindList:
		return c.list(r, path)
	case kindCampaign:
		return c.campaign(r, path)
	case kindLastCampaigns:
		return c.lastCampaigns(r, path)
	case kindLastDays:
		return c.lastDays(r, path)
	}
	return "", invalid(path, "campo no permitido: %q", clip(r.Field))
}

func (c *compiler) email(col string, r Rule, path string) (string, error) {
	switch r.Op {
	case OpEq, OpNeq:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		op := " = "
		if r.Op == OpNeq {
			op = " <> "
		}
		return col + op + c.args.Add(strings.ToLower(strings.TrimSpace(v))) + "::text", nil
	case OpContains, OpStartsWith:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return col + " LIKE " + c.args.Add(likePattern(strings.ToLower(v), r.Op)) + ` ESCAPE '\'`, nil
	case OpIn:
		vs, err := stringValues(r.Value, path)
		if err != nil {
			return "", err
		}
		for i := range vs {
			vs[i] = strings.ToLower(strings.TrimSpace(vs[i]))
		}
		return col + " = ANY(" + c.args.Add(vs) + "::text[])", nil
	}
	return "", badOp(path, "email", r.Op)
}

func (c *compiler) text(col string, r Rule, path string) (string, error) {
	switch r.Op {
	case OpEq, OpNeq:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		op := " = "
		if r.Op == OpNeq {
			op = " <> "
		}
		return "lower(" + col + ")" + op + "lower(" + c.args.Add(v) + "::text)", nil
	case OpContains, OpStartsWith:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return col + " ILIKE " + c.args.Add(likePattern(v, r.Op)) + ` ESCAPE '\'`, nil
	case OpIn:
		vs, err := stringValues(r.Value, path)
		if err != nil {
			return "", err
		}
		return "lower(" + col + ") IN (SELECT lower(v) FROM unnest(" + c.args.Add(vs) + "::text[]) AS v)", nil
	case OpExists, OpNotExists:
		if err := noValue(r.Value, path); err != nil {
			return "", err
		}
		if r.Op == OpExists {
			return col + " <> ''", nil
		}
		return col + " = ''", nil
	}
	return "", badOp(path, r.Field, r.Op)
}

func (c *compiler) nullableText(col string, r Rule, path string) (string, error) {
	switch r.Op {
	case OpEq:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return "COALESCE(lower(" + col + ") = lower(" + c.args.Add(v) + "::text), false)", nil
	case OpNeq:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return "lower(" + col + ") IS DISTINCT FROM lower(" + c.args.Add(v) + "::text)", nil
	case OpContains, OpStartsWith:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return "COALESCE(" + col + " ILIKE " + c.args.Add(likePattern(v, r.Op)) + ` ESCAPE '\', false)`, nil
	case OpIn:
		vs, err := stringValues(r.Value, path)
		if err != nil {
			return "", err
		}
		return "COALESCE(lower(" + col + ") IN (SELECT lower(v) FROM unnest(" + c.args.Add(vs) + "::text[]) AS v), false)", nil
	case OpExists, OpNotExists:
		if err := noValue(r.Value, path); err != nil {
			return "", err
		}
		if r.Op == OpExists {
			return col + " IS NOT NULL", nil
		}
		return col + " IS NULL", nil
	}
	return "", badOp(path, r.Field, r.Op)
}

func (c *compiler) enum(field, col string, r Rule, path string) (string, error) {
	allowed := c.schema.Enums[field]
	if len(allowed) == 0 {
		return "", invalid(path, "el campo %s no tiene valores declarados", field)
	}
	check := func(v string) error {
		for _, a := range allowed {
			if v == a {
				return nil
			}
		}
		return invalid(path, "%s debe ser uno de: %s", field, strings.Join(allowed, ", "))
	}
	switch r.Op {
	case OpEq, OpNeq:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		if err := check(v); err != nil {
			return "", err
		}
		op := " = "
		if r.Op == OpNeq {
			op = " <> "
		}
		return col + op + c.args.Add(v) + "::text", nil
	case OpIn:
		vs, err := stringValues(r.Value, path)
		if err != nil {
			return "", err
		}
		for _, v := range vs {
			if err := check(v); err != nil {
				return "", err
			}
		}
		return col + " = ANY(" + c.args.Add(vs) + "::text[])", nil
	}
	return "", badOp(path, field, r.Op)
}

func (c *compiler) timestamp(col string, r Rule, path string) (string, error) {
	cmp, ok := comparison(r.Op)
	if !ok {
		return "", badOp(path, r.Field, r.Op)
	}
	t, err := timestampValue(r.Value, path)
	if err != nil {
		return "", err
	}
	return col + cmp + c.args.Add(t) + "::timestamptz", nil
}

func (c *compiler) tags(col string, r Rule, path string) (string, error) {
	switch r.Op {
	case OpHasTag, OpNeq:
		v, err := tagValue(r.Value, path)
		if err != nil {
			return "", err
		}
		expr := col + " @> " + c.args.Add([]string{v}) + "::text[]"
		if r.Op == OpNeq {
			return "NOT (" + expr + ")", nil
		}
		return expr, nil
	case OpIn:
		vs, err := stringValues(r.Value, path)
		if err != nil {
			return "", err
		}
		norm := make([]string, 0, len(vs))
		for _, v := range vs {
			t, err := normalizeTag(v, path)
			if err != nil {
				return "", err
			}
			norm = append(norm, t)
		}
		return col + " && " + c.args.Add(norm) + "::text[]", nil
	case OpExists:
		if err := noValue(r.Value, path); err != nil {
			return "", err
		}
		return "cardinality(" + col + ") > 0", nil
	case OpNotExists:
		if err := noValue(r.Value, path); err != nil {
			return "", err
		}
		return "cardinality(" + col + ") = 0", nil
	}
	return "", badOp(path, "tags", r.Op)
}

func (c *compiler) list(r Rule, path string) (string, error) {
	if r.Op != OpInList && r.Op != OpNotInList {
		return "", badOp(path, "list", r.Op)
	}
	v, err := stringValue(r.Value, path)
	if err != nil {
		return "", err
	}
	id := strings.ToLower(strings.TrimSpace(v))
	if !uuidRegex.MatchString(id) {
		return "", invalid(path, "value debe ser el id de una lista")
	}
	sub := "EXISTS (SELECT 1 FROM contacts.list_members lm WHERE lm.list_id = " + c.args.Add(id) + "::uuid AND lm.contact_id = c.id)"
	if r.Op == OpNotInList {
		return "NOT " + sub, nil
	}
	return sub, nil
}

// ── Interaccion (contacts.engagement) ────────────────────────────────────────
//
// Una fila por contacto y envio de marketing (campana o flujo de automations) con la hora
// en que lo recibio y las de su ultima apertura y su ultimo clic. Un clic cuenta tambien
// como apertura. Todas las subconsultas van por la clave (contact_id, ...) de sus indices.

const engagementTable = "contacts.engagement e"

func engagementColumn(op Op) string {
	if op == OpClicked {
		return "e.last_clicked_at"
	}
	return "e.last_opened_at"
}

func (c *compiler) campaign(r Rule, path string) (string, error) {
	if r.Op != OpOpened && r.Op != OpClicked {
		return "", badOp(path, "campaign", r.Op)
	}
	v, err := stringValue(r.Value, path)
	if err != nil {
		return "", err
	}
	id := strings.ToLower(strings.TrimSpace(v))
	if !uuidRegex.MatchString(id) {
		return "", invalid(path, "value debe ser el id de una campana")
	}
	return "EXISTS (SELECT 1 FROM " + engagementTable + " WHERE e.contact_id = c.id AND e.campaign_id = " +
		c.args.Add(id) + "::uuid AND " + engagementColumn(r.Op) + " IS NOT NULL)", nil
}

// lastCampaigns mira las N ultimas campanas que recibio el contacto. not_opened exige que
// haya recibido al menos N: un contacto recien llegado no es un inactivo.
func (c *compiler) lastCampaigns(r Rule, path string) (string, error) {
	if r.Op != OpOpened && r.Op != OpClicked && r.Op != OpNotOpened {
		return "", badOp(path, "last_campaigns", r.Op)
	}
	n, err := countValue(r.Value, MaxLastCampaigns, path)
	if err != nil {
		return "", err
	}
	limit := c.args.Add(n)
	recent := "(SELECT e.last_opened_at, e.last_clicked_at FROM " + engagementTable +
		" WHERE e.contact_id = c.id ORDER BY e.received_at DESC LIMIT " + limit + "::int) t"
	switch r.Op {
	case OpOpened:
		return "EXISTS (SELECT 1 FROM " + recent + " WHERE t.last_opened_at IS NOT NULL)", nil
	case OpClicked:
		return "EXISTS (SELECT 1 FROM " + recent + " WHERE t.last_clicked_at IS NOT NULL)", nil
	}
	return "(SELECT count(*) = " + limit + "::int AND count(t.last_opened_at) = 0 FROM " + recent + ")", nil
}

func (c *compiler) lastDays(r Rule, path string) (string, error) {
	if r.Op != OpOpened && r.Op != OpClicked {
		return "", badOp(path, "last_days", r.Op)
	}
	n, err := countValue(r.Value, MaxLastDays, path)
	if err != nil {
		return "", err
	}
	return "EXISTS (SELECT 1 FROM " + engagementTable + " WHERE e.contact_id = c.id AND " + engagementColumn(r.Op) +
		" >= now() - make_interval(days => " + c.args.Add(n) + "::int))", nil
}

// attribute compila una condicion sobre attributes.<key>. La clave viaja como argumento
// como cualquier valor; la igualdad usa contencion (@>) para aprovechar el indice GIN.
func (c *compiler) attribute(key string, typ AttrType, r Rule, path string) (string, error) {
	switch r.Op {
	case OpExists, OpNotExists:
		if err := noValue(r.Value, path); err != nil {
			return "", err
		}
		expr := "c.attributes ? " + c.args.Add(key) + "::text"
		if r.Op == OpNotExists {
			return "NOT (" + expr + ")", nil
		}
		return expr, nil
	}
	switch typ {
	case AttrString:
		return c.stringAttribute(key, r, path)
	case AttrNumber:
		return c.numberAttribute(key, r, path)
	case AttrBoolean:
		return c.booleanAttribute(key, r, path)
	case AttrDate:
		return c.dateAttribute(key, r, path)
	}
	return "", invalid(path, "tipo de atributo desconocido")
}

// containment compara por contencion: {key: value} con el valor convertido por cast.
func (c *compiler) containment(key string, value any, cast string, negate bool) string {
	k := c.args.Add(key)
	expr := "c.attributes @> jsonb_build_object(" + k + "::text, " + c.args.Add(value) + cast + ")"
	if negate {
		return "NOT (" + expr + ")"
	}
	return expr
}

func (c *compiler) stringAttribute(key string, r Rule, path string) (string, error) {
	switch r.Op {
	case OpEq, OpNeq:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return c.containment(key, v, "::text", r.Op == OpNeq), nil
	case OpContains, OpStartsWith:
		v, err := stringValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return "COALESCE(c.attributes ->> " + c.args.Add(key) + "::text ILIKE " + c.args.Add(likePattern(v, r.Op)) + ` ESCAPE '\', false)`, nil
	case OpIn:
		vs, err := stringValues(r.Value, path)
		if err != nil {
			return "", err
		}
		k := c.args.Add(key)
		return "COALESCE(jsonb_typeof(c.attributes -> " + k + "::text) = 'string' AND c.attributes ->> " + k + "::text = ANY(" + c.args.Add(vs) + "::text[]), false)", nil
	}
	return "", badOp(path, "attributes."+key+" (string)", r.Op)
}

func (c *compiler) numberAttribute(key string, r Rule, path string) (string, error) {
	switch r.Op {
	case OpEq, OpNeq:
		v, err := numberValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return c.containment(key, v, "::text::numeric", r.Op == OpNeq), nil
	case OpIn:
		vs, err := numberValues(r.Value, path)
		if err != nil {
			return "", err
		}
		return "COALESCE(" + c.numericExpr(key) + " = ANY(" + c.args.Add(vs) + "::text[]::numeric[]), false)", nil
	}
	cmp, ok := comparison(r.Op)
	if !ok {
		return "", badOp(path, "attributes."+key+" (number)", r.Op)
	}
	v, err := numberValue(r.Value, path)
	if err != nil {
		return "", err
	}
	return "COALESCE(" + c.numericExpr(key) + cmp + c.args.Add(v) + "::text::numeric, false)", nil
}

// numericExpr lee el atributo como numeric solo si el JSON guarda un numero: un valor de
// otro tipo da NULL (y la comparacion, false) en vez de romper la consulta entera.
func (c *compiler) numericExpr(key string) string {
	k := c.args.Add(key)
	return "(CASE WHEN jsonb_typeof(c.attributes -> " + k + "::text) = 'number' THEN (c.attributes ->> " + k + "::text)::numeric END)"
}

func (c *compiler) booleanAttribute(key string, r Rule, path string) (string, error) {
	if r.Op != OpEq && r.Op != OpNeq {
		return "", badOp(path, "attributes."+key+" (boolean)", r.Op)
	}
	var b bool
	if err := strictDecode(r.Value, &b); err != nil {
		return "", invalid(path, "value debe ser true o false")
	}
	return c.containment(key, b, "::boolean", r.Op == OpNeq), nil
}

func (c *compiler) dateAttribute(key string, r Rule, path string) (string, error) {
	switch r.Op {
	case OpEq, OpNeq:
		v, err := dateValue(r.Value, path)
		if err != nil {
			return "", err
		}
		return c.containment(key, v, "::text", r.Op == OpNeq), nil
	case OpIn:
		vs, err := stringValues(r.Value, path)
		if err != nil {
			return "", err
		}
		for _, v := range vs {
			if _, err := time.Parse(dateLayout, v); err != nil {
				return "", invalid(path, "cada valor debe ser una fecha AAAA-MM-DD")
			}
		}
		k := c.args.Add(key)
		return "COALESCE(jsonb_typeof(c.attributes -> " + k + "::text) = 'string' AND c.attributes ->> " + k + "::text = ANY(" + c.args.Add(vs) + "::text[]), false)", nil
	}
	cmp, ok := comparison(r.Op)
	if !ok {
		return "", badOp(path, "attributes."+key+" (date)", r.Op)
	}
	v, err := dateValue(r.Value, path)
	if err != nil {
		return "", err
	}
	k := c.args.Add(key)
	expr := "(CASE WHEN jsonb_typeof(c.attributes -> " + k + "::text) = 'string' AND c.attributes ->> " + k +
		`::text ~ '^\d{4}-\d{2}-\d{2}$' THEN (c.attributes ->> ` + k + "::text)::date END)"
	return "COALESCE(" + expr + cmp + c.args.Add(v) + "::text::date, false)", nil
}

// ── Valores ──────────────────────────────────────────────────────────────────

func comparison(op Op) (string, bool) {
	switch op {
	case OpGt:
		return " > ", true
	case OpGte:
		return " >= ", true
	case OpLt:
		return " < ", true
	case OpLte:
		return " <= ", true
	}
	return "", false
}

func badOp(path, field string, op Op) error {
	return invalid(path, "el operador %q no se admite para %s", clip(string(op)), field)
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func strictDecode(raw json.RawMessage, dst any) error {
	if isNull(raw) {
		return errors.New("sin valor")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("valor sobrante")
	}
	return nil
}

func noValue(raw json.RawMessage, path string) error {
	if !isNull(raw) {
		return invalid(path, "este operador no lleva value")
	}
	return nil
}

func checkString(s, path string) error {
	if strings.TrimSpace(s) == "" {
		return invalid(path, "value no puede estar vacio")
	}
	if utf8.RuneCountInString(s) > MaxStringValue || strings.ContainsRune(s, 0) {
		return invalid(path, "value admite como maximo %d caracteres", MaxStringValue)
	}
	return nil
}

func stringValue(raw json.RawMessage, path string) (string, error) {
	var s string
	if err := strictDecode(raw, &s); err != nil {
		return "", invalid(path, "value debe ser texto")
	}
	if err := checkString(s, path); err != nil {
		return "", err
	}
	return s, nil
}

func stringValues(raw json.RawMessage, path string) ([]string, error) {
	var vs []string
	if err := strictDecode(raw, &vs); err != nil {
		return nil, invalid(path, "value debe ser una lista de textos")
	}
	if len(vs) == 0 {
		return nil, invalid(path, "value no puede ser una lista vacia")
	}
	if len(vs) > MaxInValues {
		return nil, invalid(path, "in admite como maximo %d valores", MaxInValues)
	}
	for _, v := range vs {
		if err := checkString(v, path); err != nil {
			return nil, err
		}
	}
	return vs, nil
}

func numberLiteral(n json.Number, path string) (string, error) {
	if len(n) > maxNumberLength {
		return "", invalid(path, "numero demasiado largo")
	}
	f, err := strconv.ParseFloat(n.String(), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", invalid(path, "value debe ser un numero finito")
	}
	return n.String(), nil
}

// numberValue exige un numero JSON. Se decodifica a any y no a json.Number porque este
// acepta tambien un texto con forma de numero ("7"), y el tipo lo decide el JSON.
func numberValue(raw json.RawMessage, path string) (string, error) {
	var v any
	if err := strictDecode(raw, &v); err != nil {
		return "", invalid(path, "value debe ser un numero")
	}
	n, ok := v.(json.Number)
	if !ok {
		return "", invalid(path, "value debe ser un numero")
	}
	return numberLiteral(n, path)
}

func numberValues(raw json.RawMessage, path string) ([]string, error) {
	var vs []any
	if err := strictDecode(raw, &vs); err != nil {
		return nil, invalid(path, "value debe ser una lista de numeros")
	}
	if len(vs) == 0 {
		return nil, invalid(path, "value no puede ser una lista vacia")
	}
	if len(vs) > MaxInValues {
		return nil, invalid(path, "in admite como maximo %d valores", MaxInValues)
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		n, ok := v.(json.Number)
		if !ok {
			return nil, invalid(path, "value debe ser una lista de numeros")
		}
		s, err := numberLiteral(n, path)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// countValue exige un entero JSON entre 1 y max.
func countValue(raw json.RawMessage, max int, path string) (int, error) {
	var v any
	if err := strictDecode(raw, &v); err != nil {
		return 0, invalid(path, "value debe ser un entero entre 1 y %d", max)
	}
	num, ok := v.(json.Number)
	if !ok {
		return 0, invalid(path, "value debe ser un entero entre 1 y %d", max)
	}
	n, err := strconv.Atoi(num.String())
	if err != nil || n < 1 || n > max {
		return 0, invalid(path, "value debe ser un entero entre 1 y %d", max)
	}
	return n, nil
}

func dateValue(raw json.RawMessage, path string) (string, error) {
	var s string
	if err := strictDecode(raw, &s); err != nil {
		return "", invalid(path, "value debe ser una fecha AAAA-MM-DD")
	}
	if _, err := time.Parse(dateLayout, s); err != nil {
		return "", invalid(path, "value debe ser una fecha AAAA-MM-DD")
	}
	return s, nil
}

// timestampValue acepta RFC 3339 o una fecha AAAA-MM-DD (inicio del dia en UTC).
func timestampValue(raw json.RawMessage, path string) (time.Time, error) {
	var s string
	if err := strictDecode(raw, &s); err != nil {
		return time.Time{}, invalid(path, "value debe ser una fecha")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(dateLayout, s); err == nil {
		return t, nil
	}
	return time.Time{}, invalid(path, "value debe ser RFC 3339 o AAAA-MM-DD")
}

// normalizeTag aplica la forma canonica de las etiquetas (recortada, minusculas). Una
// etiqueta que no puede existir no es un error: simplemente no selecciona a nadie.
func normalizeTag(s, path string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" || utf8.RuneCountInString(t) > 64 {
		return "", invalid(path, "etiqueta no valida")
	}
	return t, nil
}

func tagValue(raw json.RawMessage, path string) (string, error) {
	v, err := stringValue(raw, path)
	if err != nil {
		return "", err
	}
	return normalizeTag(v, path)
}

func likePattern(v string, op Op) string {
	escaped := likeEscaper.Replace(v)
	if op == OpStartsWith {
		return escaped + "%"
	}
	return "%" + escaped + "%"
}

// clip acota el texto del usuario que aparece en un mensaje de error.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= 64 {
		return s
	}
	return string([]rune(s)[:64]) + "..."
}
