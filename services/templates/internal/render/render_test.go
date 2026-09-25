package render

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

func ptr(s string) *string { return &s }

func vars(specs ...domain.Variable) []domain.Variable { return specs }

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func mustCompile(t *testing.T, c domain.Content) *Compiled {
	t.Helper()
	compiled, err := New().Compile(c)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return compiled
}

func mustRender(t *testing.T, c *Compiled, values map[string]json.RawMessage, reserved map[string]string) domain.Rendered {
	t.Helper()
	resolved, err := domain.ResolveValues(c.Variables(), values, reserved)
	if err != nil {
		t.Fatalf("ResolveValues: %v", err)
	}
	out, err := c.Render(resolved)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

func TestEscapadoContextualHrefFrenteATexto(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject: "Hola {{.name}}",
		HTML:    `<p>Hola {{.name}}</p><a href="{{.link}}">ver</a><a href="{{.raw}}">x</a>`,
		Variables: vars(
			domain.Variable{Name: "name", Type: domain.VarString, Required: true},
			domain.Variable{Name: "link", Type: domain.VarURL, Required: true},
			domain.Variable{Name: "raw", Type: domain.VarString},
		),
	})
	out := mustRender(t, c, map[string]json.RawMessage{
		"name": raw(`<b>Ana & Cia</b>`),
		"link": raw("https://example.test/a b?x=1&y=<2>"),
		"raw":  raw("javascript:alert(1)"),
	}, nil)

	if !strings.Contains(out.HTML, "Hola &lt;b&gt;Ana &amp; Cia&lt;/b&gt;") {
		t.Errorf("el texto no se escapo como HTML: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, `href="https://example.test/a%20b?x=1&amp;y=%3c2%3e"`) {
		t.Errorf("la URL no se escapo como URL: %s", out.HTML)
	}
	if !strings.Contains(out.HTML, `href="#ZgotmplZ"`) {
		t.Errorf("javascript: en href no se neutralizo: %s", out.HTML)
	}
	// El asunto es texto plano: no lleva entidades HTML.
	if out.Subject != "Hola <b>Ana & Cia</b>" {
		t.Errorf("asunto inesperado: %q", out.Subject)
	}
}

func TestVariableNoDeclaradaRechazaAlCompilar(t *testing.T) {
	_, err := New().Compile(domain.Content{Subject: "s", HTML: "<p>{{.nombre}}</p>"})
	if !errors.Is(err, domain.ErrInvalidTemplate) || !strings.Contains(err.Error(), `"nombre"`) {
		t.Fatalf("esperaba ErrInvalidTemplate por variable no declarada, obtuve %v", err)
	}
}

func TestVariablesReservadasNoNecesitanDeclaracion(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject: "Hola desde {{.tenant_name}}",
		HTML:    `<a href="{{.unsubscribe_url}}">baja</a> {{.recipient_email}}`,
	})
	out := mustRender(t, c, nil, map[string]string{
		"tenant_name":     "Acme",
		"unsubscribe_url": "https://mail.example.test/u/1",
		"recipient_email": "ana@example.test",
	})
	if out.Subject != "Hola desde Acme" || !strings.Contains(out.HTML, `href="https://mail.example.test/u/1"`) {
		t.Errorf("salida inesperada: %+v", out)
	}
	// Sin reservadas (previsualizacion) renderiza con vacios en vez de fallar.
	out = mustRender(t, c, nil, nil)
	if out.Subject != "Hola desde" {
		t.Errorf("asunto inesperado sin reservadas: %q", out.Subject)
	}
}

func TestRequeridaAusenteFalla(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "s",
		HTML:      "<p>{{.name}}</p>",
		Variables: vars(domain.Variable{Name: "name", Type: domain.VarString, Required: true}),
	})
	_, err := domain.ResolveValues(c.Variables(), nil, nil)
	if !errors.Is(err, domain.ErrInvalidVariables) || !strings.Contains(err.Error(), "requerida") {
		t.Fatalf("esperaba ErrInvalidVariables por requerida ausente, obtuve %v", err)
	}
}

