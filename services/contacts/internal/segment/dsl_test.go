package segment

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testSchema() Schema {
	return Schema{
		Attributes: map[string]AttrType{
			"plan":     AttrString,
			"score":    AttrNumber,
			"vip":      AttrBoolean,
			"birthday": AttrDate,
		},
		Enums: map[string][]string{
			"status":  {"active", "unsubscribed", "bounced", "complained"},
			"source":  {"api", "import", "form", "integration"},
			"consent": {"granted", "revoked", "pending", "none"},
		},
	}
}

func mustParse(t *testing.T, s string) Definition {
	t.Helper()
	def, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse(%s): %v", s, err)
	}
	return def
}

func compile(t *testing.T, s string) (string, []any, error) {
	t.Helper()
	def, err := Parse([]byte(s))
	if err != nil {
		return "", nil, err
	}
	args := NewArgs()
	sql, err := Compile(def, testSchema(), args)
	return sql, args.Values(), err
}

func mustCompile(t *testing.T, s string) (string, []any) {
	t.Helper()
	sql, args, err := compile(t, s)
	if err != nil {
		t.Fatalf("Compile(%s): %v", s, err)
	}
	return sql, args
}

func expectInvalid(t *testing.T, s string) {
	t.Helper()
	if _, _, err := compile(t, s); !errors.Is(err, ErrInvalid) {
		t.Fatalf("se esperaba ErrInvalid para %s, hubo %v", s, err)
	}
}

