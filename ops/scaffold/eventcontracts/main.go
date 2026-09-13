// Command eventcontracts extrae de forma estatica el CONTRATO de cada evento NATS (que
// campos publica el emisor y que campos exige cada consumidor) y, del mismo analisis, el
// registro de quien publica y quien consume cada subject.
//
// Por que: cambiar una clave del mapa de datos no rompe ninguna compilacion — rompe al
// consumidor en produccion, en silencio. Este comando materializa el contrato en archivos
// versionados (drift detectado en CI) y, cuando puede leer ambos lados con certeza,
// comprueba que el emisor publique todo lo que el consumidor lee.
//
// Publicaciones: Publish/PublishPersistent(subject, evento) y outbox.Enqueue(ctx, q,
// subject, evento), que el rele entrega despues con el mismo subject y payload. El subject
// es un literal o una constante (del paquete o de otro paquete del modulo); si llega por
// parametro se sigue hasta cada llamada del servicio que lo fija, por nombre y aridad. Un
// subject de publicacion que no se resuelve es un error, nunca se omite. El payload se lee
// del mapa literal o del struct (etiquetas json) que va en Data, siguiendo variables,
// parametros, claves anadidas con m["clave"] = v y funciones que lo devuelven.
//
// Consumidores: Subscribe/QueueSubscribe/DurableQueueSubscribe con subject literal,
// constante o de una tabla de structs; el handler indexa evt.Data con claves literales en
// su cuerpo o en auxiliares que reciben el mapa. Si el handler lo fabrica una funcion que
// recibe el subject, las ramas que lo comparan con una constante se evaluan por subject.
//
// Lo que no es legible se marca "opaco" y se excluye de la comprobacion: no se adivina.
//
//	go run ./ops/scaffold/eventcontracts            # regenera EVENT-CONTRACTS.md y EVENTS.md
//	go run ./ops/scaffold/eventcontracts -check     # falla si hay drift, subject sin resolver,
//	                                                # subject con dos duenos o campo faltante
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	outputPath   = "docs/arquitectura/EVENT-CONTRACTS.md"
	registryPath = "docs/arquitectura/EVENTS.md"
)

type contract struct {
	Subject    string
	Publishers []string // servicios emisores; mas de uno rompe la regla de un solo dueno
	Fields     []string // campos del payload; nil + Opaque = no legible
	Opaque     bool
	Sites      map[string][]token.Pos // publicaciones por servicio
}

type result struct {
	repo       *repo
	services   []string
	pubs       []publication
	cons       []consumption
	contracts  map[string]*contract
	unresolved []string
	notices    []string
}

func main() {
	check := flag.Bool("check", false, "falla si los registros estan desactualizados, hay un subject sin resolver, con dos duenos o un campo faltante")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	os.Exit(run(root, *check, os.Stdout, os.Stderr))
}

func run(root string, check bool, stdout, stderr io.Writer) int {
	res, err := analyze(root)
	if err != nil {
		fmt.Fprintf(stderr, "eventcontracts: %v\n", err)
		return 1
	}
	for _, n := range res.notices {
		fmt.Fprintf(stderr, "aviso: %s\n", n)
	}
	reg := buildRegistry(res)
	docs := []struct{ path, content string }{
		{outputPath, renderContracts(res)},
		{registryPath, reg.render()},
	}

	if !check {
		if len(res.unresolved) > 0 {
			for _, p := range res.unresolved {
				fmt.Fprintf(stderr, "::error::%s\n", p)
			}
			fmt.Fprintln(stderr, "eventcontracts: registros sin regenerar: hay publicaciones cuyo subject no se puede resolver")
			return 1
		}
		for _, d := range docs {
			if err := os.WriteFile(filepath.Join(root, d.path), []byte(d.content), 0o644); err != nil {
				fmt.Fprintf(stderr, "eventcontracts: %v\n", err)
				return 1
			}
		}
		fmt.Fprintf(stdout, "%s actualizado: %d subjects publicados, %d consumos\n", outputPath, len(res.contracts), len(res.cons))
		fmt.Fprintf(stdout, "%s actualizado: %d subjects, %d pub, %d sub\n", registryPath, len(reg.rows), reg.npub, reg.ncon)
		if len(reg.orphans) > 0 {
			fmt.Fprintln(stdout, "Subjects consumidos sin publicador (revisar):")
			for _, s := range reg.orphans {
				fmt.Fprintf(stdout, "  - %s\n", s)
			}
		}
		return 0
	}

	problems := append(append([]string{}, res.unresolved...), verify(res)...)
	for _, d := range docs {
		current, err := os.ReadFile(filepath.Join(root, d.path))
		if err != nil || string(current) != d.content {
			problems = append(problems, fmt.Sprintf(
				"%s desactualizado: corre 'make gen-event-contracts' y commitea el cambio", d.path))
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintf(stderr, "::error::%s\n", p)
		}
		return 1
	}
	fmt.Fprintf(stdout, "Contratos de eventos al dia: %d subjects, sin campos faltantes.\n", len(res.contracts))
	return 0
}

func analyze(root string) (*result, error) {
	r, err := newRepo(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, "services"))
	if err != nil {
		return nil, err
	}
	res := &result{repo: r}
	a := &analyzer{r: r}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		svc, err := r.loadService(e.Name())
		if err != nil {
			return nil, err
		}
		res.services = append(res.services, e.Name())
		a.svc = svc
		a.collectPublishers()
		a.collectConsumers()
	}
	res.pubs, res.cons = a.pubs, a.cons
	res.contracts = buildContracts(a.pubs)
	sort.Strings(a.problems)
	res.unresolved = dedupe(a.problems)
	sort.Strings(a.notices)
	res.notices = dedupe(a.notices)
	return res, nil
}

