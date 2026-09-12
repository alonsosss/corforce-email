// Command eventcontracts extrae de forma estatica el CONTRATO de cada evento NATS: que
// campos publica el emisor y que campos exige cada consumidor.
//
// Por que: EVENTS.md ya registra quien publica y quien consume cada subject, pero no que
// lleva dentro el payload. Hoy cambiar una clave del mapa de datos no rompe ninguna
// compilacion — rompe al consumidor en produccion, en silencio. Este comando materializa
// el contrato en un archivo versionado (drift detectado en CI) y, cuando puede leer ambos
// lados con certeza, comprueba que el emisor publique todo lo que el consumidor lee.
//
// Alcance deliberado: solo entiende el patron dominante del repo — Publish(subject,
// events.Event{Data: map[...]{...}}) y consumidores que indexan el mapa con claves
// literales. Si un lado no es legible estaticamente se marca "opaco" y se excluye de la
// comprobacion, nunca se adivina.
//
//	go run ./ops/scaffold/eventcontracts            # regenera el registro
//	go run ./ops/scaffold/eventcontracts -check     # falla si hay drift o campo faltante
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const outputPath = "docs/arquitectura/EVENT-CONTRACTS.md"

type contract struct {
	Subject   string
	Publisher string   // servicio emisor
	Fields    []string // campos del payload; nil + Opaque = no legible
	Opaque    bool
}

type consumption struct {
	Subject  string
	Consumer string
	Fields   []string
	Opaque   bool
}

func main() {
	check := flag.Bool("check", false, "falla si el registro esta desactualizado o hay un campo faltante")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	published, consumed, err := scan(filepath.Join(root, "services"))
	if err != nil {
		fail(err)
	}

	rendered := render(published, consumed)
	target := filepath.Join(root, outputPath)

	if !*check {
		if err := os.WriteFile(target, []byte(rendered), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("%s actualizado: %d subjects publicados, %d consumos\n",
			outputPath, len(published), len(consumed))
		return
	}

	problems := verify(published, consumed)
	current, err := os.ReadFile(target)
	if err != nil || string(current) != rendered {
		problems = append(problems, fmt.Sprintf(
			"%s desactualizado: corre 'make gen-event-contracts' y commitea el cambio", outputPath))
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "::error::%s\n", p)
		}
		os.Exit(1)
	}
	fmt.Printf("Contratos de eventos al dia: %d subjects, sin campos faltantes.\n", len(published))
}

// verify comprueba que cada campo que un consumidor lee lo publique su emisor. Solo se
// pronuncia cuando ambos lados son legibles: un lado opaco se reporta como informacion,
// no como fallo, para no inventar contratos que no se pueden leer.
func verify(published map[string]contract, consumed []consumption) []string {
	var problems []string
	for _, c := range consumed {
		if c.Opaque || strings.ContainsAny(c.Subject, "*>") {
			continue
		}
		pub, ok := published[c.Subject]
		if !ok || pub.Opaque {
			continue
		}
		have := make(map[string]bool, len(pub.Fields))
		for _, f := range pub.Fields {
			have[f] = true
		}
		var missing []string
		for _, f := range c.Fields {
			if !have[f] {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf(
				"%s: %s lee %v, pero %s no lo publica (campos: %v)",
				c.Subject, c.Consumer, missing, pub.Publisher, pub.Fields))
		}
	}
	sort.Strings(problems)
	return problems
}

func scan(servicesDir string) (map[string]contract, []consumption, error) {
	published := map[string]contract{}
	var consumed []consumption

	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		service := e.Name()
		var files []*ast.File
		err := filepath.WalkDir(filepath.Join(servicesDir, service), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil // un archivo que no parsea no es asunto de este check
			}
			files = append(files, file)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		// Dos pasadas: primero las constantes de cadena de TODO el servicio, porque el
		// subject suele declararse como constante junto al worker y la llamada la usa por
		// nombre. Sin esto el consumo desaparece del mapa sin que nada falle: el servicio
		// sigue suscrito y el documento dice que nadie escucha.
		consts, ambiguos := map[string]string{}, map[string]bool{}
		for _, file := range files {
			collectStringConsts(file, consts, ambiguos)
		}
		for _, file := range files {
			collectPublishers(file, service, published, consts, ambiguos)
			consumed = append(consumed, collectConsumers(file, service, consts, ambiguos)...)
		}
	}
	return published, consumed, nil
}