func TestCompilaCadaCampoYOperador(t *testing.T) {
	cases := []struct {
		name string
		rule string
		sql  string
		args []any
	}{
		{"email eq normaliza", `{"field":"email","op":"eq","value":" Ana@Example.COM "}`, `c.email = $1::text`, []any{"ana@example.com"}},
		{"email neq", `{"field":"email","op":"neq","value":"a@b.co"}`, `c.email <> $1::text`, []any{"a@b.co"}},
		{"email contains", `{"field":"email","op":"contains","value":"Example"}`, `c.email LIKE $1 ESCAPE '\'`, []any{"%example%"}},
		{"email in", `{"field":"email","op":"in","value":["A@x.co","b@x.co"]}`, `c.email = ANY($1::text[])`, []any{[]string{"a@x.co", "b@x.co"}}},
		{"first_name eq", `{"field":"first_name","op":"eq","value":"Ana"}`, `lower(c.first_name) = lower($1::text)`, []any{"Ana"}},
		{"last_name starts_with", `{"field":"last_name","op":"starts_with","value":"Pe"}`, `c.last_name ILIKE $1 ESCAPE '\'`, []any{"Pe%"}},
		{"first_name in", `{"field":"first_name","op":"in","value":["Ana","Luis"]}`, `lower(c.first_name) IN (SELECT lower(v) FROM unnest($1::text[]) AS v)`, []any{[]string{"Ana", "Luis"}}},
		{"first_name exists", `{"field":"first_name","op":"exists"}`, `c.first_name <> ''`, nil},
		{"last_name not_exists", `{"field":"last_name","op":"not_exists","value":null}`, `c.last_name = ''`, nil},
		{"locale eq", `{"field":"locale","op":"eq","value":"es-PE"}`, `COALESCE(lower(c.locale) = lower($1::text), false)`, []any{"es-PE"}},
		{"locale neq", `{"field":"locale","op":"neq","value":"es"}`, `lower(c.locale) IS DISTINCT FROM lower($1::text)`, []any{"es"}},
		{"timezone starts_with", `{"field":"timezone","op":"starts_with","value":"America/"}`, `COALESCE(c.timezone ILIKE $1 ESCAPE '\', false)`, []any{"America/%"}},
		{"locale in", `{"field":"locale","op":"in","value":["es","pt-BR"]}`, `COALESCE(lower(c.locale) IN (SELECT lower(v) FROM unnest($1::text[]) AS v), false)`, []any{[]string{"es", "pt-BR"}}},
		{"timezone exists", `{"field":"timezone","op":"exists"}`, `c.timezone IS NOT NULL`, nil},
		{"locale not_exists", `{"field":"locale","op":"not_exists"}`, `c.locale IS NULL`, nil},
		{"status eq", `{"field":"status","op":"eq","value":"active"}`, `c.status = $1::text`, []any{"active"}},
		{"source in", `{"field":"source","op":"in","value":["api","form"]}`, `c.source = ANY($1::text[])`, []any{[]string{"api", "form"}}},
		{"consent neq", `{"field":"consent","op":"neq","value":"granted"}`, `c.marketing_consent <> $1::text`, []any{"granted"}},
		{"created_at gte fecha", `{"field":"created_at","op":"gte","value":"2026-01-01"}`, `c.created_at >= $1::timestamptz`, []any{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"created_at lt rfc3339", `{"field":"created_at","op":"lt","value":"2026-01-01T05:00:00-05:00"}`, `c.created_at < $1::timestamptz`, []any{time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)}},
		{"tags has_tag normaliza", `{"field":"tags","op":"has_tag","value":" VIP "}`, `c.tags @> $1::text[]`, []any{[]string{"vip"}}},
		{"tags neq", `{"field":"tags","op":"neq","value":"baja"}`, `NOT (c.tags @> $1::text[])`, []any{[]string{"baja"}}},
		{"tags in", `{"field":"tags","op":"in","value":["A","b"]}`, `c.tags && $1::text[]`, []any{[]string{"a", "b"}}},
		{"tags exists", `{"field":"tags","op":"exists"}`, `cardinality(c.tags) > 0`, nil},
		{"tags not_exists", `{"field":"tags","op":"not_exists"}`, `cardinality(c.tags) = 0`, nil},
		{"list in_list", `{"field":"list","op":"in_list","value":"0B6F1F7C-1D2E-4C3B-9A8F-7E6D5C4B3A21"}`,
			`EXISTS (SELECT 1 FROM contacts.list_members lm WHERE lm.list_id = $1::uuid AND lm.contact_id = c.id)`,
			[]any{"0b6f1f7c-1d2e-4c3b-9a8f-7e6d5c4b3a21"}},
		{"list not_in_list", `{"field":"list","op":"not_in_list","value":"0b6f1f7c-1d2e-4c3b-9a8f-7e6d5c4b3a21"}`,
			`NOT EXISTS (SELECT 1 FROM contacts.list_members lm WHERE lm.list_id = $1::uuid AND lm.contact_id = c.id)`,
			[]any{"0b6f1f7c-1d2e-4c3b-9a8f-7e6d5c4b3a21"}},
		{"attr string eq", `{"field":"attributes.plan","op":"eq","value":"pro"}`, `c.attributes @> jsonb_build_object($1::text, $2::text)`, []any{"plan", "pro"}},
		{"attr string neq", `{"field":"attributes.plan","op":"neq","value":"pro"}`, `NOT (c.attributes @> jsonb_build_object($1::text, $2::text))`, []any{"plan", "pro"}},
		{"attr string contains", `{"field":"attributes.plan","op":"contains","value":"r"}`, `COALESCE(c.attributes ->> $1::text ILIKE $2 ESCAPE '\', false)`, []any{"plan", "%r%"}},
		{"attr string in", `{"field":"attributes.plan","op":"in","value":["pro","free"]}`,
			`COALESCE(jsonb_typeof(c.attributes -> $1::text) = 'string' AND c.attributes ->> $1::text = ANY($2::text[]), false)`,
			[]any{"plan", []string{"pro", "free"}}},
		{"attr exists", `{"field":"attributes.plan","op":"exists"}`, `c.attributes ? $1::text`, []any{"plan"}},
		{"attr not_exists", `{"field":"attributes.vip","op":"not_exists"}`, `NOT (c.attributes ? $1::text)`, []any{"vip"}},
		{"attr number eq", `{"field":"attributes.score","op":"eq","value":10.5}`, `c.attributes @> jsonb_build_object($1::text, $2::text::numeric)`, []any{"score", "10.5"}},
		{"attr number gt", `{"field":"attributes.score","op":"gt","value":7}`,
			`COALESCE((CASE WHEN jsonb_typeof(c.attributes -> $1::text) = 'number' THEN (c.attributes ->> $1::text)::numeric END) > $2::text::numeric, false)`,
			[]any{"score", "7"}},
		{"attr number in", `{"field":"attributes.score","op":"in","value":[1,2.5]}`,
			`COALESCE((CASE WHEN jsonb_typeof(c.attributes -> $1::text) = 'number' THEN (c.attributes ->> $1::text)::numeric END) = ANY($2::text[]::numeric[]), false)`,
			[]any{"score", []string{"1", "2.5"}}},
		{"attr boolean eq", `{"field":"attributes.vip","op":"eq","value":true}`, `c.attributes @> jsonb_build_object($1::text, $2::boolean)`, []any{"vip", true}},
		{"attr boolean neq", `{"field":"attributes.vip","op":"neq","value":false}`, `NOT (c.attributes @> jsonb_build_object($1::text, $2::boolean))`, []any{"vip", false}},
		{"attr date eq", `{"field":"attributes.birthday","op":"eq","value":"1990-05-01"}`, `c.attributes @> jsonb_build_object($1::text, $2::text)`, []any{"birthday", "1990-05-01"}},
		{"attr date lte", `{"field":"attributes.birthday","op":"lte","value":"2000-01-01"}`,
			`COALESCE((CASE WHEN jsonb_typeof(c.attributes -> $1::text) = 'string' AND c.attributes ->> $1::text ~ '^\d{4}-\d{2}-\d{2}$' THEN (c.attributes ->> $1::text)::date END) <= $2::text::date, false)`,
			[]any{"birthday", "2000-01-01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := mustCompile(t, `{"match":"all","rules":[`+tc.rule+`]}`)
			if want := "(" + tc.sql + ")"; sql != want {
				t.Fatalf("sql\n got: %s\nwant: %s", sql, want)
			}
			if len(tc.args) == 0 && len(args) == 0 {
				return
			}
			if !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("args\n got: %#v\nwant: %#v", args, tc.args)
			}
		})
	}
}

