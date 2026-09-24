package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func rule(name string, conds []FilterCondition, actions ...FilterAction) FilterRule {
	return FilterRule{Name: name, Enabled: true, Conditions: conds, Actions: actions}
}

func cond(field, op, value string) []FilterCondition {
	return []FilterCondition{{Field: field, Op: op, Value: value}}
}

func normalized(t *testing.T, f *MailboxFilters) *MailboxFilters {
	t.Helper()
	if f.Username == "" {
		f.Username = "ana@acme.test"
	}
	if err := f.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return f
}

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var fe *FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("se esperaba un FieldError y salio %v", err)
	}
	return fe.Field
}

func TestReglasVaciasNoGeneranScript(t *testing.T) {
	f := normalized(t, &MailboxFilters{})
	if f.ScriptData != "" || f.Rules == nil || f.Forwarding.Addresses == nil {
		t.Fatalf("sin reglas ni reenvio no hay script y las listas salen vacias, no nulas: %+v", f)
	}
	apagada := rule("apagada", cond(FilterFieldFrom, FilterOpContains, "x"), FilterAction{Type: FilterActionDiscard})
	apagada.Enabled = false
	f = normalized(t, &MailboxFilters{Rules: []FilterRule{apagada}, Forwarding: Forwarding{Addresses: []string{"otro@acme.test"}}})
	if f.ScriptData != "" {
		t.Fatalf("una regla desactivada y un reenvio apagado no generan script:\n%s", f.ScriptData)
	}
	if f.Rules[0].ID == uuid.Nil {
		t.Fatal("una regla nueva recibe identificador")
	}
}

func TestGeneraElScriptDentroDeNoEsSpam(t *testing.T) {
	f := normalized(t, &MailboxFilters{
		Rules: []FilterRule{
			rule("Clientes", []FilterCondition{
				{Field: FilterFieldFrom, Op: FilterOpContains, Value: "cliente.example"},
				{Field: FilterFieldSubject, Op: FilterOpNotContains, Value: "boletin"},
			}, FilterAction{Type: FilterActionMove, Folder: "Clientes/2026"}, FilterAction{Type: FilterActionMarkRead}),
		},
		Forwarding: Forwarding{Enabled: true, Addresses: []string{"Copia@Externo.Example"}, KeepCopy: true},
	})
	want := "# Reglas y reenvio generados por la plataforma; se editan desde el buzon, no a mano.\n" +
		"require [\"fileinto\", \"mailbox\", \"imap4flags\", \"copy\"];\n" +
		"if not header :contains \"X-Spam-Flag\" \"YES\" {\n" +
		"  redirect :copy \"copia@externo.example\";\n" +
		"  if allof (header :contains \"from\" \"cliente.example\", not header :contains \"subject\" \"boletin\") {\n" +
		"    addflag \"\\\\Seen\";\n" +
		"    fileinto :create \"Clientes/2026\";\n" +
		"  }\n" +
		"}\n"
	if f.ScriptData != want {
		t.Fatalf("script:\n%s\nesperado:\n%s", f.ScriptData, want)
	}
}