func collectPublishers(file *ast.File, service string, out map[string]contract, consts map[string]string, ambiguos map[string]bool) {
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Publish" && sel.Sel.Name != "PublishPersistent") {
			return true
		}
		subject, ok := subjectArg(call.Args[0], consts, ambiguos)
		if !ok {
			return true // subject dinamico: no hay contrato que fijar
		}
		lit, ok := call.Args[1].(*ast.CompositeLit)
		if !ok {
			return true
		}
		c := contract{Subject: subject, Publisher: service}

		// Forma directa: Publish(subject, map{...}). La usan los servicios cuyo
		// publisher envuelve el evento por dentro; el payload es igual de legible.
		if fields, readable := mapLiteralKeys(lit); readable {
			c.Fields = fields
			c.Opaque = false
			mergeContract(out, subject, service, c)
			return true
		}

		dataFound := false
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if ident, ok := kv.Key.(*ast.Ident); !ok || ident.Name != "Data" {
				continue
			}
			dataFound = true
			fields, readable := mapLiteralKeys(kv.Value)
			c.Fields, c.Opaque = fields, !readable
		}
		if !dataFound {
			c.Opaque = true
		}
		mergeContract(out, subject, service, c)
		return true
	})
}

// mergeContract acumula lo hallado para un subject. Un mismo subject emitido desde varios
// sitios une sus campos, y basta que uno sea opaco para no afirmar nada sobre el.
func mergeContract(out map[string]contract, subject, service string, c contract) {
	if prev, exists := out[subject]; exists {
		c.Fields = union(prev.Fields, c.Fields)
		c.Opaque = prev.Opaque || c.Opaque
		if prev.Publisher != service {
			c.Publisher = prev.Publisher + ", " + service
		}
	}
	sort.Strings(c.Fields)
	out[subject] = c
}

func collectConsumers(file *ast.File, service string, consts map[string]string, ambiguos map[string]bool) []consumption {
	var out []consumption
	methods := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			methods[fn.Name.Name] = fn
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Subscribe", "QueueSubscribe", "DurableQueueSubscribe":
		default:
			return true
		}
		subject, ok := subjectArg(call.Args[0], consts, ambiguos)
		if !ok {
			return true
		}
		handler := call.Args[len(call.Args)-1]
		var body *ast.BlockStmt
		switch h := handler.(type) {
		case *ast.FuncLit:
			body = h.Body
		case *ast.SelectorExpr:
			if fn, found := methods[h.Sel.Name]; found {
				body = fn.Body
			}
		case *ast.Ident:
			if fn, found := methods[h.Name]; found {
				body = fn.Body
			}
		}
		if body == nil {
			out = append(out, consumption{Subject: subject, Consumer: service, Opaque: true})
			return true
		}
		fields, readable := payloadKeysRead(body)
		out = append(out, consumption{
			Subject: subject, Consumer: service, Fields: fields, Opaque: !readable,
		})
		return true
	})
	return out
}

// payloadKeysRead busca la variable que recibe evt.Data por asercion de tipo y devuelve
// las claves literales con las que se la indexa. Si el handler no usa ese patron (p.ej.
// deserializa a un struct), se declara opaco: no se adivina.
func payloadKeysRead(body *ast.BlockStmt) ([]string, bool) {
	var dataVars []string
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		ta, ok := assign.Rhs[0].(*ast.TypeAssertExpr)
		if !ok {
			return true
		}
		sel, ok := ta.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Data" {
			return true
		}
		if ident, ok := assign.Lhs[0].(*ast.Ident); ok && ident.Name != "_" {
			dataVars = append(dataVars, ident.Name)
		}
		return true
	})
	if len(dataVars) == 0 {
		return nil, false
	}

	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		idx, ok := n.(*ast.IndexExpr)
		if !ok {
			return true
		}
		ident, ok := idx.X.(*ast.Ident)
		if !ok || !contains(dataVars, ident.Name) {
			return true
		}
		if key, ok := stringLit(idx.Index); ok {
			seen[key] = true
		}
		return true
	})
	return sortedKeys(seen), true
}