func TestGruposAnidadosYNumeracionDePlaceholders(t *testing.T) {
	def := mustParse(t, `{"match":"any","rules":[
		{"field":"status","op":"eq","value":"active"},
		{"match":"all","rules":[
			{"field":"tags","op":"has_tag","value":"vip"},
			{"field":"attributes.score","op":"gte","value":5}
		]}
	]}`)
	// El llamador ya uso $1 y $2: la definicion debe seguir en $3.
	args := NewArgs("tenant", "cursor")
	sql, err := Compile(def, testSchema(), args)
	if err != nil {
		t.Fatal(err)
	}
	want := `(c.status = $3::text OR (c.tags @> $4::text[] AND COALESCE((CASE WHEN jsonb_typeof(c.attributes -> $5::text) = 'number' THEN (c.attributes ->> $5::text)::numeric END) >= $6::text::numeric, false)))`
	if sql != want {
		t.Fatalf("sql\n got: %s\nwant: %s", sql, want)
	}
	if n := len(args.Values()); n != 6 {
		t.Fatalf("args: %d", n)
	}

	// Dos definiciones en la misma Args no reutilizan placeholders.
	second := mustParse(t, `{"match":"all","rules":[{"field":"email","op":"eq","value":"x@y.co"}]}`)
	sql2, err := Compile(second, testSchema(), args)
	if err != nil || sql2 != `(c.email = $7::text)` {
		t.Fatalf("segunda definicion: %s err=%v", sql2, err)
	}
}

func TestInyeccionNuncaLlegaAlSQL(t *testing.T) {
	payloads := []string{
		`'; DROP TABLE contacts.contacts; --`,
		`") OR 1=1 --`,
		`$1`,
		`\'; SELECT pg_sleep(10); --`,
	}
	for _, p := range payloads {
		quoted, _ := json.Marshal(p)
		rules := []string{
			fmt.Sprintf(`{"field":"email","op":"eq","value":%s}`, quoted),
			fmt.Sprintf(`{"field":"first_name","op":"contains","value":%s}`, quoted),
			fmt.Sprintf(`{"field":"attributes.plan","op":"eq","value":%s}`, quoted),
			fmt.Sprintf(`{"field":"attributes.plan","op":"in","value":[%s]}`, quoted),
			fmt.Sprintf(`{"field":"tags","op":"has_tag","value":%s}`, quoted),
		}
		for _, r := range rules {
			sql, args := mustCompile(t, `{"match":"all","rules":[`+r+`]}`)
			if strings.Contains(sql, "DROP") || strings.Contains(sql, "pg_sleep") || strings.Contains(sql, "1=1") {
				t.Fatalf("el valor llego al SQL: %s", sql)
			}
			if len(args) == 0 {
				t.Fatalf("el valor no viajo como argumento: %s", sql)
			}
		}
	}

	// Claves y campos: nada fuera de la lista blanca se acepta, ni siquiera parecido.
	for _, field := range []string{
		`attributes.plan'); DROP TABLE x; --`,
		`attributes.Plan`,
		`attributes.`,
		`attributes.plan.sub`,
		`attributes.1plan`,
		`email; DROP TABLE x`,
		`c.email`,
		`EMAIL`,
		`tenant_id`,
		`id`,
		`marketing_consent`,
		`attributes`,
	} {
		quoted, _ := json.Marshal(field)
		expectInvalid(t, fmt.Sprintf(`{"match":"all","rules":[{"field":%s,"op":"exists"}]}`, quoted))
	}
	// Operadores inventados.
	expectInvalid(t, `{"match":"all","rules":[{"field":"email","op":"= 'x' OR 1=1 --","value":"a"}]}`)
}