func TestTiposDeVariable(t *testing.T) {
	specs := vars(
		domain.Variable{Name: "u", Type: domain.VarURL},
		domain.Variable{Name: "e", Type: domain.VarEmail},
		domain.Variable{Name: "n", Type: domain.VarNumber},
		domain.Variable{Name: "b", Type: domain.VarBoolean},
		domain.Variable{Name: "i", Type: domain.VarImage},
	)
	cases := []struct {
		name   string
		values map[string]json.RawMessage
		ok     bool
	}{
		{"url absoluta", map[string]json.RawMessage{"u": raw("https://a.test/x")}, true},
		{"url relativa", map[string]json.RawMessage{"u": raw("/x")}, false},
		{"url javascript", map[string]json.RawMessage{"u": raw("javascript:alert(1)")}, false},
		{"url ftp", map[string]json.RawMessage{"u": raw("ftp://a.test")}, false},
		{"email valido", map[string]json.RawMessage{"e": raw("ana@example.test")}, true},
		{"email invalido", map[string]json.RawMessage{"e": raw("ana@")}, false},
		{"email con nombre", map[string]json.RawMessage{"e": raw("Ana <ana@example.test>")}, false},
		{"numero json", map[string]json.RawMessage{"n": raw(12.5)}, true},
		{"numero en cadena", map[string]json.RawMessage{"n": raw("42")}, true},
		{"numero no numerico", map[string]json.RawMessage{"n": raw("abc")}, false},
		{"numero NaN en cadena", map[string]json.RawMessage{"n": raw("NaN")}, false},
		{"numero infinito en cadena", map[string]json.RawMessage{"n": raw("-Inf")}, false},
		{"numero con signo mas", map[string]json.RawMessage{"n": raw("+5")}, false},
		{"numero con espacios", map[string]json.RawMessage{"n": raw(" 12.50 ")}, true},
		{"imagen https", map[string]json.RawMessage{"i": raw("https://a.test/p.webp")}, true},
		{"imagen http", map[string]json.RawMessage{"i": raw("http://a.test/p.png")}, false},
		{"imagen svg", map[string]json.RawMessage{"i": raw("https://a.test/logo.svg?v=2")}, false},
		{"booleano", map[string]json.RawMessage{"b": raw(true)}, true},
		{"booleano en cadena", map[string]json.RawMessage{"b": raw("false")}, true},
		{"booleano invalido", map[string]json.RawMessage{"b": raw("si")}, false},
		{"cadena donde va numero", map[string]json.RawMessage{"n": raw([]int{1})}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ResolveValues(specs, tc.values, nil)
			if tc.ok && err != nil {
				t.Fatalf("esperaba exito, obtuve %v", err)
			}
			if !tc.ok && !errors.Is(err, domain.ErrInvalidVariables) {
				t.Fatalf("esperaba ErrInvalidVariables, obtuve %v", err)
			}
		})
	}
}

func TestNumeroSeImprimeTalCual(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "Total {{.total}}",
		HTML:      "<p>{{.total}}</p>",
		Variables: vars(domain.Variable{Name: "total", Type: domain.VarNumber}),
	})
	out := mustRender(t, c, map[string]json.RawMessage{"total": raw(json.Number("1234567890123.45"))}, nil)
	if out.Subject != "Total 1234567890123.45" {
		t.Errorf("el numero perdio su representacion: %q", out.Subject)
	}
}

func TestDefaultsYOpcionales(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject: "{{.greeting}} {{.name}}",
		HTML:    `<p>{{if .vip}}VIP{{else}}normal{{end}} {{.count}}</p>`,
		Variables: vars(
			domain.Variable{Name: "greeting", Type: domain.VarString, Default: raw("Hola")},
			domain.Variable{Name: "name", Type: domain.VarString},
			domain.Variable{Name: "vip", Type: domain.VarBoolean},
			domain.Variable{Name: "count", Type: domain.VarNumber},
		),
	})
	out := mustRender(t, c, map[string]json.RawMessage{"name": raw("Ana")}, nil)
	if out.Subject != "Hola Ana" {
		t.Errorf("default no aplicado: %q", out.Subject)
	}
	if !strings.Contains(out.HTML, "normal 0") {
		t.Errorf("opcionales sin default deben valer el cero de su tipo: %s", out.HTML)
	}
}

