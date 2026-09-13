package segment

import "sort"

// Arity dice cuantos valores lleva un operador: ninguno (exists), uno o una lista (in).
type Arity string

const (
	ArityNone Arity = "none"
	ArityOne  Arity = "one"
	ArityMany Arity = "many"
)

// operators es el catalogo completo de operadores, en el orden en que se presentan.
var operators = []Op{
	OpEq, OpNeq, OpContains, OpStartsWith, OpGt, OpGte, OpLt, OpLte, OpIn,
	OpExists, OpNotExists, OpHasTag, OpInList, OpNotInList,
}

// Arity es el numero de valores que exige el operador.
func (o Op) Arity() Arity {
	switch o {
	case OpExists, OpNotExists:
		return ArityNone
	case OpIn:
		return ArityMany
	}
	return ArityOne
}

// kindOperators y attrOperators son los operadores que acepta el compilador por clase de
// campo y por tipo de atributo. catalog_test.go comprueba contra Compile que cada entrada
// compila y que ningun otro operador lo hace: la tabla no puede desviarse del compilador.
var kindOperators = map[kind][]Op{
	kindEmail:        {OpEq, OpNeq, OpContains, OpStartsWith, OpIn},
	kindText:         {OpEq, OpNeq, OpContains, OpStartsWith, OpIn, OpExists, OpNotExists},
	kindNullableText: {OpEq, OpNeq, OpContains, OpStartsWith, OpIn, OpExists, OpNotExists},
	kindEnum:         {OpEq, OpNeq, OpIn},
	kindTimestamp:    {OpGt, OpGte, OpLt, OpLte},
	kindTags:         {OpHasTag, OpNeq, OpIn, OpExists, OpNotExists},
	kindList:         {OpInList, OpNotInList},
}

var attrOperators = map[AttrType][]Op{
	AttrString:  {OpEq, OpNeq, OpContains, OpStartsWith, OpIn, OpExists, OpNotExists},
	AttrNumber:  {OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpIn, OpExists, OpNotExists},
	AttrBoolean: {OpEq, OpNeq, OpExists, OpNotExists},
	AttrDate:    {OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpIn, OpExists, OpNotExists},
}

// attrTypes es el orden en que se presentan los tipos de atributo.
var attrTypes = []AttrType{AttrString, AttrNumber, AttrBoolean, AttrDate}

// ValueType es la forma del valor de un campo fijo. Los atributos usan su AttrType.
type ValueType string

const (
	ValueEmail     ValueType = "email"
	ValueText      ValueType = "text"
	ValueEnum      ValueType = "enum"
	ValueTimestamp ValueType = "timestamp"
	ValueTag       ValueType = "tag"
	ValueList      ValueType = "list"
)

var kindValueTypes = map[kind]ValueType{
	kindEmail:        ValueEmail,
	kindText:         ValueText,
	kindNullableText: ValueText,
	kindEnum:         ValueEnum,
	kindTimestamp:    ValueTimestamp,
	kindTags:         ValueTag,
	kindList:         ValueList,
}

// Catalog describe lo que una definicion puede usar con el esquema de una empresa: el
// editor de segmentos se construye con esto y no con una copia de las reglas.
type Catalog struct {
	Match           []Match             `json:"match"`
	AttributePrefix string              `json:"attribute_prefix"`
	Operators       []OperatorInfo      `json:"operators"`
	Fields          []FieldInfo         `json:"fields"`
	AttributeTypes  []AttributeTypeInfo `json:"attribute_types"`
	Attributes      []AttributeInfo     `json:"attributes"`
	Limits          Limits              `json:"limits"`
}

type OperatorInfo struct {
	Op    Op    `json:"op"`
	Arity Arity `json:"arity"`
}

// FieldInfo es un campo fijo. Values solo existe en los enumerados.
type FieldInfo struct {
	Field     string    `json:"field"`
	ValueType ValueType `json:"value_type"`
	Operators []Op      `json:"operators"`
	Values    []string  `json:"values,omitempty"`
}

type AttributeTypeInfo struct {
	Type      AttrType `json:"type"`
	Operators []Op     `json:"operators"`
}

type AttributeInfo struct {
	Key  string   `json:"key"`
	Type AttrType `json:"type"`
}

type Limits struct {
	MaxDepth           int `json:"max_depth"`
	MaxRules           int `json:"max_rules"`
	MaxInValues        int `json:"max_in_values"`
	MaxStringValue     int `json:"max_string_value"`
	MaxDefinitionBytes int `json:"max_definition_bytes"`
}

// Describe arma el catalogo con el esquema dado. Los atributos salen ordenados por clave.
func Describe(schema Schema) Catalog {
	c := Catalog{
		Match:           []Match{MatchAll, MatchAny},
		AttributePrefix: attrPrefix,
		Operators:       make([]OperatorInfo, 0, len(operators)),
		Fields:          make([]FieldInfo, 0, len(fieldSpecs)),
		AttributeTypes:  make([]AttributeTypeInfo, 0, len(attrTypes)),
		Attributes:      make([]AttributeInfo, 0, len(schema.Attributes)),
		Limits: Limits{
			MaxDepth: MaxDepth, MaxRules: MaxRules, MaxInValues: MaxInValues,
			MaxStringValue: MaxStringValue, MaxDefinitionBytes: MaxDefinitionBytes,
		},
	}
	for _, op := range operators {
		c.Operators = append(c.Operators, OperatorInfo{Op: op, Arity: op.Arity()})
	}
	for _, f := range fieldSpecs {
		info := FieldInfo{Field: f.name, ValueType: kindValueTypes[f.kind], Operators: cloneOps(kindOperators[f.kind])}
		if f.kind == kindEnum {
			info.Values = append([]string{}, schema.Enums[f.name]...)
		}
		c.Fields = append(c.Fields, info)
	}
	for _, t := range attrTypes {
		c.AttributeTypes = append(c.AttributeTypes, AttributeTypeInfo{Type: t, Operators: cloneOps(attrOperators[t])})
	}
	for key, typ := range schema.Attributes {
		c.Attributes = append(c.Attributes, AttributeInfo{Key: key, Type: typ})
	}
	sort.Slice(c.Attributes, func(i, j int) bool { return c.Attributes[i].Key < c.Attributes[j].Key })
	return c
}

func cloneOps(ops []Op) []Op {
	return append([]Op{}, ops...)
}
