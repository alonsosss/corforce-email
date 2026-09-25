package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

func itemsVar() domain.Variable {
	return domain.Variable{Name: "items", Type: domain.VarList, Required: true, Fields: []domain.Field{
		{Name: "name", Type: domain.VarString, Required: true},
		{Name: "quantity", Type: domain.VarNumber},
		{Name: "price", Type: domain.VarNumber, Required: true},
		{Name: "image_url", Type: domain.VarImage},
		{Name: "link", Type: domain.VarURL},
	}}
}

func orderContent(html string) domain.Content {
	return domain.Content{
		Subject: "Pedido {{.order}}: {{count .items}} productos",
		HTML:    html,
		Variables: vars(
			itemsVar(),
			domain.Variable{Name: "order", Type: domain.VarString, Required: true},
			domain.Variable{Name: "currency", Type: domain.VarString, Required: true},
		),
	}
}

func item(name string, price any) map[string]any {
	return map[string]any{"name": name, "price": price, "quantity": 1}
}

func TestListaSeRecorreConCamposYVariablesDeLaPlantilla(t *testing.T) {
	c := mustCompile(t, orderContent(
		`<table>{{range .items}}<tr><td><img src="{{.image_url}}" alt="{{.name}}"></td>`+
			`<td>{{.name}} x{{.quantity}}</td><td>{{$.currency}} {{money .price}}</td></tr>`+
			`{{else}}<tr><td>Sin productos en {{.order}}</td></tr>{{end}}</table>`))
	out := mustRender(t, c, map[string]json.RawMessage{
		"order":    raw("A-1"),
		"currency": raw("S/"),
		"items": raw([]map[string]any{
			{"name": "Lavadora", "price": "1899", "quantity": 1, "image_url": "https://cdn.test/l.jpg"},
			{"name": "Hervidor", "price": 128.7, "quantity": 2, "sku": "no declarado"},
		}),
	}, nil)
	for _, want := range []string{
		`<img src="https://cdn.test/l.jpg" alt="Lavadora">`,
		`Lavadora x1</td><td>S/ 1,899.00`,
		`Hervidor x2</td><td>S/ 128.70`,
		`<img src="" alt="Hervidor">`,
	} {
		if !strings.Contains(out.HTML, want) {
			t.Errorf("falta %q en %s", want, out.HTML)
		}
	}
	if out.Subject != "Pedido A-1: 2 productos" {
		t.Errorf("asunto: %q", out.Subject)
	}

	empty := mustRender(t, c, map[string]json.RawMessage{
		"order": raw("A-2"), "currency": raw("S/"), "items": raw([]any{}),
	}, nil)
	if !strings.Contains(empty.HTML, "Sin productos en A-2") {
		t.Errorf("else del range con la lista vacia: %s", empty.HTML)
	}
}

func TestLosValoresDeLaListaSeEscapanSegunElContexto(t *testing.T) {
	c := mustCompile(t, orderContent(
		`{{range .items}}<a href="{{.link}}">{{.name}}</a>{{end}}`))
	out := mustRender(t, c, map[string]json.RawMessage{
		"order": raw("A"), "currency": raw("S/"),
		"items": raw([]map[string]any{{"name": `<script>alert(1)</script>`, "price": 1, "link": "https://a.test/x?q=<b>"}}),
	}, nil)
	if strings.Contains(out.HTML, "<script>") {
		t.Fatalf("un campo de la lista salio sin escapar: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, "&lt;script&gt;") || !strings.Contains(out.HTML, `href="https://a.test/x?q=%3cb%3e"`) {
		t.Errorf("escapado inesperado: %s", out.HTML)
	}
}

func TestTakeYRestAcotanLoQueSeMuestra(t *testing.T) {
	c := mustCompile(t, orderContent(
		`{{range take 2 .items}}[{{.name}}]{{end}}{{if rest 2 .items}} y {{rest 2 .items}} más{{end}}`))
	list := make([]map[string]any, 5)
	for i := range list {
		list[i] = item(fmt.Sprintf("p%d", i), 1)
	}
	out := mustRender(t, c, map[string]json.RawMessage{"order": raw("A"), "currency": raw("S/"), "items": raw(list)}, nil)
	if out.HTML != "[p0][p1] y 3 más" {
		t.Errorf("html: %q", out.HTML)
	}
	if c.ListCap("items") != 2 || len(c.UnboundedLists()) != 0 {
		t.Errorf("tope %d, sin tope %v", c.ListCap("items"), c.UnboundedLists())
	}
	out = mustRender(t, c, map[string]json.RawMessage{"order": raw("A"), "currency": raw("S/"), "items": raw(list[:1])}, nil)
	if out.HTML != "[p0]" {
		t.Errorf("con menos elementos que el tope: %q", out.HTML)
	}
}

func TestListaSinTopeSeSenala(t *testing.T) {
	c := mustCompile(t, orderContent(`{{range .items}}{{.name}}{{end}}{{range take 3 .items}}{{.name}}{{end}}`))
	if got := c.UnboundedLists(); len(got) != 1 || got[0] != "items" {
		t.Errorf("sin tope: %v", got)
	}
	if c.ListCap("items") != domain.MaxListItems {
		t.Errorf("el peor caso de una lista sin tope es MaxListItems: %d", c.ListCap("items"))
	}
}

func TestMoney(t *testing.T) {
	cases := map[string]string{
		"0": "0.00", "5": "5.00", "1234.5": "1,234.50", "2027.7": "2,027.70", "1000000": "1,000,000.00",
		"0.005": "0.01", "-0.005": "-0.01", "-1234.567": "-1,234.57", "999.995": "1,000.00", "1e3": "1,000.00",
		"123456789012345678.905": "123,456,789,012,345,678.91",
	}
	for in, want := range cases {
		got, err := formatMoney(json.Number(in))
		if err != nil || got != want {
			t.Errorf("money(%s) = %q, %v; quiero %q", in, got, err, want)
		}
	}
	if got, err := formatMoney(""); got != "" || err != nil {
		t.Errorf("money vacio: %q %v", got, err)
	}
}

func TestNonzeroDistingueElCeroDeUnImporte(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "s",
		HTML:      `{{if nonzero .discount}}-{{money .discount}}{{else}}sin descuento{{end}}`,
		Variables: vars(domain.Variable{Name: "discount", Type: domain.VarNumber}),
	})
	for in, want := range map[string]string{"": "sin descuento", "0": "sin descuento", "0.00": "sin descuento", "12.5": "-12.50"} {
		values := map[string]json.RawMessage{}
		if in != "" {
			values["discount"] = raw(in)
		}
		if out := mustRender(t, c, values, nil); out.HTML != want {
			t.Errorf("descuento %q: %q, quiero %q", in, out.HTML, want)
		}
	}
}