func TestFuncionesPermitidas(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject: `{{upper .name}} {{lower .name}} {{title .name}} {{default "sin fecha" .when}} {{date "2006-01-02" .when}}`,
		HTML:    `<p>{{default "anonimo" .name}}</p>`,
		Variables: vars(
			domain.Variable{Name: "name", Type: domain.VarString},
			domain.Variable{Name: "when", Type: domain.VarString},
		),
	})
	out := mustRender(t, c, map[string]json.RawMessage{
		"name": raw("ana maría"),
		"when": raw("2026-09-12T10:30:00Z"),
	}, nil)
	if out.Subject != "ANA MARÍA ana maría Ana María 2026-09-12T10:30:00Z 2026-09-12" {
		t.Errorf("asunto inesperado: %q", out.Subject)
	}
	out = mustRender(t, c, nil, nil)
	if out.Subject != "sin fecha" || !strings.Contains(out.HTML, "anonimo") {
		t.Errorf("default sobre vacios: subject=%q html=%s", out.Subject, out.HTML)
	}
}

func TestDateConValorNoRFC3339FallaAlRenderizar(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   `{{date "2006" .when}}`,
		HTML:      "<p>x</p>",
		Variables: vars(domain.Variable{Name: "when", Type: domain.VarString}),
	})
	resolved, _ := domain.ResolveValues(c.Variables(), map[string]json.RawMessage{"when": raw("ayer")}, nil)
	if _, err := c.Render(resolved); !errors.Is(err, domain.ErrInvalidVariables) {
		t.Fatalf("esperaba ErrInvalidVariables, obtuve %v", err)
	}
}

func TestFuncionesProhibidas(t *testing.T) {
	for _, src := range []string{
		`{{js .name}}`, `{{html .name}}`, `{{call .name}}`, `{{printf "%s" .name}}`,
		`{{urlquery .name}}`, `{{len .name}}`, `{{index .name 0}}`, `{{if lt .name "a"}}x{{end}}`,
		`{{slice .name 1}}`, `{{print .name}}`,
	} {
		_, err := New().Compile(domain.Content{
			Subject:   "s",
			HTML:      "<p>" + src + "</p>",
			Variables: vars(domain.Variable{Name: "name", Type: domain.VarString}),
		})
		if !errors.Is(err, domain.ErrInvalidTemplate) || !strings.Contains(err.Error(), "función no permitida") {
			t.Errorf("%s: esperaba rechazo por funcion no permitida, obtuve %v", src, err)
		}
	}
}

func TestConstruccionesNoAdmitidas(t *testing.T) {
	cases := map[string]string{
		`{{range .items}}{{.}}{{end}}`:    "no es una lista",
		`{{with .name}}{{.}}{{end}}`:      "with",
		`{{$x := .name}}{{$x}}`:           "variables locales",
		`{{.}}`:                           "{{.}}",
		`{{.name.first}}`:                 "variables planas",
		`{{template "x"}}`:                "plantillas anidadas",
		`{{define "x"}}a{{end}}{{.name}}`: "define, block ni template",
		`{{block "x" .}}a{{end}}`:         "define, block ni template",
	}
	for src, want := range cases {
		_, err := New().Compile(domain.Content{
			Subject:   "s",
			HTML:      "<p>" + src + "</p>",
			Variables: vars(domain.Variable{Name: "name", Type: domain.VarString}, domain.Variable{Name: "items", Type: domain.VarString}),
		})
		if !errors.Is(err, domain.ErrInvalidTemplate) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: esperaba rechazo con %q, obtuve %v", src, want, err)
		}
	}
}

func TestSintaxisInvalidaSeReporta(t *testing.T) {
	_, err := New().Compile(domain.Content{Subject: "s", HTML: "<p>{{.name</p>"})
	if !errors.Is(err, domain.ErrInvalidTemplate) || !strings.Contains(err.Error(), "html:") {
		t.Fatalf("esperaba error de sintaxis con el nombre de la parte, obtuve %v", err)
	}
}