// mapLiteralKeys extrae las claves de un literal map[...]{...}. Devuelve readable=false
// si el valor no es un literal (p.ej. una variable construida en otro sitio).
func mapLiteralKeys(v ast.Expr) ([]string, bool) {
	lit, ok := v.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	if _, ok := lit.Type.(*ast.MapType); !ok {
		return nil, false
	}
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := stringLit(kv.Key); ok {
			seen[key] = true
		}
	}
	return sortedKeys(seen), true
}

func render(published map[string]contract, consumed []consumption) string {
	var b strings.Builder
	b.WriteString("# Contratos de eventos\n\n")
	b.WriteString("Generado — no editar a mano. Regenera con `make gen-event-contracts`.\n\n")
	b.WriteString("Campos del payload (`Event.Data`) por subject, extraidos del codigo. Un cambio en\n")
	b.WriteString("esta tabla es un cambio de contrato: quitar o renombrar un campo rompe a sus\n")
	b.WriteString("consumidores en tiempo de ejecucion, no de compilacion. `opaco` = el payload se\n")
	b.WriteString("construye fuera del literal y no se puede leer estaticamente.\n\n")

	b.WriteString("## Publicado\n\n")
	b.WriteString("| Subject | Servicio | Campos |\n|---|---|---|\n")
	for _, subject := range sortedContractKeys(published) {
		c := published[subject]
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s |\n", subject, c.Publisher, fieldList(c.Fields, c.Opaque)))
	}

	b.WriteString("\n## Consumido\n\n")
	b.WriteString("| Subject | Servicio | Campos que lee |\n|---|---|---|\n")
	rows := make([]string, 0, len(consumed))
	for _, c := range consumed {
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |\n", c.Subject, c.Consumer, fieldList(c.Fields, c.Opaque)))
	}
	sort.Strings(rows)
	rows = dedupe(rows)
	for _, r := range rows {
		b.WriteString(r)
	}
	return b.String()
}

func fieldList(fields []string, opaque bool) string {
	if opaque {
		return "_opaco_"
	}
	if len(fields) == 0 {
		return "_sin campos_"
	}
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		quoted = append(quoted, "`"+f+"`")
	}
	return strings.Join(quoted, ", ")
}

// collectStringConsts junta las constantes (y variables) de cadena declaradas en el
// archivo. Un nombre que aparece con DOS valores distintos en el mismo servicio queda
// marcado como ambiguo y deja de resolverse: preferimos perder ese subject a atribuirlo
// mal.
func collectStringConsts(file *ast.File, out map[string]string, ambiguos map[string]bool) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, name := range vs.Names {
				valor, ok := stringLit(vs.Values[i])
				if !ok {
					continue
				}
				if prev, existe := out[name.Name]; existe && prev != valor {
					ambiguos[name.Name] = true
				}
				out[name.Name] = valor
			}
		}
	}
}

// subjectArg resuelve el primer argumento de Publish/Subscribe: un literal, o el nombre de
// una constante de cadena del mismo servicio.
func subjectArg(e ast.Expr, consts map[string]string, ambiguos map[string]bool) (string, bool) {
	if s, ok := stringLit(e); ok {
		return s, true
	}
	ident, ok := e.(*ast.Ident)
	if !ok || ambiguos[ident.Name] {
		return "", false
	}
	s, ok := consts[ident.Name]
	return s, ok
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedContractKeys(m map[string]contract) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range append(append([]string{}, a...), b...) {
		seen[s] = true
	}
	return sortedKeys(seen)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	var prev string
	for i, s := range sorted {
		if i > 0 && s == prev {
			continue
		}
		out = append(out, s)
		prev = s
	}
	return out
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no se encontro la raiz del repo (go.mod)")
		}
		dir = parent
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "eventcontracts: %v\n", err)
	os.Exit(1)
}
