package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	if got, err := NormalizeEmail("  Ana.Perez@Example.COM "); err != nil || got != "ana.perez@example.com" {
		t.Fatalf("normalizar: %q %v", got, err)
	}
	for _, bad := range []string{"", "sin-arroba", "a@b", "con espacio@example.com", "@example.com", "a@@b.com", strings.Repeat("a", 320) + "@x.com"} {
		if _, err := NormalizeEmail(bad); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("%q: se esperaba ErrInvalidEmail, hubo %v", bad, err)
		}
	}
	if EmailSHA256("Ana@Example.com ") != EmailSHA256("ana@example.com") {
		t.Fatal("la huella debe ser la de la direccion normalizada")
	}
}

func TestNormalizeLocale(t *testing.T) {
	ok := map[string]string{"es": "es", "ES_pe": "es-PE", "pt-br": "pt-BR", "es-419": "es-419", "fil": "fil"}
	for in, want := range ok {
		got, err := NormalizeLocale(in)
		if err != nil || got == nil || *got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if got, err := NormalizeLocale("  "); err != nil || got != nil {
		t.Fatalf("vacio debe ser nil: %v %v", got, err)
	}
	for _, bad := range []string{"e", "english", "es-PE-x", "es-P", "es-1234", "12", "es PE"} {
		if _, err := NormalizeLocale(bad); !errors.Is(err, ErrInvalidLocale) {
			t.Errorf("%q: se esperaba ErrInvalidLocale, hubo %v", bad, err)
		}
	}
}

func TestNormalizeTimezone(t *testing.T) {
	for _, tz := range []string{"America/Lima", "UTC", "Europe/Madrid", "America/Argentina/Buenos_Aires", "Etc/GMT+5"} {
		got, err := NormalizeTimezone(tz)
		if err != nil || got == nil || *got != tz {
			t.Errorf("%q: %v %v", tz, got, err)
		}
	}
	if got, err := NormalizeTimezone(""); err != nil || got != nil {
		t.Fatalf("vacio debe ser nil: %v %v", got, err)
	}
	for _, bad := range []string{"Local", "Mars/Olympus", "../etc/passwd", "/etc/localtime", "America/../Lima", "america lima", strings.Repeat("A", 65)} {
		if _, err := NormalizeTimezone(bad); !errors.Is(err, ErrInvalidTimezone) {
			t.Errorf("%q: se esperaba ErrInvalidTimezone, hubo %v", bad, err)
		}
	}
}

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{" VIP ", "vip", "Cliente Nuevo", "año-2026", "a:b.c_d"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"vip", "cliente nuevo", "año-2026", "a:b.c_d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("etiquetas: %v", got)
	}
	for _, bad := range [][]string{{""}, {"  "}, {"con,coma"}, {"<script>"}, {strings.Repeat("x", 65)}, {"-empieza"}} {
		if _, err := NormalizeTags(bad); !errors.Is(err, ErrInvalidTag) {
			t.Errorf("%q: se esperaba ErrInvalidTag, hubo %v", bad, err)
		}
	}
	many := make([]string, MaxTags+1)
	for i := range many {
		many[i] = "t" + strings.Repeat("x", i%60) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := NormalizeTags(many); !errors.Is(err, ErrTooManyTags) {
		t.Fatalf("se esperaba ErrTooManyTags, hubo %v", err)
	}
	merged, err := MergeTags([]string{"vip"}, []string{"VIP", "nuevo"})
	if err != nil || !reflect.DeepEqual(merged, []string{"vip", "nuevo"}) {
		t.Fatalf("MergeTags: %v %v", merged, err)
	}
}

func TestValidateAttributeKey(t *testing.T) {
	for _, ok := range []string{"plan", "score_2", "a"} {
		if err := ValidateAttributeKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Plan", "1plan", "_plan", "plan-x", "plan.x", strings.Repeat("a", 64), "a'b"} {
		if err := ValidateAttributeKey(bad); !errors.Is(err, ErrInvalidAttributeKey) {
			t.Errorf("%q: se esperaba ErrInvalidAttributeKey, hubo %v", bad, err)
		}
	}
	for _, reserved := range []string{"email", "tags", "status", "consent", "list", "created_at", "attributes", "tenant_id"} {
		if err := ValidateAttributeKey(reserved); !errors.Is(err, ErrReservedAttributeKey) {
			t.Errorf("%q: se esperaba ErrReservedAttributeKey, hubo %v", reserved, err)
		}
	}
}

func TestParseAttributeValuePorTipo(t *testing.T) {
	cases := []struct {
		typ  AttrType
		raw  string
		want any
		ok   bool
	}{
		{AttrString, `"pro"`, "pro", true},
		{AttrString, `7`, nil, false},
		{AttrString, `"` + strings.Repeat("a", MaxAttributeString+1) + `"`, nil, false},
		{AttrNumber, `12.50`, json.Number("12.50"), true},
		{AttrNumber, `-3`, json.Number("-3"), true},
		{AttrNumber, `"7"`, nil, false},
		{AttrNumber, `1e400`, nil, false},
		{AttrNumber, `true`, nil, false},
		{AttrBoolean, `true`, true, true},
		{AttrBoolean, `"true"`, nil, false},
		{AttrBoolean, `1`, nil, false},
		{AttrDate, `"2026-09-12"`, "2026-09-12", true},
		{AttrDate, `"2026-02-30"`, nil, false},
		{AttrDate, `"12/09/2026"`, nil, false},
		{AttrDate, `20260912`, nil, false},
		{AttrString, `null`, nil, true},
		{AttrString, `{"a":1}`, nil, false},
		{AttrString, `"a" "b"`, nil, false},
	}
	for _, tc := range cases {
		got, err := ParseAttributeValue(tc.typ, json.RawMessage(tc.raw))
		if tc.ok != (err == nil) {
			t.Errorf("%s %s: err=%v", tc.typ, tc.raw, err)
			continue
		}
		if tc.ok && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %s: %#v, se esperaba %#v", tc.typ, tc.raw, got, tc.want)
		}
	}
}

