package segment

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

const sampleListID = "0b6c0d2e-9a5f-4a57-8a0e-2f7f1c3d4e5a"

// sample es un valor valido de la forma dada. Con un operador que el campo no admite, la
// definicion debe fallar igual: el error tiene que venir del operador, no del valor.
func sample(valueType string, enumValues []string) any {
	switch valueType {
	case string(ValueEmail):
		return "ana@acme.test"
	case string(ValueEnum):
		return enumValues[0]
	case string(ValueTimestamp), string(AttrDate):
		return "2026-01-01"
	case string(ValueTag):
		return "vip"
	case string(ValueList):
		return sampleListID
	case string(AttrNumber):
		return 5
	case string(AttrBoolean):
		return true
	}
	return "ana"
}

func ruleJSON(t *testing.T, field string, op Op, value any) []byte {
	t.Helper()
	leaf := map[string]any{"field": field, "op": op}
	switch op.Arity() {
	case ArityOne:
		leaf["value"] = value
	case ArityMany:
		leaf["value"] = []any{value}
	}
	raw, err := json.Marshal(map[string]any{"match": "all", "rules": []any{leaf}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func compiles(t *testing.T, raw []byte) error {
	t.Helper()
	def, err := Parse(raw)
	if err != nil {
		return err
	}
	return Validate(def, testSchema())
}

// El catalogo es el contrato del editor de segmentos: cada operador que anuncia para un
// campo compila, y ninguno que calla lo hace.
func TestCatalogoCoincideConElCompilador(t *testing.T) {
	cat := Describe(testSchema())
	for _, f := range cat.Fields {
		for _, op := range operators {
			raw := ruleJSON(t, f.Field, op, sample(string(f.ValueType), f.Values))
			err := compiles(t, raw)
			want := slices.Contains(f.Operators, op)
			if want && err != nil {
				t.Errorf("%s %s anunciado pero no compila: %v", f.Field, op, err)
			}
			if !want && err == nil {
				t.Errorf("%s %s compila pero el catalogo no lo anuncia", f.Field, op)
			}
		}
	}
	byType := map[AttrType]string{}
	for _, a := range cat.Attributes {
		byType[a.Type] = a.Key
	}
	for _, at := range cat.AttributeTypes {
		key, ok := byType[at.Type]
		if !ok {
			t.Fatalf("el esquema de prueba no declara un atributo %s", at.Type)
		}
		for _, op := range operators {
			raw := ruleJSON(t, cat.AttributePrefix+key, op, sample(string(at.Type), nil))
			err := compiles(t, raw)
			want := slices.Contains(at.Operators, op)
			if want && err != nil {
				t.Errorf("atributo %s %s anunciado pero no compila: %v", at.Type, op, err)
			}
			if !want && err == nil {
				t.Errorf("atributo %s %s compila pero el catalogo no lo anuncia", at.Type, op)
			}
		}
	}
}

func TestCatalogoCubreTodosLosCamposYTipos(t *testing.T) {
	cat := Describe(testSchema())
	if len(cat.Fields) != len(fixedFields) {
		t.Fatalf("campos del catalogo %d, del compilador %d", len(cat.Fields), len(fixedFields))
	}
	for _, f := range cat.Fields {
		if _, ok := fixedFields[f.Field]; !ok {
			t.Errorf("campo %q anunciado y desconocido para el compilador", f.Field)
		}
		if len(f.Operators) == 0 || f.ValueType == "" {
			t.Errorf("campo %q sin operadores o sin tipo de valor", f.Field)
		}
	}
	for _, at := range []AttrType{AttrString, AttrNumber, AttrBoolean, AttrDate} {
		if !slices.ContainsFunc(cat.AttributeTypes, func(i AttributeTypeInfo) bool { return i.Type == at }) {
			t.Errorf("falta el tipo de atributo %s", at)
		}
	}
	if len(cat.Operators) != len(operators) {
		t.Errorf("operadores %d, esperados %d", len(cat.Operators), len(operators))
	}
}

func TestCatalogoTomaLosEnumeradosYAtributosDelEsquema(t *testing.T) {
	schema := testSchema()
	cat := Describe(schema)
	for _, f := range cat.Fields {
		if f.ValueType != ValueEnum {
			if f.Values != nil {
				t.Errorf("%s no es enumerado y lleva valores", f.Field)
			}
			continue
		}
		if !slices.Equal(f.Values, schema.Enums[f.Field]) {
			t.Errorf("%s: valores %v, esquema %v", f.Field, f.Values, schema.Enums[f.Field])
		}
	}
	keys := make([]string, 0, len(cat.Attributes))
	for _, a := range cat.Attributes {
		keys = append(keys, a.Key)
		if a.Type != schema.Attributes[a.Key] {
			t.Errorf("atributo %s: tipo %s, esquema %s", a.Key, a.Type, schema.Attributes[a.Key])
		}
	}
	if !slices.IsSorted(keys) || len(keys) != len(schema.Attributes) {
		t.Errorf("atributos no ordenados o incompletos: %v", keys)
	}
	if cat.Limits.MaxDepth != MaxDepth || cat.Limits.MaxRules != MaxRules || cat.Limits.MaxInValues != MaxInValues {
		t.Errorf("limites distintos de las constantes del compilador: %+v", cat.Limits)
	}
}

// Sin atributos declarados el JSON lleva listas vacias, nunca null: el editor itera sin
// comprobar nada.
func TestCatalogoSinAtributosSerializaListasVacias(t *testing.T) {
	raw, err := json.Marshal(Describe(Schema{Enums: testSchema().Enums}))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"match", "operators", "fields", "attribute_types", "attributes"} {
		if string(out[key]) == "null" || len(out[key]) == 0 {
			t.Errorf("%s llega como %s", key, out[key])
		}
	}
	if string(out["attributes"]) != "[]" {
		t.Errorf("attributes: %s", fmt.Sprint(string(out["attributes"])))
	}
}
