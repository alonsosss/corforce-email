package markup

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

func values() map[string]any {
	return map[string]any{
		VarOrderNumber: "A-1", VarCurrencyCode: "PEN", VarTotal: json.Number("10"),
		VarItems: []map[string]any{{FieldName: "Te", FieldQuantity: json.Number("1.50"), FieldUnitPrice: json.Number("4"), FieldImageURL: ""}},
	}
}

func TestBuildOmiteLoQueNoLlegaYExigeLoMinimo(t *testing.T) {
	got, ok := Build(domain.MarkupOrder, values(), "Tienda")
	if !ok || strings.Contains(got, `"url"`) || strings.Contains(got, `"orderStatus"`) || strings.Contains(got, `"image"`) ||
		!strings.Contains(got, `"value":"1.5"`) || !strings.Contains(got, `"price":"4.00"`) {
		t.Fatalf("marcado: %s %v", got, ok)
	}
	for name, mutate := range map[string]func(map[string]any){
		"sin comercio":      nil,
		"sin numero":        func(v map[string]any) { v[VarOrderNumber] = "" },
		"moneda en simbolo": func(v map[string]any) { v[VarCurrencyCode] = "S/" },
		"sin productos":     func(v map[string]any) { v[VarItems] = []map[string]any{} },
		"producto sin nombre": func(v map[string]any) {
			v[VarItems] = []map[string]any{{FieldName: "", FieldQuantity: json.Number("1"), FieldUnitPrice: json.Number("1")}}
		},
	} {
		v, merchant := values(), "Tienda"
		if mutate == nil {
			merchant = " "
		} else {
			mutate(v)
		}
		if _, ok := Build(domain.MarkupOrder, v, merchant); ok {
			t.Errorf("%s: no debe haber marcado", name)
		}
	}
	if _, ok := Build("", values(), "Tienda"); ok {
		t.Error("sin marcado declarado no hay marcado")
	}
}

func TestInject(t *testing.T) {
	if got := Inject("<html><HEAD></HEAD><body>x</body></html>", "{}"); got != `<html><HEAD><script type="application/ld+json">{}</script></HEAD><body>x</body></html>` {
		t.Errorf("con head: %s", got)
	}
	if got := Inject("<p>x</p>", "{}"); got != `<script type="application/ld+json">{}</script><p>x</p>` {
		t.Errorf("sin head: %s", got)
	}
}

func TestValidateExigeLasVariablesRequeridas(t *testing.T) {
	vars := []domain.Variable{
		{Name: VarOrderNumber, Type: domain.VarString, Required: true},
		{Name: VarCurrencyCode, Type: domain.VarString, Required: true},
		{Name: VarTotal, Type: domain.VarNumber, Required: true},
		{Name: VarItems, Type: domain.VarList, Required: true, Fields: []domain.Field{
			{Name: FieldName, Type: domain.VarString, Required: true},
			{Name: FieldQuantity, Type: domain.VarNumber, Required: true},
			{Name: FieldUnitPrice, Type: domain.VarNumber, Required: true},
		}},
	}
	if err := Validate(domain.MarkupOrder, vars); err != nil {
		t.Fatal(err)
	}
	optionalPrice := append([]domain.Variable(nil), vars...)
	optionalPrice[3].Fields = []domain.Field{vars[3].Fields[0], vars[3].Fields[1], {Name: FieldUnitPrice, Type: domain.VarNumber}}
	optionalCode := append([]domain.Variable(nil), vars...)
	optionalCode[1] = domain.Variable{Name: VarCurrencyCode, Type: domain.VarString}
	wrongURL := append(append([]domain.Variable(nil), vars...), domain.Variable{Name: VarOrderURL, Type: domain.VarString})
	for name, v := range map[string][]domain.Variable{"precio opcional": optionalPrice, "moneda opcional": optionalCode, "url de texto": wrongURL} {
		if err := Validate(domain.MarkupOrder, v); !errors.Is(err, domain.ErrInvalidMarkup) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