func TestMergeAttributes(t *testing.T) {
	defs := IndexDefinitions([]AttributeDefinition{
		{Key: "plan", Type: AttrString, Required: true},
		{Key: "score", Type: AttrNumber},
		{Key: "vip", Type: AttrBoolean},
	})
	current := map[string]any{"plan": "free", "score": json.Number("3")}

	out, changed, err := MergeAttributes(current, RawAttributes{
		"plan": json.RawMessage(`"pro"`), "score": json.RawMessage(`null`), "vip": json.RawMessage(`false`),
	}, defs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, map[string]any{"plan": "pro", "vip": false}) {
		t.Fatalf("mezcla: %#v", out)
	}
	if len(changed) != 3 {
		t.Fatalf("cambios: %v", changed)
	}
	if current["plan"] != "free" {
		t.Fatal("MergeAttributes no debe modificar el mapa de entrada")
	}

	if _, changed, _ := MergeAttributes(out, RawAttributes{"plan": json.RawMessage(`"pro"`)}, defs); len(changed) != 0 {
		t.Fatalf("el mismo valor no es un cambio: %v", changed)
	}
	if _, _, err := MergeAttributes(nil, RawAttributes{"otro": json.RawMessage(`1`)}, defs); !errors.Is(err, ErrUndeclaredAttribute) {
		t.Fatalf("clave no declarada: %v", err)
	}
	if _, _, err := MergeAttributes(nil, RawAttributes{"score": json.RawMessage(`"x"`)}, defs); !errors.Is(err, ErrAttributeValue) {
		t.Fatalf("tipo incorrecto: %v", err)
	}
	if _, _, err := MergeAttributes(current, RawAttributes{"plan": json.RawMessage(`null`)}, defs); !errors.Is(err, ErrRequiredAttribute) {
		t.Fatalf("quitar un obligatorio: %v", err)
	}
	if err := CheckRequired(map[string]any{"score": json.Number("1")}, defs); !errors.Is(err, ErrRequiredAttribute) {
		t.Fatalf("CheckRequired: %v", err)
	}
	if err := CheckRequired(map[string]any{"plan": "x"}, defs); err != nil {
		t.Fatalf("CheckRequired: %v", err)
	}
}
