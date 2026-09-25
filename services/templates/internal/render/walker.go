package render

import (
	"fmt"
	"sort"
	"strings"
	"text/template/parse"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

// position es lo que el contexto espera de una expresion. Decide que variables caben en
// cada sitio: una lista solo se recorre, se cuenta o se prueba; nunca se imprime.
type position int

const (
	posScalar position = iota // se imprime o se pasa a una funcion de texto
	posNumber                 // se pasa a money o nonzero
	posTruth                  // condicion de if, not, and, or: cualquier tipo
	posList                   // se recorre o se cuenta
)

// usage anota como se usa un nombre de primer nivel en las tres plantillas de la version.
type usage struct {
	list, scalar, number bool
	// fields son los campos usados dentro de un range sobre esta lista; el valor dice si se
	// paso a money.
	fields map[string]bool
}

// rangeUse es un {{range}} sobre una lista y cuantos elementos muestra como mucho (0: todos).
type rangeUse struct {
	list  string
	limit int
}

// analysis es lo que el recorrido averigua de las tres plantillas de una version.
type analysis struct {
	declared map[string]domain.Variable
	uses     map[string]*usage
	ranges   []rangeUse
}

func newAnalysis(declared []domain.Variable) *analysis {
	a := &analysis{declared: make(map[string]domain.Variable, len(declared)), uses: map[string]*usage{}}
	for _, v := range declared {
		a.declared[v.Name] = v
	}
	return a
}

// names devuelve, ordenados, los nombres de primer nivel que las plantillas usan.
func (a *analysis) names() []string {
	out := make([]string, 0, len(a.uses))
	for name := range a.uses {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// listCaps devuelve el maximo de elementos que la plantilla puede mostrar de cada lista
// recorrida, y las listas que algun range recorre sin take.
func (a *analysis) listCaps() (map[string]int, []string) {
	caps := map[string]int{}
	unbounded := map[string]struct{}{}
	for _, r := range a.ranges {
		limit := r.limit
		if limit == 0 {
			limit = domain.MaxListItems
			unbounded[r.list] = struct{}{}
		}
		if limit > caps[r.list] {
			caps[r.list] = limit
		}
	}
	names := make([]string, 0, len(unbounded))
	for name := range unbounded {
		names = append(names, name)
	}
	sort.Strings(names)
	return caps, names
}

// inferMissing declara, para verificar un borrador, las variables usadas y no declaradas: una
// lista con los campos que se leen de ella y un escalar como cadena (o numero si pasa por money).
func (a *analysis) inferMissing() ([]domain.Variable, error) {
	var missing []domain.Variable
	for _, name := range a.names() {
		if _, ok := a.declared[name]; ok || domain.IsReserved(name) {
			continue
		}
		u := a.uses[name]
		switch {
		case u.list && u.scalar:
			return nil, fmt.Errorf("%w: %q se usa a la vez como lista y como valor", domain.ErrInvalidTemplate, name)
		case u.list:
			if len(u.fields) == 0 {
				return nil, fmt.Errorf("%w: declare la lista %q y sus campos", domain.ErrInvalidTemplate, name)
			}
			fields := make([]string, 0, len(u.fields))
			for f := range u.fields {
				fields = append(fields, f)
			}
			sort.Strings(fields)
			v := domain.Variable{Name: name, Type: domain.VarList}
			for _, f := range fields {
				typ := domain.VarString
				if u.fields[f] {
					typ = domain.VarNumber
				}
				v.Fields = append(v.Fields, domain.Field{Name: f, Type: typ})
			}
			missing = append(missing, v)
		case u.number:
			missing = append(missing, domain.Variable{Name: name, Type: domain.VarNumber})
		default:
			missing = append(missing, domain.Variable{Name: name, Type: domain.VarString})
		}
	}
	return missing, nil
}

// walker recorre el arbol parseado y acepta solo el subconjunto admitido: variables planas,
// {{if}}, un {{range}} sin anidar sobre una lista declarada y las funciones de funcs.go.
type walker struct {
	name string
	a    *analysis
	// scope es la lista que recorre el range en curso; vacio fuera de un range.
	scope string
}

func (w *walker) reject(n parse.Node, msg string) error {
	return fmt.Errorf("%w: %s: %s (en %q)", domain.ErrInvalidTemplate, w.name, msg, n.String())
}

func (w *walker) walk(n parse.Node) error {
	switch n := n.(type) {
	case nil:
		return nil
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, child := range n.Nodes {
			if err := w.walk(child); err != nil {
				return err
			}
		}
		return nil
	case *parse.TextNode, *parse.CommentNode:
		return nil
	case *parse.ActionNode:
		return w.pipe(n.Pipe, posScalar)
	case *parse.IfNode:
		if err := w.pipe(n.Pipe, posTruth); err != nil {
			return err
		}
		if err := w.walk(n.List); err != nil {
			return err
		}
		return w.walk(n.ElseList)
	case *parse.RangeNode:
		return w.rangeNode(n)
	case *parse.WithNode:
		return w.reject(n, "with no se admite; use {{if .variable}}")
	case *parse.TemplateNode:
		return w.reject(n, "no se admiten plantillas anidadas")
	case *parse.BreakNode, *parse.ContinueNode:
		return w.reject(n, "break y continue no se admiten")
	default:
		return w.reject(n, "construcción no admitida")
	}
}

// rangeNode acepta {{range .lista}} y {{range take N .lista}}. Dentro, el punto es el
// elemento; {{else}} se ejecuta con la lista vacia y fuera del elemento.
func (w *walker) rangeNode(n *parse.RangeNode) error {
	if w.scope != "" {
		return w.reject(n, "no se admite un range dentro de otro")
	}
	p := n.Pipe
	if len(p.Decl) > 0 {
		return w.reject(p, "no se admiten variables locales ($x)")
	}
	if len(p.Cmds) != 1 {
		return w.reject(p, "range recorre una lista: {{range .lista}} o {{range take N .lista}}")
	}
	args := p.Cmds[0].Args
	limit := 0
	if fn, ok := args[0].(*parse.IdentifierNode); ok {
		if fn.Ident != "take" || len(args) != 3 {
			return w.reject(p, "range recorre una lista: {{range .lista}} o {{range take N .lista}}")
		}
		var err error
		if limit, err = w.limit(args[1]); err != nil {
			return err
		}
		args = args[2:]
	}
	if len(args) != 1 {
		return w.reject(p, "range recorre una lista: {{range .lista}} o {{range take N .lista}}")
	}
	list, err := w.listName(args[0])
	if err != nil {
		return err
	}
	w.a.ranges = append(w.a.ranges, rangeUse{list: list, limit: limit})

	w.scope = list
	err = w.walk(n.List)
	w.scope = ""
	if err != nil {
		return err
	}
	return w.walk(n.ElseList)
}

// listName comprueba que el operando nombra una lista de primer nivel y la devuelve.
func (w *walker) listName(n parse.Node) (string, error) {
	var name string
	switch n := n.(type) {
	case *parse.FieldNode:
		if w.scope != "" || len(n.Ident) != 1 {
			return "", w.reject(n, "se esperaba una lista de la plantilla")
		}
		name = n.Ident[0]
	case *parse.VariableNode:
		if len(n.Ident) != 2 || n.Ident[0] != "$" {
			return "", w.reject(n, "no se admiten variables locales ($x)")
		}
		name = n.Ident[1]
	default:
		return "", w.reject(n, "se esperaba una lista de la plantilla")
	}
	return name, w.use(n, name, posList)
}

// limit lee el N de take y rest: un entero literal entre 1 y MaxListItems.
func (w *walker) limit(n parse.Node) (int, error) {
	num, ok := n.(*parse.NumberNode)
	if !ok || !num.IsInt || num.Int64 < 1 || num.Int64 > domain.MaxListItems {
		return 0, w.reject(n, fmt.Sprintf("take y rest reciben un entero entre 1 y %d", domain.MaxListItems))
	}
	return int(num.Int64), nil
}

func (w *walker) pipe(p *parse.PipeNode, pos position) error {
	if p == nil {
		return nil
	}
	if len(p.Decl) > 0 {
		return w.reject(p, "no se admiten variables locales ($x)")
	}
	for i, cmd := range p.Cmds {
		// En una cadena {{.x | upper}} lo que pasa de un comando al siguiente es un valor:
		// solo el comando unico de la pipeline hereda la posicion del contexto.
		cmdPos := pos
		if len(p.Cmds) > 1 {
			cmdPos = posScalar
		}
		if err := w.command(cmd, cmdPos, i > 0); err != nil {
			return err
		}
	}
	return nil
}

// command comprueba una llamada a funcion o un operando suelto. piped indica que el comando
// recibe como ultimo argumento el resultado del anterior.
func (w *walker) command(cmd *parse.CommandNode, pos position, piped bool) error {
	fn, ok := cmd.Args[0].(*parse.IdentifierNode)
	if !ok {
		if len(cmd.Args) != 1 {
			return w.reject(cmd, "expresión no admitida")
		}
		return w.operand(cmd.Args[0], pos)
	}
	if !isAllowedFunc(fn.Ident) {
		return w.reject(fn, fmt.Sprintf("función no permitida %q; se admiten %s", fn.Ident, strings.Join(allowedFuncNames(), ", ")))
	}
	args := cmd.Args[1:]
	switch fn.Ident {
	case "take":
		return w.reject(cmd, "take solo se usa como {{range take N .lista}}")
	case "count":
		if piped || len(args) != 1 {
			return w.reject(cmd, "count recibe la lista: {{count .lista}}")
		}
		return w.operand(args[0], posList)
	case "rest":
		if piped || len(args) != 2 {
			return w.reject(cmd, "rest recibe el tope y la lista: {{rest N .lista}}")
		}
		if _, err := w.limit(args[0]); err != nil {
			return err
		}
		return w.operand(args[1], posList)
	case "eq", "ne":
		// Solo un valor frente a un texto literal: comparar un numero JSON con un entero
		// literal falla al enviar, no al guardar, y eso no se admite.
		if piped || len(args) != 2 {
			return w.reject(cmd, fn.Ident+" compara una variable con un texto: {{if eq .estado \"enviado\"}}")
		}
		_, leftLiteral := args[0].(*parse.StringNode)
		_, rightLiteral := args[1].(*parse.StringNode)
		if leftLiteral == rightLiteral {
			return w.reject(cmd, fn.Ident+" compara una variable con un texto: {{if eq .estado \"enviado\"}}")
		}
		for _, arg := range args {
			if err := w.operand(arg, posScalar); err != nil {
				return err
			}
		}
		return nil
	case "and", "or", "not":
		for _, arg := range args {
			if err := w.operand(arg, posTruth); err != nil {
				return err
			}
		}
		return nil
	case "money", "nonzero":
		for _, arg := range args {
			if err := w.operand(arg, posNumber); err != nil {
				return err
			}
		}
		return nil
	default:
		for _, arg := range args {
			if err := w.operand(arg, posScalar); err != nil {
				return err
			}
		}
		return nil
	}
}

func (w *walker) operand(n parse.Node, pos position) error {
	switch n := n.(type) {
	case *parse.FieldNode:
		if len(n.Ident) != 1 {
			return w.reject(n, "solo se admiten variables planas {{.nombre}}")
		}
		if w.scope != "" {
			return w.field(n, n.Ident[0], pos)
		}
		return w.use(n, n.Ident[0], pos)
	case *parse.VariableNode:
		if len(n.Ident) != 2 || n.Ident[0] != "$" {
			return w.reject(n, "no se admiten variables locales ($x)")
		}
		return w.use(n, n.Ident[1], pos)
	case *parse.StringNode, *parse.NumberNode, *parse.BoolNode, *parse.NilNode:
		if pos == posList {
			return w.reject(n, "se esperaba una lista de la plantilla")
		}
		return nil
	case *parse.PipeNode:
		return w.pipe(n, pos)
	case *parse.IdentifierNode:
		return w.command(&parse.CommandNode{NodeType: parse.NodeCommand, Pos: n.Pos, Args: []parse.Node{n}}, pos, false)
	case *parse.DotNode:
		return w.reject(n, "{{.}} no se admite; nombre la variable")
	case *parse.ChainNode:
		return w.reject(n, "no se admiten accesos encadenados")
	default:
		return w.reject(n, "expresión no admitida")
	}
}

// use anota un nombre de primer nivel y comprueba su tipo si esta declarado.
func (w *walker) use(n parse.Node, name string, pos position) error {
	u := w.a.uses[name]
	if u == nil {
		u = &usage{}
		w.a.uses[name] = u
	}
	switch pos {
	case posList:
		u.list = true
	case posNumber:
		u.number, u.scalar = true, true
	case posScalar:
		u.scalar = true
	}
	typ := ""
	if v, ok := w.a.declared[name]; ok {
		typ = v.Type
	} else if domain.IsReserved(name) {
		typ = domain.VarString
	}
	if typ == "" {
		return nil
	}
	switch {
	case pos == posList && typ != domain.VarList:
		return w.reject(n, fmt.Sprintf("%q no es una lista", name))
	case (pos == posScalar || pos == posNumber) && typ == domain.VarList:
		return w.reject(n, fmt.Sprintf("la lista %q solo se recorre con range, se cuenta con count o se prueba con if", name))
	case pos == posNumber && typ != domain.VarNumber:
		return w.reject(n, fmt.Sprintf("money y nonzero solo reciben variables de tipo número y %q es %s", name, typ))
	}
	return nil
}

// field anota un campo de la lista que recorre el range en curso.
func (w *walker) field(n parse.Node, name string, pos position) error {
	if pos == posList {
		return w.reject(n, fmt.Sprintf("el campo %q no es una lista; no se admite un range dentro de otro", name))
	}
	u := w.a.uses[w.scope]
	if u.fields == nil {
		u.fields = map[string]bool{}
	}
	u.fields[name] = u.fields[name] || pos == posNumber
	v, ok := w.a.declared[w.scope]
	if !ok {
		return nil
	}
	f, ok := v.Field(name)
	if !ok {
		return w.reject(n, fmt.Sprintf("la lista %q no declara el campo %q; use {{$.%s}} para una variable de la plantilla", w.scope, name, name))
	}
	if pos == posNumber && f.Type != domain.VarNumber {
		return w.reject(n, fmt.Sprintf("money y nonzero solo reciben campos de tipo número y %q es %s", name, f.Type))
	}
	return nil
}