func TestComparacionesConTexto(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject: "s",
		HTML:    `{{if eq .status "shipped"}}enviado{{else if or (eq .status "delivered") (not .vip)}}otro{{end}}{{if ne "x" .status}}!{{end}}`,
		Variables: vars(
			domain.Variable{Name: "status", Type: domain.VarString},
			domain.Variable{Name: "vip", Type: domain.VarBoolean},
		),
	})
	out := mustRender(t, c, map[string]json.RawMessage{"status": raw("shipped")}, nil)
	if out.HTML != "enviado!" {
		t.Errorf("html: %q", out.HTML)
	}
}

func TestRangeYListasRechazadas(t *testing.T) {
	scalars := []domain.Variable{
		{Name: "name", Type: domain.VarString},
		{Name: "amount", Type: domain.VarString},
		{Name: "n", Type: domain.VarNumber},
	}
	cases := map[string]string{
		`{{.items}}`:             "solo se recorre con range",
		`{{upper .items}}`:       "solo se recorre con range",
		`{{range .name}}{{end}}`: "no es una lista",
		`{{range .items}}{{range .items}}{{end}}{{end}}`: "range dentro de otro",
		`{{range .items}}{{.sku}}{{end}}`:                `no declara el campo "sku"`,
		`{{range .items}}{{.name.first}}{{end}}`:         "variables planas",
		`{{range $i, $e := .items}}{{end}}`:              "variables locales",
		`{{range .items}}{{$x := .name}}{{end}}`:         "variables locales",
		`{{range take 0 .items}}{{end}}`:                 "entero entre 1",
		`{{range take 101 .items}}{{end}}`:               "entero entre 1",
		`{{range take .n .items}}{{end}}`:                "entero entre 1",
		`{{range .items | take 2}}{{end}}`:               "range recorre una lista",
		`{{take 2 .items}}`:                              "take solo se usa",
		`{{.items | count}}`:                             "solo se recorre con range",
		`{{count .name}}`:                                "no es una lista",
		`{{money .amount}}`:                              "money y nonzero solo reciben",
		`{{if nonzero .name}}x{{end}}`:                   "money y nonzero solo reciben",
		`{{range .items}}{{money .name}}{{end}}`:         "money y nonzero solo reciben",
		`{{if eq .n 1}}x{{end}}`:                         "compara una variable con un texto",
		`{{if eq "a" "b"}}x{{end}}`:                      "compara una variable con un texto",
		`{{if eq .name .name}}x{{end}}`:                  "compara una variable con un texto",
		`{{with .items}}{{end}}`:                         "with",
		`{{range .items}}{{.}}{{end}}`:                   "{{.}}",
	}
	for src, want := range cases {
		c := orderContent("<p>" + src + "</p>")
		c.Variables = append(c.Variables, scalars...)
		_, err := New().Compile(c)
		if !errors.Is(err, domain.ErrInvalidTemplate) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: esperaba rechazo con %q, obtuve %v", src, want, err)
		}
	}
}