func TestCadaAccionYCondicionSeTraduce(t *testing.T) {
	casos := []struct {
		nombre string
		rule   FilterRule
		quiere []string
		evita  []string
	}{
		{"is sobre direccion", rule("r", cond(FilterFieldTo, FilterOpIs, "ventas@acme.test"), FilterAction{Type: FilterActionFlag}),
			[]string{`address :all :is "to" "ventas@acme.test"`, `addflag "\\Flagged";`, `require ["imap4flags"];`}, []string{"fileinto"}},
		{"is sobre asunto", rule("r", cond(FilterFieldSubject, FilterOpIs, "Factura"), FilterAction{Type: FilterActionDiscard}),
			[]string{`header :is "subject" "Factura"`, "    discard;\n"}, []string{"require"}},
		{"destinatario", rule("r", cond(FilterFieldRecipient, FilterOpContains, "equipo"), FilterAction{Type: FilterActionMarkRead}),
			[]string{`header :contains ["to", "cc"] "equipo"`}, nil},
		{"cualquiera y stop", FilterRule{Name: "r", Enabled: true, Match: FilterMatchAny, Stop: true, Conditions: []FilterCondition{
			{Field: FilterFieldCc, Op: FilterOpContains, Value: "a"}, {Field: FilterFieldFrom, Op: FilterOpContains, Value: "b"},
		}, Actions: []FilterAction{{Type: FilterActionForward, Address: "fuera@otro.example"}}},
			[]string{`anyof (header :contains "cc" "a", header :contains "from" "b")`, "    redirect \"fuera@otro.example\";\n    stop;\n"},
			[]string{":copy", "require"}},
		{"reenvio con copia en regla", rule("r", cond(FilterFieldFrom, FilterOpContains, "x"),
			FilterAction{Type: FilterActionForward, Address: "fuera@otro.example", KeepCopy: true}),
			[]string{`redirect :copy "fuera@otro.example";`, `require ["copy"];`}, nil},
	}
	for _, c := range casos {
		f := normalized(t, &MailboxFilters{Rules: []FilterRule{c.rule}})
		for _, q := range c.quiere {
			if !strings.Contains(f.ScriptData, q) {
				t.Errorf("%s: falta %q en\n%s", c.nombre, q, f.ScriptData)
			}
		}
		for _, e := range c.evita {
			if strings.Contains(f.ScriptData, e) {
				t.Errorf("%s: sobra %q en\n%s", c.nombre, e, f.ScriptData)
			}
		}
	}
}