func TestHTMLPeligrosoSeRechaza(t *testing.T) {
	cases := map[string]string{
		`<p>x</p><script>alert(1)</script>`:        "<script>",
		`<SCRIPT src=x>`:                           "<script>",
		`<iframe src="https://a"></iframe>`:        "<iframe>",
		`<object data="x"></object>`:               "<object>",
		`<embed src="x">`:                          "<embed>",
		`<form action="/x"><input></form>`:         "<form>",
		`<img src="x" onerror="alert(1)">`:         "on*=",
		`<div ONCLICK = "x">`:                      "on*=",
		`<a href="javascript:alert(1)">x</a>`:      "javascript:",
		`<a href = ' JavaScript : alert(1)'>x</a>`: "javascript:",
	}
	for src, want := range cases {
		_, err := New().Compile(domain.Content{Subject: "s", HTML: src})
		if !errors.Is(err, domain.ErrInvalidTemplate) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: esperaba rechazo con %q, obtuve %v", src, want, err)
		}
	}
	// Palabras que contienen "on" no son atributos de evento.
	if _, err := New().Compile(domain.Content{Subject: "s", HTML: `<p>confirmation button=1 animation=2</p>`}); err != nil {
		t.Errorf("falso positivo en atributos de evento: %v", err)
	}
}

func TestLimitesDeTamano(t *testing.T) {
	if _, err := New().Compile(domain.Content{Subject: strings.Repeat("a", domain.MaxSubjectBytes+1), HTML: "<p>x</p>"}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Errorf("asunto largo: %v", err)
	}
	if _, err := New().Compile(domain.Content{Subject: "a\nb", HTML: "<p>x</p>"}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Errorf("asunto con salto de linea: %v", err)
	}
	if _, err := New().Compile(domain.Content{Subject: "s", HTML: strings.Repeat("a", domain.MaxHTMLBytes+1)}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Errorf("html grande: %v", err)
	}
	if _, err := New().Compile(domain.Content{Subject: "", HTML: "<p>x</p>"}); !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Errorf("asunto vacio: %v", err)
	}
}

func TestSalidaDemasiadoGrande(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "s",
		HTML:      "<p>{{.a}}{{.a}}{{.a}}</p>",
		Variables: vars(domain.Variable{Name: "a", Type: domain.VarString}),
	})
	big := strings.Repeat("x", domain.MaxOutputBytes/2)
	resolved, _ := domain.ResolveValues(c.Variables(), map[string]json.RawMessage{"a": raw(big)}, nil)
	if _, err := c.Render(resolved); !errors.Is(err, domain.ErrOutputTooLarge) {
		t.Fatalf("esperaba ErrOutputTooLarge, obtuve %v", err)
	}
}

func TestAsuntoNoAdmiteInyeccionDeCabeceras(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "Pedido {{.ref}}",
		HTML:      "<p>x</p>",
		Variables: vars(domain.Variable{Name: "ref", Type: domain.VarString}),
	})
	out := mustRender(t, c, map[string]json.RawMessage{"ref": raw("1\r\nBcc: x@y.test")}, nil)
	if strings.ContainsAny(out.Subject, "\r\n") {
		t.Errorf("el asunto conserva saltos de linea: %q", out.Subject)
	}
}

func TestMissingKeyError(t *testing.T) {
	// Si el mapa de valores no trae una clave referenciada, la ejecucion falla en vez de
	// imprimir "<no value>". Se simula pasando un mapa incompleto directamente.
	c := mustCompile(t, domain.Content{
		Subject:   "{{.name}}",
		HTML:      "<p>{{.name}}</p>",
		Variables: vars(domain.Variable{Name: "name", Type: domain.VarString}),
	})
	if _, err := c.Render(map[string]any{}); err == nil || !errors.Is(err, domain.ErrInvalidVariables) {
		t.Fatalf("esperaba fallo por clave ausente, obtuve %v", err)
	}
}

func TestTextoPropioSeUsaSiExiste(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "s",
		HTML:      "<p>Hola {{.name}}</p>",
		Text:      ptr("Texto propio para {{.name}} <sin escapar>"),
		Variables: vars(domain.Variable{Name: "name", Type: domain.VarString}),
	})
	out := mustRender(t, c, map[string]json.RawMessage{"name": raw("Ana & Cia")}, nil)
	if out.Text != "Texto propio para Ana & Cia <sin escapar>" {
		t.Errorf("texto inesperado: %q", out.Text)
	}
}