// buildContracts acumula lo hallado por subject. Un mismo subject emitido desde varios
// sitios une sus campos, y basta que uno sea opaco para no afirmar nada sobre el.
func buildContracts(pubs []publication) map[string]*contract {
	out := map[string]*contract{}
	for _, p := range pubs {
		c, ok := out[p.Subject]
		if !ok {
			c = &contract{Subject: p.Subject, Sites: map[string][]token.Pos{}}
			out[p.Subject] = c
		}
		c.Fields = union(c.Fields, p.Fields)
		c.Opaque = c.Opaque || p.Opaque
		if !contains(c.Publishers, p.Service) {
			c.Publishers = append(c.Publishers, p.Service)
			sort.Strings(c.Publishers)
		}
		sites := c.Sites[p.Service]
		found := false
		for _, s := range sites {
			found = found || s == p.Pos
		}
		if !found {
			sites = append(sites, p.Pos)
			sort.Slice(sites, func(i, j int) bool { return sites[i] < sites[j] })
			c.Sites[p.Service] = sites
		}
	}
	return out
}

// verify comprueba que cada subject tenga un solo dueno y que cada campo que un
// consumidor lee lo publique su emisor (o, con comodin, alguno de los subjects que
// recibe). Solo se pronuncia cuando ambos lados son legibles: un lado opaco se excluye,
// para no inventar contratos que no se pueden leer.
func verify(res *result) []string {
	r := res.repo
	var problems []string
	for _, subject := range sortedContractKeys(res.contracts) {
		c := res.contracts[subject]
		if len(c.Publishers) < 2 {
			continue
		}
		who := make([]string, 0, len(c.Publishers))
		for _, s := range c.Publishers {
			who = append(who, fmt.Sprintf("%s (%s)", s, r.rel(c.Sites[s][0])))
		}
		problems = append(problems, fmt.Sprintf("%s: lo publican %s; un subject tiene un solo dueno",
			subject, strings.Join(who, " y ")))
	}
	for _, c := range res.cons {
		if c.Opaque {
			continue
		}
		if isPattern(c.Subject) {
			problems = append(problems, verifyPattern(res, c)...)
			continue
		}
		pub, ok := res.contracts[c.Subject]
		if !ok || pub.Opaque {
			continue
		}
		if missing := missingFields(c.Fields, pub.Fields); len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%s: %s lee %s, pero %s no lo publica (%s; campos: %v)",
				c.Subject, c.Consumer, readSites(r, c, missing), strings.Join(pub.Publishers, ", "),
				pubSites(r, pub), pub.Fields))
		}
	}
	sort.Strings(problems)
	return dedupe(problems)
}

func verifyPattern(res *result, c consumption) []string {
	var matched []string
	have := map[string]bool{}
	for subject, pc := range res.contracts {
		if !matchSubject(c.Subject, subject) {
			continue
		}
		if pc.Opaque {
			return nil
		}
		matched = append(matched, subject)
		for _, f := range pc.Fields {
			have[f] = true
		}
	}
	if len(matched) == 0 {
		return nil
	}
	var missing []string
	for _, f := range c.Fields {
		if !have[f] {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(matched)
	return []string{fmt.Sprintf("%s: %s lee %s, pero ninguno de los subjects que recibe lo publica (%s)",
		c.Subject, c.Consumer, readSites(res.repo, c, missing), strings.Join(matched, ", "))}
}

func missingFields(read, published []string) []string {
	var missing []string
	for _, f := range read {
		if !contains(published, f) {
			missing = append(missing, f)
		}
	}
	return missing
}

func readSites(r *repo, c consumption, fields []string) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, fmt.Sprintf("`%s` en %s", f, r.rel(c.FieldPos[f])))
	}
	return strings.Join(parts, ", ")
}

func pubSites(r *repo, c *contract) string {
	var parts []string
	for _, s := range c.Publishers {
		for _, pos := range c.Sites[s] {
			parts = append(parts, "publica en "+r.rel(pos))
		}
	}
	return strings.Join(parts, ", ")
}

func renderContracts(res *result) string {
	var b strings.Builder
	b.WriteString("# Contratos de eventos\n\n")
	b.WriteString("Generado — no editar a mano. Regenera con `make gen-event-contracts`.\n\n")
	b.WriteString("Campos del payload (`Event.Data`) por subject, extraidos del codigo. Un cambio en\n")
	b.WriteString("esta tabla es un cambio de contrato: quitar o renombrar un campo rompe a sus\n")
	b.WriteString("consumidores en tiempo de ejecucion, no de compilacion. Cuenta lo publicado en el\n")
	b.WriteString("bus y lo encolado en la outbox (`outbox.Enqueue`), que el rele entrega con el mismo\n")
	b.WriteString("subject y payload. `opaco` = el payload no se puede leer estaticamente.\n\n")

	b.WriteString("## Publicado\n\n")
	b.WriteString("| Subject | Servicio | Campos |\n|---|---|---|\n")
	for _, subject := range sortedContractKeys(res.contracts) {
		c := res.contracts[subject]
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s |\n", subject, strings.Join(c.Publishers, ", "), fieldList(c.Fields, c.Opaque)))
	}

	b.WriteString("\n## Consumido\n\n")
	b.WriteString("| Subject | Servicio | Campos que lee |\n|---|---|---|\n")
	rows := make([]string, 0, len(res.cons))
	for _, c := range res.cons {
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

func exprString(e ast.Expr) string { return types.ExprString(e) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedPosKeys(m map[string]token.Pos) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedContractKeys(m map[string]*contract) []string {
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