// Los textos del usuario solo entran como cadenas citadas: ni una comilla, ni una barra, ni una llave,
// ni un punto y coma cierran la cadena o abren codigo Sieve.
func TestLosTextosDelUsuarioNoInyectanSieve(t *testing.T) {
	ataques := []string{
		`"; discard; if true { redirect "robo@malo.example`,
		`\"; stop; #`,
		`fin\`,
		`} redirect "robo@malo.example"; if false {`,
		`${hex:22} ${unicode:7d}`,
		`text: multilinea`,
	}
	for _, v := range ataques {
		f := normalized(t, &MailboxFilters{Rules: []FilterRule{
			rule("r", cond(FilterFieldSubject, FilterOpContains, v), FilterAction{Type: FilterActionMove, Folder: "Carpeta " + v}),
		}})
		lines := strings.Split(f.ScriptData, "\n")
		var test, action string
		for _, l := range lines {
			if strings.HasPrefix(l, "  if ") {
				test = l
			}
			if strings.HasPrefix(l, "    fileinto") {
				action = l
			}
		}
		if test != "  if header :contains \"subject\" "+sieveQuote(v)+" {" {
			t.Errorf("%q: la prueba no es una cadena citada: %q", v, test)
		}
		if action != "    fileinto :create "+sieveQuote("Carpeta "+v)+";" {
			t.Errorf("%q: la carpeta no es una cadena citada: %q", v, action)
		}
		unquoted := unquotedSieve(t, f.ScriptData)
		for _, fuera := range []string{"redirect", "discard", "stop", "robo", "hex", "text:"} {
			if strings.Contains(unquoted, fuera) {
				t.Errorf("%q: %q aparece fuera de una cadena:\n%s", v, fuera, unquoted)
			}
		}
		if strings.Contains(f.ScriptData, `"variables"`) || strings.Contains(f.ScriptData, `"encoded-character"`) {
			t.Errorf("%q: el script no debe declarar extensiones que interpreten el texto", v)
		}
	}
}

// unquotedSieve devuelve el script sin el contenido de las cadenas citadas, siguiendo las reglas de
// escape de RFC 5228: si el texto del usuario escapara de su cadena, quedaria a la vista aqui.
func unquotedSieve(t *testing.T, script string) string {
	t.Helper()
	var out strings.Builder
	inString, escaped := false, false
	for _, r := range script {
		switch {
		case inString && escaped:
			escaped = false
		case inString && r == '\\':
			escaped = true
		case inString && r == '"':
			inString = false
			out.WriteRune(r)
		case inString:
		case r == '"':
			inString = true
			out.WriteRune(r)
		default:
			out.WriteRune(r)
		}
	}
	if inString {
		t.Fatalf("el script deja una cadena sin cerrar:\n%s", script)
	}
	return out.String()
}

func TestSaltosDeLineaYControlSeRechazan(t *testing.T) {
	for _, v := range []string{"a\nb", "a\rb", "a\tb", "a\x00b", "a b", "\xff"} {
		f := &MailboxFilters{Username: "ana@acme.test", Rules: []FilterRule{
			rule("r", cond(FilterFieldFrom, FilterOpContains, v), FilterAction{Type: FilterActionDiscard}),
		}}
		if got := fieldOf(t, f.Normalize()); got != "rules[0].conditions[0].value" {
			t.Errorf("%q: campo %q", v, got)
		}
	}
	f := &MailboxFilters{Username: "ana@acme.test", Rules: []FilterRule{
		rule("r", cond(FilterFieldFrom, FilterOpContains, "x"), FilterAction{Type: FilterActionMove, Folder: "a\nb"}),
	}}
	if got := fieldOf(t, f.Normalize()); got != "rules[0].actions[0].folder" {
		t.Errorf("carpeta con salto: %q", got)
	}
}

func TestValidacionSenalaElCampo(t *testing.T) {
	ok := func() FilterRule {
		return rule("r", cond(FilterFieldFrom, FilterOpContains, "x"), FilterAction{Type: FilterActionMarkRead})
	}
	many := func(n int) []FilterRule {
		out := make([]FilterRule, n)
		for i := range out {
			out[i] = ok()
		}
		return out
	}
	dupID := uuid.New()
	casos := map[string]struct {
		f     MailboxFilters
		campo string
	}{
		"demasiadas reglas":   {MailboxFilters{Rules: many(MaxFilterRules + 1)}, "rules"},
		"id repetido":         {MailboxFilters{Rules: []FilterRule{{ID: dupID, Name: "a", Conditions: ok().Conditions, Actions: ok().Actions}, {ID: dupID, Name: "b", Conditions: ok().Conditions, Actions: ok().Actions}}}, "rules[1].id"},
		"sin nombre":          {MailboxFilters{Rules: []FilterRule{rule(" ", ok().Conditions, ok().Actions...)}}, "rules[0].name"},
		"nombre largo":        {MailboxFilters{Rules: []FilterRule{rule(strings.Repeat("n", MaxFilterNameRunes+1), ok().Conditions, ok().Actions...)}}, "rules[0].name"},
		"match raro":          {MailboxFilters{Rules: []FilterRule{{Name: "r", Match: "some", Conditions: ok().Conditions, Actions: ok().Actions}}}, "rules[0].match"},
		"sin condiciones":     {MailboxFilters{Rules: []FilterRule{rule("r", nil, ok().Actions...)}}, "rules[0].conditions"},
		"once condiciones":    {MailboxFilters{Rules: []FilterRule{rule("r", make([]FilterCondition, MaxFilterConditions+1), ok().Actions...)}}, "rules[0].conditions"},
		"campo raro":          {MailboxFilters{Rules: []FilterRule{rule("r", cond("body", FilterOpContains, "x"), ok().Actions...)}}, "rules[0].conditions[0].field"},
		"op raro":             {MailboxFilters{Rules: []FilterRule{rule("r", cond(FilterFieldFrom, "matches", "*"), ok().Actions...)}}, "rules[0].conditions[0].op"},
		"valor vacio":         {MailboxFilters{Rules: []FilterRule{rule("r", cond(FilterFieldFrom, FilterOpContains, "  "), ok().Actions...)}}, "rules[0].conditions[0].value"},
		"valor largo":         {MailboxFilters{Rules: []FilterRule{rule("r", cond(FilterFieldFrom, FilterOpContains, strings.Repeat("v", MaxFilterValueRunes+1)), ok().Actions...)}}, "rules[0].conditions[0].value"},
		"sin acciones":        {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions)}}, "rules[0].actions"},
		"seis acciones":       {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, make([]FilterAction, MaxFilterActions+1)...)}}, "rules[0].actions"},
		"accion rara":         {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, FilterAction{Type: "vacation"})}}, "rules[0].actions[0].type"},
		"move sin carpeta":    {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, FilterAction{Type: FilterActionMove})}}, "rules[0].actions[0].folder"},
		"carpeta comodin":     {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, FilterAction{Type: FilterActionMove, Folder: "a*"})}}, "rules[0].actions[0].folder"},
		"carpeta nivel vacio": {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, FilterAction{Type: FilterActionMove, Folder: "a//b"})}}, "rules[0].actions[0].folder"},
		"move con direccion":  {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, FilterAction{Type: FilterActionMove, Folder: "a", Address: "x@y.example"})}}, "rules[0].actions[0].type"},
		"marca con carpeta":   {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, FilterAction{Type: FilterActionFlag, Folder: "a"})}}, "rules[0].actions[0].type"},
		"reenvio invalido":    {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, FilterAction{Type: FilterActionForward, Address: "no-es-correo"})}}, "rules[0].actions[0].address"},
		"reenvio a si mismo":  {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions, FilterAction{Type: FilterActionForward, Address: "ANA@acme.test"})}}, "rules[0].actions[0].address"},
		"reenvio repetido": {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions,
			FilterAction{Type: FilterActionForward, Address: "x@y.example"}, FilterAction{Type: FilterActionForward, Address: "X@y.example"})}}, "rules[0].actions[1].address"},
		"accion repetida": {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions,
			FilterAction{Type: FilterActionMove, Folder: "a"}, FilterAction{Type: FilterActionMove, Folder: "b"})}}, "rules[0].actions[1].type"},
		"descartar y archivar": {MailboxFilters{Rules: []FilterRule{rule("r", ok().Conditions,
			FilterAction{Type: FilterActionMove, Folder: "a"}, FilterAction{Type: FilterActionDiscard})}}, "rules[0].actions[1].type"},
		"regla 3 condicion 0": {MailboxFilters{Rules: append(many(3), rule("r", cond(FilterFieldFrom, FilterOpContains, "")))}, "rules[3].conditions[0].value"},
		"seis direcciones":    {MailboxFilters{Forwarding: Forwarding{Addresses: []string{"a@x.example", "b@x.example", "c@x.example", "d@x.example", "e@x.example", "f@x.example"}}}, "forwarding.addresses"},
		"direccion 1 mala":    {MailboxFilters{Forwarding: Forwarding{Addresses: []string{"a@x.example", "mala"}}}, "forwarding.addresses[1]"},
		"direccion propia":    {MailboxFilters{Forwarding: Forwarding{Addresses: []string{"ana@acme.test"}}}, "forwarding.addresses[0]"},
		"direccion repetida":  {MailboxFilters{Forwarding: Forwarding{Addresses: []string{"a@x.example", "A@x.example"}}}, "forwarding.addresses[1]"},
		"reenvio sin destino": {MailboxFilters{Forwarding: Forwarding{Enabled: true}}, "forwarding.addresses"},
	}
	for nombre, c := range casos {
		f := c.f
		f.Username = "ana@acme.test"
		if got := fieldOf(t, f.Normalize()); got != c.campo {
			t.Errorf("%s: campo %q, se esperaba %q", nombre, got, c.campo)
		}
	}
}

func TestLosTopesCabenEnElScriptDeDovecot(t *testing.T) {
	value := strings.Repeat(`"\`, MaxFilterValueRunes/2)
	conds := make([]FilterCondition, MaxFilterConditions)
	for i := range conds {
		conds[i] = FilterCondition{Field: FilterFieldRecipient, Op: FilterOpNotContains, Value: value}
	}
	rules := make([]FilterRule, MaxFilterRules)
	for i := range rules {
		rules[i] = rule("r", conds,
			FilterAction{Type: FilterActionMove, Folder: strings.Repeat("\U0001F4C1", MaxFilterFolderBytes/4)},
			FilterAction{Type: FilterActionMarkRead}, FilterAction{Type: FilterActionFlag},
			FilterAction{Type: FilterActionForward, Address: "a@x.example", KeepCopy: true},
			FilterAction{Type: FilterActionForward, Address: "b@x.example"})
	}
	f := normalized(t, &MailboxFilters{Rules: rules})
	if len(f.ScriptData) > MaxFilterScriptBytes {
		t.Fatalf("el peor caso de los topes (%d bytes) no cabe en sieve_max_script_size", len(f.ScriptData))
	}
}