func TestGeneracionDeTextoPlano(t *testing.T) {
	src := `<!DOCTYPE html><html><head><title>t</title><style>p{color:red}</style></head>
<body><!-- comentario --><style>.x{}</style>
<h1>Hola&nbsp;Ana</h1>
<p>Tu pedido   <b>#42</b> est&aacute; listo.<br>Gracias &amp; saludos.</p>
<ul><li>Uno</li><li>Dos</li></ul>
<table><tr><td>A</td><td>B</td></tr></table>
<p><a href="https://example.test/?a=1&amp;b=2">Ver pedido</a> o <a href='https://example.test/'>https://example.test/</a></p>
<p><a href="{{.x}}"></a></p>
</body></html>`
	got := HTMLToText(src)
	want := "Hola Ana\n\nTu pedido #42 está listo.\nGracias & saludos.\n\n- Uno\n- Dos\n\nA B\n\nVer pedido (https://example.test/?a=1&b=2) o https://example.test/\n\n{{.x}}"
	if got != want {
		t.Errorf("texto plano inesperado:\n--- obtenido ---\n%s\n--- esperado ---\n%s", got, want)
	}
}

func TestTextoGeneradoDesdeHTMLRenderizado(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "s",
		HTML:      `<p>Hola {{.name}}</p><p><a href="{{.link}}">Entrar</a></p>`,
		Variables: vars(domain.Variable{Name: "name", Type: domain.VarString}, domain.Variable{Name: "link", Type: domain.VarURL}),
	})
	out := mustRender(t, c, map[string]json.RawMessage{"name": raw("Ana & Cia"), "link": raw("https://a.test/?x=1&y=2")}, nil)
	if out.Text != "Hola Ana & Cia\n\nEntrar (https://a.test/?x=1&y=2)" {
		t.Errorf("texto generado inesperado: %q", out.Text)
	}
}

func TestDeclaracionesInvalidas(t *testing.T) {
	cases := map[string][]domain.Variable{
		"nombre con mayusculas": {{Name: "Nombre", Type: domain.VarString}},
		"nombre con guion":      {{Name: "first-name", Type: domain.VarString}},
		"tipo desconocido":      {{Name: "a", Type: "date"}},
		"duplicada":             {{Name: "a", Type: domain.VarString}, {Name: "a", Type: domain.VarString}},
		"reservada":             {{Name: "tenant_name", Type: domain.VarString}},
		"default incoherente":   {{Name: "a", Type: domain.VarNumber, Default: raw("abc")}},
		"requerida con default": {{Name: "a", Type: domain.VarString, Required: true, Default: raw("x")}},
	}
	for name, specs := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New().Compile(domain.Content{Subject: "s", HTML: "<p>x</p>", Variables: specs})
			if !errors.Is(err, domain.ErrInvalidVariableDeclaration) {
				t.Fatalf("esperaba ErrInvalidVariableDeclaration, obtuve %v", err)
			}
		})
	}
}

func TestReferenciasOrdenadas(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "{{.b}}",
		HTML:      "<p>{{.a}} {{.tenant_name}}</p>",
		Variables: vars(domain.Variable{Name: "a", Type: domain.VarString}, domain.Variable{Name: "b", Type: domain.VarString}),
	})
	if got := strings.Join(c.References(), ","); got != "a,b,tenant_name" {
		t.Errorf("referencias inesperadas: %s", got)
	}
}

func TestCompiladoSePuedeRenderizarEnParalelo(t *testing.T) {
	c := mustCompile(t, domain.Content{
		Subject:   "{{.name}}",
		HTML:      `<a href="{{.name}}">{{.name}}</a>`,
		Variables: vars(domain.Variable{Name: "name", Type: domain.VarString}),
	})
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() {
			_, err := c.Render(map[string]any{"name": "x", "unsubscribe_url": "", "view_in_browser_url": "", "recipient_email": "", "tenant_name": ""})
			done <- err
		}()
	}
	for i := 0; i < 20; i++ {
		if err := <-done; err != nil {
			t.Fatalf("render paralelo: %v", err)
		}
	}
}
