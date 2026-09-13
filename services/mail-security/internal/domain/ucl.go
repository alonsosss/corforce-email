package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Nombres de simbolo que definen los ficheros de Rspamd copiados (composites.conf,
// multimap.conf, metadata_exporter.conf). Se conservan porque renombrarlos obligaria a
// tocar esos ficheros y no aporta nada; solo existen como constantes aqui.
const (
	SymbolAllowList = "MAILCOW_WHITE"
	SymbolDenyList  = "MAILCOW_BLACK"
)

// SymbolInternalAlias puntua el correo que llega a un alias interno desde fuera de su
// dominio. No lo declara ningun fichero de Rspamd: lo crea la propia regla de settings
// con su puntuacion, igual que hacian los mapas dinamicos originales, y conserva el
// nombre para que los paneles y registros de Rspamd sigan hablando el mismo idioma.
const SymbolInternalAlias = "MAILCOW_INTERNAL_ALIAS"

// Prioridades de las reglas: Rspamd aplica UNA sola regla de settings por mensaje, la de
// mayor prioridad que case. El watchdog manda sobre todo; una lista sobre un umbral.
const (
	priorityWatchdog      = 10
	priorityInternalAlias = 8
	priorityList          = 5
	priorityScore         = 4
)

// ScoreRule es un umbral ya resuelto: Recipients son las direcciones (buzon, sus
// variantes en dominios alias y los aliases que llegan a el) o los dominios (el dominio
// y sus dominios alias) que casan con la regla.
type ScoreRule struct {
	Object     string
	Kind       ObjectKind
	Recipients []string
	HighScore  decimal.Decimal
	LowScore   decimal.Decimal
}

// ListRule agrupa los patrones de una lista para un objeto ya resuelto.
type ListRule struct {
	Object     string
	Kind       ObjectKind
	ListKind   ListKind
	Recipients []string
	Patterns   []string
}

// InternalAliasRule es un alias que solo acepta correo de su propia organizacion.
// Domains son el dominio del alias y sus dominios alias: el alias casa en cualquiera de
// ellos como destinatario, y solo los remitentes de esos dominios pasan.
type InternalAliasRule struct {
	Address string
	Domains []string
}

// SettingsInput es todo lo que hace falta para generar el documento de /settings.
type SettingsInput struct {
	Scores          []ScoreRule
	Lists           []ListRule
	InternalAliases []InternalAliasRule
	// Maps son los bloques adicionales activos, ya validados al guardarlos.
	Maps []string
}

// RenderSettings genera el UCL settings { ... } que consume el modulo settings de
// Rspamd. La regla watchdog es obligatoria: el vigilante de los motores envia un
// mensaje a null@localhost y comprueba que required_score sea 9999.
func RenderSettings(in SettingsInput) string {
	var b strings.Builder
	b.WriteString("settings {\n")
	renderWatchdog(&b)
	for i, a := range in.InternalAliases {
		renderInternalAlias(&b, i, a)
	}
	for i, s := range in.Scores {
		renderScore(&b, i, s)
	}
	for i, l := range in.Lists {
		renderList(&b, i, l)
	}
	for _, m := range in.Maps {
		b.WriteString(indent(strings.TrimSpace(m), "  "))
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// El watchdog manda el mensaje por /scan con cabeceras MIME (To/From) y sin sobre SMTP:
// por eso las condiciones son rcpt_mime/from_mime y no rcpt/from.
func renderWatchdog(b *strings.Builder) {
	fmt.Fprintf(b, "  watchdog {\n")
	fmt.Fprintf(b, "    priority = %d;\n", priorityWatchdog)
	fmt.Fprintf(b, "    rcpt_mime = \"/null@localhost/i\";\n")
	fmt.Fprintf(b, "    from_mime = \"/watchdog@localhost/i\";\n")
	fmt.Fprintf(b, "    apply \"default\" {\n")
	fmt.Fprintf(b, "      actions {\n")
	fmt.Fprintf(b, "        reject = 9999.0;\n")
	fmt.Fprintf(b, "        greylist = 9998.0;\n")
	fmt.Fprintf(b, "        \"add header\" = 9997.0;\n")
	fmt.Fprintf(b, "      }\n")
	fmt.Fprintf(b, "      symbols_disabled = [\"HISTORY_SAVE\", \"ARC\", \"ARC_SIGNED\", \"DKIM\", \"DKIM_SIGNED\", \"CLAM_VIRUS\"];\n")
	fmt.Fprintf(b, "      want_spam = yes;\n")
	fmt.Fprintf(b, "    }\n")
	fmt.Fprintf(b, "  }\n")
}

// renderInternalAlias rechaza (puntuacion 9999) el correo que un remitente externo
// dirige a un alias interno. El remitente se juzga por el sobre: un buzon de la propia
// organizacion envia con su direccion de uno de esos dominios. La negacion se expresa
// con una busqueda anticipada porque las condiciones de settings solo casan en positivo.
func renderInternalAlias(b *strings.Builder, i int, a InternalAliasRule) {
	addr := strings.ToLower(strings.TrimSpace(a.Address))
	local, catchAll := "", strings.HasPrefix(addr, "@")
	if !catchAll {
		at := strings.LastIndex(addr, "@")
		if at <= 0 {
			return
		}
		local = addr[:at]
	}
	seen := map[string]struct{}{}
	var doms, rcpts []string
	for _, d := range a.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		lit := regexpLiteral(d)
		if lit == "" {
			continue
		}
		if _, dup := seen[lit]; dup {
			continue
		}
		seen[lit] = struct{}{}
		doms = append(doms, lit)
		if catchAll {
			rcpts = append(rcpts, `"/@`+lit+`$/i"`)
		} else {
			rcpts = append(rcpts, `"/^`+regexpLiteral(local+"@"+d)+`$/i"`)
		}
	}
	if len(doms) == 0 {
		return
	}
	fmt.Fprintf(b, "  internal_alias_%d {\n", i)
	fmt.Fprintf(b, "    priority = %d;\n", priorityInternalAlias)
	fmt.Fprintf(b, "    rcpt = [%s];\n", strings.Join(rcpts, ", "))
	fmt.Fprintf(b, "    from = \"/^(?!.*@(%s)$).*$/i\";\n", strings.Join(doms, "|"))
	fmt.Fprintf(b, "    apply \"default\" {\n")
	fmt.Fprintf(b, "      %s = 9999.0;\n", SymbolInternalAlias)
	fmt.Fprintf(b, "    }\n")
	fmt.Fprintf(b, "    symbols [\n")
	fmt.Fprintf(b, "      \"%s\"\n", SymbolInternalAlias)
	fmt.Fprintf(b, "    ]\n")
	fmt.Fprintf(b, "  }\n")
}

func renderScore(b *strings.Builder, i int, s ScoreRule) {
	rcpts := recipientRegexps(s.Kind, s.Object, s.Recipients)
	if len(rcpts) == 0 {
		return
	}
	fmt.Fprintf(b, "  score_%d {\n", i)
	fmt.Fprintf(b, "    priority = %d;\n", priorityScore)
	fmt.Fprintf(b, "    rcpt = [%s];\n", strings.Join(rcpts, ", "))
	fmt.Fprintf(b, "    apply \"default\" {\n")
	// greylist un punto por debajo del marcado, como hacian los mapas originales: sin
	// ello el greylist global (actions.conf) podria quedar por encima del add_header.
	greylist := s.LowScore.Sub(decimal.NewFromInt(1))
	if greylist.LessThan(decimal.Zero) {
		greylist = decimal.Zero
	}
	fmt.Fprintf(b, "      actions {\n")
	fmt.Fprintf(b, "        reject = %s;\n", s.HighScore.StringFixed(2))
	fmt.Fprintf(b, "        greylist = %s;\n", greylist.StringFixed(2))
	fmt.Fprintf(b, "        add_header = %s;\n", s.LowScore.StringFixed(2))
	fmt.Fprintf(b, "      }\n")
	fmt.Fprintf(b, "    }\n")
	fmt.Fprintf(b, "  }\n")
}

func renderList(b *strings.Builder, i int, l ListRule) {
	rcpts := recipientRegexps(l.Kind, l.Object, l.Recipients)
	froms := make([]string, 0, len(l.Patterns))
	for _, p := range l.Patterns {
		if re, ok := patternRegexp(p); ok {
			froms = append(froms, re)
		}
	}
	if len(rcpts) == 0 || len(froms) == 0 {
		return
	}
	name, symbol, score := "allow", SymbolAllowList, "-999.0"
	if l.ListKind == ListDeny {
		name, symbol, score = "deny", SymbolDenyList, "999.0"
	}
	fmt.Fprintf(b, "  %s_%d {\n", name, i)
	fmt.Fprintf(b, "    priority = %d;\n", priorityList)
	fmt.Fprintf(b, "    from = [%s];\n", strings.Join(froms, ", "))
	fmt.Fprintf(b, "    rcpt = [%s];\n", strings.Join(rcpts, ", "))
	fmt.Fprintf(b, "    apply \"default\" {\n")
	fmt.Fprintf(b, "      %s = %s;\n", symbol, score)
	fmt.Fprintf(b, "    }\n")
	fmt.Fprintf(b, "    symbols [\n")
	fmt.Fprintf(b, "      \"%s\"\n", symbol)
	fmt.Fprintf(b, "    ]\n")
	fmt.Fprintf(b, "  }\n")
}

// recipientRegexps traduce los destinatarios de una regla a expresiones regulares de
// Rspamd. Un dominio casa por sufijo @dominio; un buzon por igualdad.
func recipientRegexps(kind ObjectKind, object string, recipients []string) []string {
	all := append([]string{object}, recipients...)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(all))
	for _, r := range all {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		if kind == ObjectDomain {
			out = append(out, `"/@`+regexpLiteral(r)+`$/i"`)
		} else {
			out = append(out, `"/^`+regexpLiteral(r)+`$/i"`)
		}
	}
	return out
}

// patternRegexp convierte un patron de lista (direccion, @dominio o comodin) en la
// expresion regular que Rspamd evalua sobre el remitente.
func patternRegexp(pattern string) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if ValidateListPattern(p) != nil {
		return "", false
	}
	if strings.HasPrefix(p, "@") {
		return `"/^.*@` + regexpLiteral(p[1:]) + `$/i"`, true
	}
	return `"/^` + regexpLiteral(p) + `$/i"`, true
}

// regexpLiteral escapa lo que en una expresion regular tendria significado, usando
// clases de caracteres en vez de barras invertidas: asi el resultado no depende de como
// el parser de UCL trate las secuencias de escape dentro de una cadena. El comodin '*'
// se traduce a '.*'; cualquier caracter fuera del alfabeto admitido se descarta.
func regexpLiteral(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '*':
			b.WriteString(".*")
		case r == '.' || r == '+':
			b.WriteRune('[')
			b.WriteRune(r)
			b.WriteRune(']')
		case r == '@' || r == '-' || r == '_' || r == '%' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