func TestBorradorInfiereLaLista(t *testing.T) {
	c, err := New().CompileDraft(domain.Content{
		Subject: "s",
		HTML:    `{{range take 5 .items}}{{.name}} {{money .price}}{{end}} {{money .total}} {{.first_name}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]domain.Variable{}
	for _, v := range c.Variables() {
		got[v.Name] = v
	}
	items := got["items"]
	if items.Type != domain.VarList || len(items.Fields) != 2 ||
		items.Fields[0] != (domain.Field{Name: "name", Type: domain.VarString}) ||
		items.Fields[1] != (domain.Field{Name: "price", Type: domain.VarNumber}) {
		t.Errorf("lista inferida: %+v", items)
	}
	if got["total"].Type != domain.VarNumber || got["first_name"].Type != domain.VarString {
		t.Errorf("escalares inferidos: %+v", got)
	}

	if _, err := New().CompileDraft(domain.Content{Subject: "s", HTML: `{{count .items}}`}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Errorf("una lista sin campos no se puede inferir: %v", err)
	}
	if _, err := New().CompileDraft(domain.Content{Subject: "{{.x}}", HTML: `{{range .x}}{{.a}}{{end}}`}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Errorf("un nombre usado como lista y como valor: %v", err)
	}
}

func TestValoresDeLista(t *testing.T) {
	declared := vars(itemsVar())
	many := make([]map[string]any, domain.MaxListItems+1)
	for i := range many {
		many[i] = item("p", 1)
	}
	cases := []struct {
		name  string
		value any
		ok    bool
	}{
		{"valida", []map[string]any{item("a", "10.50")}, true},
		{"vacia", []any{}, true},
		{"campo opcional en blanco", []map[string]any{{"name": "a", "price": 1, "image_url": "", "quantity": ""}}, true},
		{"no es lista", map[string]any{"name": "a"}, false},
		{"elemento no objeto", []any{"a"}, false},
		{"elemento nulo", []any{nil}, false},
		{"falta requerido", []map[string]any{{"price": 1}}, false},
		{"requerido en blanco no es ausente", []map[string]any{{"name": "a", "price": ""}}, false},
		{"numero NaN", []map[string]any{{"name": "a", "price": "NaN"}}, false},
		{"numero infinito", []map[string]any{{"name": "a", "price": "Inf"}}, false},
		{"numero hexadecimal", []map[string]any{{"name": "a", "price": "0x10"}}, false},
		{"imagen http", []map[string]any{{"name": "a", "price": 1, "image_url": "http://a.test/x.png"}}, false},
		{"imagen svg", []map[string]any{{"name": "a", "price": 1, "image_url": "https://a.test/x.SVG"}}, false},
		{"imagen data", []map[string]any{{"name": "a", "price": 1, "image_url": "data:image/png;base64,AAAA"}}, false},
		{"enlace javascript", []map[string]any{{"name": "a", "price": 1, "link": "javascript:alert(1)"}}, false},
		{"demasiados elementos", many, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ResolveValues(declared, map[string]json.RawMessage{"items": raw(tc.value)}, nil)
			if tc.ok && err != nil {
				t.Fatalf("esperaba exito, obtuve %v", err)
			}
			if !tc.ok && !errors.Is(err, domain.ErrInvalidVariables) {
				t.Fatalf("esperaba ErrInvalidVariables, obtuve %v", err)
			}
		})
	}
}

func TestDeclaracionDeListas(t *testing.T) {
	field := domain.Field{Name: "a", Type: domain.VarString}
	tooMany := make([]domain.Field, domain.MaxListFields+1)
	for i := range tooMany {
		tooMany[i] = domain.Field{Name: fmt.Sprintf("f%d", i), Type: domain.VarString}
	}
	cases := map[string]domain.Variable{
		"sin campos":         {Name: "l", Type: domain.VarList},
		"demasiados campos":  {Name: "l", Type: domain.VarList, Fields: tooMany},
		"campo lista":        {Name: "l", Type: domain.VarList, Fields: []domain.Field{{Name: "x", Type: domain.VarList}}},
		"campo mal nombrado": {Name: "l", Type: domain.VarList, Fields: []domain.Field{{Name: "X", Type: domain.VarString}}},
		"campo duplicado":    {Name: "l", Type: domain.VarList, Fields: []domain.Field{field, field}},
		"lista con default":  {Name: "l", Type: domain.VarList, Fields: []domain.Field{field}, Default: raw([]any{})},
		"escalar con campos": {Name: "s", Type: domain.VarString, Fields: []domain.Field{field}},
	}
	for name, v := range cases {
		if err := domain.ValidateDeclarations([]domain.Variable{v}); !errors.Is(err, domain.ErrInvalidVariableDeclaration) {
			t.Errorf("%s: esperaba rechazo, obtuve %v", name, err)
		}
	}
}

func TestListaDentroDeComentarioCondicionalDeOutlook(t *testing.T) {
	c := mustCompile(t, orderContent(
		`{{range .items}}<!--[if mso]><table><tr><td><![endif]--><p>{{.name}}</p><!--[if mso]></td></tr></table><![endif]-->{{end}}`))
	out := mustRender(t, c, map[string]json.RawMessage{
		"order": raw("A"), "currency": raw("S/"), "items": raw([]map[string]any{item("x", 1), item("y", 2)}),
	}, nil)
	if strings.Count(out.HTML, "<!--[if mso]><table><tr><td><![endif]-->") != 2 || strings.Contains(out.HTML, conditionalPrefix) {
		t.Errorf("los comentarios repetidos por el range deben reponerse todos: %s", out.HTML)
	}
}