func TestTiposYOperadores(t *testing.T) {
	invalids := []string{
		// Atributo no declarado.
		`{"field":"attributes.unknown","op":"eq","value":"x"}`,
		// Operador que no cuadra con el tipo.
		`{"field":"attributes.plan","op":"gt","value":"a"}`,
		`{"field":"attributes.vip","op":"contains","value":"t"}`,
		`{"field":"attributes.vip","op":"in","value":[true]}`,
		`{"field":"attributes.score","op":"contains","value":"1"}`,
		`{"field":"email","op":"gt","value":"a"}`,
		`{"field":"email","op":"exists"}`,
		`{"field":"status","op":"contains","value":"act"}`,
		`{"field":"created_at","op":"eq","value":"2026-01-01"}`,
		`{"field":"tags","op":"eq","value":"vip"}`,
		`{"field":"list","op":"eq","value":"0b6f1f7c-1d2e-4c3b-9a8f-7e6d5c4b3a21"}`,
		`{"field":"first_name","op":"has_tag","value":"x"}`,
		// Valor que no cuadra con el tipo.
		`{"field":"attributes.score","op":"gt","value":"7"}`,
		`{"field":"attributes.score","op":"eq","value":1e400}`,
		`{"field":"attributes.score","op":"eq","value":true}`,
		`{"field":"attributes.vip","op":"eq","value":"true"}`,
		`{"field":"attributes.birthday","op":"gt","value":"01/05/1990"}`,
		`{"field":"attributes.birthday","op":"eq","value":"1990-02-30"}`,
		`{"field":"attributes.birthday","op":"in","value":["1990-13-01"]}`,
		`{"field":"created_at","op":"gt","value":"ayer"}`,
		`{"field":"created_at","op":"gt","value":1700000000}`,
		`{"field":"email","op":"eq","value":42}`,
		`{"field":"email","op":"eq","value":""}`,
		`{"field":"email","op":"eq","value":"   "}`,
		`{"field":"email","op":"eq"}`,
		`{"field":"email","op":"in","value":"a@b.co"}`,
		// Enumerados fuera de su lista.
		`{"field":"status","op":"eq","value":"deleted"}`,
		`{"field":"consent","op":"in","value":["granted","maybe"]}`,
		`{"field":"source","op":"neq","value":"csv"}`,
		// exists con valor.
		`{"field":"locale","op":"exists","value":"es"}`,
		`{"field":"attributes.plan","op":"not_exists","value":"x"}`,
		// Lista con id que no es uuid.
		`{"field":"list","op":"in_list","value":"1; DROP TABLE x"}`,
	}
	for _, r := range invalids {
		expectInvalid(t, `{"match":"all","rules":[`+r+`]}`)
	}
}

func TestEscapeLike(t *testing.T) {
	_, args := mustCompile(t, `{"match":"all","rules":[{"field":"first_name","op":"contains","value":"50%_off\\"}]}`)
	if got, want := args[0], `%50\%\_off\\%`; got != want {
		t.Fatalf("patron contains: %q, se esperaba %q", got, want)
	}
	_, args = mustCompile(t, `{"match":"all","rules":[{"field":"email","op":"starts_with","value":"A_B%"}]}`)
	if got, want := args[0], `a\_b\%%`; got != want {
		t.Fatalf("patron starts_with: %q, se esperaba %q", got, want)
	}
	_, args = mustCompile(t, `{"match":"all","rules":[{"field":"attributes.plan","op":"starts_with","value":"%"}]}`)
	if got, want := args[1], `\%%`; got != want {
		t.Fatalf("patron de atributo: %q, se esperaba %q", got, want)
	}
}

func nestedDefinition(depth int) string {
	leaf := `{"field":"tags","op":"exists"}`
	s := leaf
	for i := 1; i < depth; i++ {
		s = `{"match":"all","rules":[` + s + `]}`
	}
	return `{"match":"all","rules":[` + s + `]}`
}

func TestLimites(t *testing.T) {
	if _, _, err := compile(t, nestedDefinition(MaxDepth)); err != nil {
		t.Fatalf("profundidad %d debe valer: %v", MaxDepth, err)
	}
	expectInvalid(t, nestedDefinition(MaxDepth+1))

	rules := func(n int) string {
		rs := make([]string, n)
		for i := range rs {
			rs[i] = `{"field":"tags","op":"exists"}`
		}
		return `{"match":"any","rules":[` + strings.Join(rs, ",") + `]}`
	}
	if _, _, err := compile(t, rules(MaxRules)); err != nil {
		t.Fatalf("%d reglas deben valer: %v", MaxRules, err)
	}
	expectInvalid(t, rules(MaxRules+1))
	// Los subgrupos cuentan como reglas.
	expectInvalid(t, `{"match":"all","rules":[`+strings.TrimSuffix(strings.TrimPrefix(rules(MaxRules), `{"match":"any","rules":[`), `]}`)+`,{"match":"all","rules":[{"field":"tags","op":"exists"}]}]}`)

	values := func(n int) string {
		vs := make([]string, n)
		for i := range vs {
			vs[i] = fmt.Sprintf(`"v%d"`, i)
		}
		return `{"match":"all","rules":[{"field":"attributes.plan","op":"in","value":[` + strings.Join(vs, ",") + `]}]}`
	}
	if _, _, err := compile(t, values(MaxInValues)); err != nil {
		t.Fatalf("%d valores deben valer: %v", MaxInValues, err)
	}
	expectInvalid(t, values(MaxInValues+1))
	expectInvalid(t, `{"match":"all","rules":[{"field":"email","op":"in","value":[]}]}`)

	expectInvalid(t, `{"match":"all","rules":[{"field":"first_name","op":"eq","value":"`+strings.Repeat("a", MaxStringValue+1)+`"}]}`)
	if _, err := Parse([]byte(`{"match":"all","rules":[{"field":"email","op":"eq","value":"` + strings.Repeat("a", MaxDefinitionBytes) + `"}]}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("una definicion enorme debe rechazarse: %v", err)
	}
}

func TestEstructuraEstricta(t *testing.T) {
	for _, s := range []string{
		`[]`,
		`"all"`,
		`{"match":"all"}`,
		`{"match":"all","rules":[]}`,
		`{"match":"some","rules":[{"field":"tags","op":"exists"}]}`,
		`{"rules":[{"field":"tags","op":"exists"}]}`,
		`{"match":"all","rules":[{"field":"tags","op":"exists","extra":1}]}`,
		`{"match":"all","rules":[{"field":"tags","op":"exists","rules":[]}]}`,
		`{"match":"all","rules":[{"match":"all","rules":[{"field":"tags","op":"exists"}],"field":"email"}]}`,
		`{"match":"all","rules":[{"match":"all","rules":[]}]}`,
		`{"match":"all","rules":[42]}`,
		`{"match":"all","rules":{"field":"tags"}}`,
		`{"match":"all","rules":[{"field":"tags","op":"exists"}],"sql":"1=1"}`,
		`{"match":"all","rules":[{"field":7,"op":"exists"}]}`,
	} {
		expectInvalid(t, s)
	}
}

func TestSerializacionCanonicaYReferencias(t *testing.T) {
	src := `{"match":"all","rules":[
		{"field":"attributes.plan","op":"eq","value": "pro"},
		{"match":"any","rules":[
			{"field":"list","op":"in_list","value":"0B6F1F7C-1D2E-4C3B-9A8F-7E6D5C4B3A21"},
			{"field":"attributes.score","op":"gt","value":3},
			{"field":"attributes.plan","op":"exists"}
		]}
	]}`
	def := mustParse(t, src)
	out, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"match":"all","rules":[{"field":"attributes.plan","op":"eq","value":"pro"},{"match":"any","rules":[{"field":"list","op":"in_list","value":"0B6F1F7C-1D2E-4C3B-9A8F-7E6D5C4B3A21"},{"field":"attributes.score","op":"gt","value":3},{"field":"attributes.plan","op":"exists"}]}]}`
	if string(out) != want {
		t.Fatalf("forma canonica\n got: %s\nwant: %s", out, want)
	}
	again := mustParse(t, string(out))
	if !reflect.DeepEqual(again, def) {
		t.Fatal("la forma canonica no se relee igual")
	}

	refs := Refs(def)
	if !reflect.DeepEqual(refs.Attributes, []string{"plan", "score"}) {
		t.Fatalf("atributos: %v", refs.Attributes)
	}
	if !reflect.DeepEqual(refs.Lists, []string{"0b6f1f7c-1d2e-4c3b-9a8f-7e6d5c4b3a21"}) {
		t.Fatalf("listas: %v", refs.Lists)
	}
}

func TestSinEnumeradosDeclaradosNoCompila(t *testing.T) {
	def := mustParse(t, `{"match":"all","rules":[{"field":"status","op":"eq","value":"active"}]}`)
	if err := Validate(def, Schema{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sin Enums el campo status no debe compilar: %v", err)
	}
}
