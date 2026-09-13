package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
)

// maxHelperDepth acota cuantas funciones auxiliares se siguen al buscar lecturas del mapa.
const maxHelperDepth = 3

type consumption struct {
	Subject  string
	Consumer string
	Fields   []string
	FieldPos map[string]token.Pos // primera lectura de cada campo
	Opaque   bool
	Pos      token.Pos
}

var subscribeMethods = map[string]bool{"Subscribe": true, "QueueSubscribe": true, "DurableQueueSubscribe": true}

func (a *analyzer) collectConsumers() {
	for _, fn := range a.svc.funcs {
		var stack []ast.Node
		ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && subscribeMethods[sel.Sel.Name] {
				a.consumeCall(call, fn, stack)
			}
			return true
		})
	}
}

// consumeCall registra una suscripcion. Un subject que no se resuelve (p.ej. uno que
// viene de configuracion) se avisa: no se puede atribuir, pero tampoco se calla.
func (a *analyzer) consumeCall(call *ast.CallExpr, fn *funcInfo, stack []ast.Node) {
	subjArg, handler := call.Args[0], call.Args[len(call.Args)-1]
	report := func(pos token.Pos, why string) {
		a.notices = append(a.notices, fmt.Sprintf(
			"%s: %s se suscribe sin subject resoluble (%s); no entra en el registro", a.r.rel(pos), a.svc.name, why))
	}
	if sel, ok := subjArg.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			if rng := enclosingRange(stack, id.Name); rng != nil {
				a.consumeTable(call, fn, rng, sel, handler, report)
				return
			}
		}
	}
	a.expand(chain{{fn: fn, file: fn.file}}, 0, func(ch chain) error {
		subject, err := a.evalString(subjArg, ch, 0, 0)
		if err != nil {
			return err
		}
		return a.addConsumption(subject, handler, ch, "", call)
	}, report)
}

// consumeTable cubre el patron "for _, s := range tabla { Subscribe(s.subject, ...) }":
// los subjects son los que fijan los literales del struct de la tabla en su paquete.
func (a *analyzer) consumeTable(call *ast.CallExpr, fn *funcInfo, rng *ast.RangeStmt, sel *ast.SelectorExpr, handler ast.Expr, report func(token.Pos, string)) {
	elem, pkg := a.rangeElem(rng.X, fn, fn.file, 0)
	if elem == "" {
		report(call.Pos(), "no se sabe de que tabla sale "+exprString(sel))
		return
	}
	subjects := a.fieldValues(pkg, elem, sel.Sel.Name, report)
	if len(subjects) == 0 {
		report(call.Pos(), fmt.Sprintf("ningun literal de %s fija %s", elem, sel.Sel.Name))
		return
	}
	ch := chain{{fn: fn, file: fn.file}}
	for _, subject := range subjects {
		if err := a.addConsumption(subject, handler, ch, exprString(sel), call); err != nil {
			a.cons = append(a.cons, consumption{Subject: subject, Consumer: a.svc.name, Opaque: true, Pos: call.Pos()})
		}
	}
}

func enclosingRange(stack []ast.Node, name string) *ast.RangeStmt {
	for k := len(stack) - 1; k >= 0; k-- {
		rng, ok := stack[k].(*ast.RangeStmt)
		if !ok {
			continue
		}
		for _, e := range []ast.Expr{rng.Key, rng.Value} {
			if id, ok := e.(*ast.Ident); ok && id.Name == name {
				return rng
			}
		}
	}
	return nil
}

// rangeElem da el tipo de elemento de lo que se recorre: parametro, variable local o de
// paquete, literal, append o conversion a []T, o resultado de una funcion.
func (a *analyzer) rangeElem(x ast.Expr, fn *funcInfo, file *fileInfo, depth int) (string, *pkgInfo) {
	if depth > 4 {
		return "", nil
	}
	switch e := x.(type) {
	case *ast.ParenExpr:
		return a.rangeElem(e.X, fn, file, depth+1)
	case *ast.Ident:
		if fn != nil {
			if idx := fn.paramIndex(e.Name); idx >= 0 {
				return a.sliceElem(fn.paramTypes[idx], fn.file)
			}
			if li := localDecl(fn.decl.Body, e.Name); li.found {
				for _, v := range li.values {
					if t, p := a.rangeElem(v, fn, file, depth+1); t != "" {
						return t, p
					}
				}
				return "", nil
			}
		}
		if def, ok := file.pkg.values[e.Name]; ok {
			if def.typ != nil {
				return a.sliceElem(def.typ, def.file)
			}
			if def.expr != nil {
				return a.rangeElem(def.expr, nil, def.file, depth+1)
			}
		}
	case *ast.CompositeLit:
		return a.sliceElem(e.Type, file)
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "append" && len(e.Args) > 0 {
			return a.rangeElem(e.Args[0], fn, file, depth+1)
		}
		if t, p := a.sliceElem(e.Fun, file); t != "" {
			return t, p
		}
		if callee := a.callee(e, file, fn); callee != nil && callee.decl.Type.Results != nil && len(callee.decl.Type.Results.List) > 0 {
			return a.sliceElem(callee.decl.Type.Results.List[0].Type, callee.file)
		}
	}
	return "", nil
}

func (a *analyzer) sliceElem(t ast.Expr, file *fileInfo) (string, *pkgInfo) {
	if p, ok := t.(*ast.ParenExpr); ok {
		t = p.X
	}
	arr, ok := t.(*ast.ArrayType)
	if !ok {
		return "", nil
	}
	elt := arr.Elt
	if star, ok := elt.(*ast.StarExpr); ok {
		elt = star.X
	}
	if id, ok := elt.(*ast.Ident); ok {
		return id.Name, file.pkg
	}
	return "", nil
}

// fieldValues: el valor de field en cada literal de typeName del paquete, explicito o
// elidido dentro de un []typeName{...}.
func (a *analyzer) fieldValues(pkg *pkgInfo, typeName, field string, report func(token.Pos, string)) []string {
	seen := map[string]bool{}
	var out []string
	take := func(lit *ast.CompositeLit, fi *fileInfo) {
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if id, ok := kv.Key.(*ast.Ident); !ok || id.Name != field {
				continue
			}
			s, err := a.evalString(kv.Value, chain{{file: fi}}, 0, 0)
			if err != nil {
				report(kv.Value.Pos(), err.Error())
				continue
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	named := func(t ast.Expr) bool {
		if star, ok := t.(*ast.StarExpr); ok {
			t = star.X
		}
		id, ok := t.(*ast.Ident)
		return ok && id.Name == typeName
	}
	for _, fi := range pkg.files {
		ast.Inspect(fi.ast, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if named(lit.Type) {
				take(lit, fi)
				return true
			}
			if arr, ok := lit.Type.(*ast.ArrayType); ok && named(arr.Elt) {
				for _, elt := range lit.Elts {
					if inner, ok := elt.(*ast.CompositeLit); ok && inner.Type == nil {
						take(inner, fi)
					}
				}
			}
			return true
		})
	}
	return out
}

func (a *analyzer) addConsumption(subject string, handler ast.Expr, ch chain, tableSel string, call *ast.CallExpr) error {
	h, err := a.handlerBody(handler, ch, 0, subject, tableSel, 0)
	if err != nil {
		return err
	}
	c := consumption{Subject: subject, Consumer: a.svc.name, Pos: call.Pos(), Opaque: true}
	if h.body != nil {
		if reads, ok := a.payloadKeysRead(h.body, h.file, h.fn, h.bindings); ok {
			c.Opaque = false
			c.FieldPos = reads
			c.Fields = sortedPosKeys(reads)
		}
	}
	a.cons = append(a.cons, c)
	return nil
}

type handlerInfo struct {
	body     *ast.BlockStmt
	file     *fileInfo
	fn       *funcInfo         // funcion que contiene el cuerpo: da el receptor para resolver metodos
	bindings map[string]string // parametros de la fabrica del handler con valor conocido
}

// handlerBody localiza el cuerpo del handler: closure, funcion o metodo (del archivo o,
// si es inequivoco, del paquete), o el closure que devuelve una fabrica como
// w.handle(subject); en ese caso sus parametros quedan ligados a su valor.
func (a *analyzer) handlerBody(e ast.Expr, ch chain, i int, subject, tableSel string, depth int) (handlerInfo, error) {
	if depth > 4 {
		return handlerInfo{}, nil
	}
	file, ctx := ch[i].file, ch[i].fn
	switch h := e.(type) {
	case *ast.ParenExpr:
		return a.handlerBody(h.X, ch, i, subject, tableSel, depth+1)
	case *ast.FuncLit:
		return handlerInfo{body: h.Body, file: file, fn: ctx}, nil
	case *ast.Ident:
		if ctx != nil {
			if idx := ctx.paramIndex(h.Name); idx >= 0 {
				arg, err := boundArg(ch, i, idx)
				if err != nil {
					if errors.Is(err, errUnbound) {
						return handlerInfo{}, err
					}
					return handlerInfo{}, nil
				}
				return a.handlerBody(arg, ch, i+1, subject, tableSel, depth+1)
			}
			if li := localDecl(ctx.decl.Body, h.Name); li.found {
				if !li.opaque && len(li.values) == 1 {
					return a.handlerBody(li.values[0], ch, i, subject, tableSel, depth+1)
				}
				return handlerInfo{}, nil
			}
		}
		if f := a.lookupFunc(h, file, ctx); f != nil {
			return handlerInfo{body: f.decl.Body, file: f.file, fn: f}, nil
		}
	case *ast.SelectorExpr:
		if f := a.lookupFunc(h, file, ctx); f != nil {
			return handlerInfo{body: f.decl.Body, file: f.file, fn: f}, nil
		}
	case *ast.CallExpr:
		f := a.lookupFunc(h.Fun, file, ctx)
		if f == nil {
			return handlerInfo{}, nil
		}
		lit := returnedFuncLit(f.decl.Body)
		if lit == nil {
			return handlerInfo{}, nil
		}
		bindings := map[string]string{}
		for k, p := range f.params {
			if k >= len(h.Args) || p == "" || p == "_" {
				continue
			}
			arg := h.Args[k]
			if tableSel != "" && exprString(arg) == tableSel {
				bindings[p] = subject
				continue
			}
			if v, err := a.evalString(arg, ch, i, 0); err == nil {
				bindings[p] = v
			}
		}
		return handlerInfo{body: lit.Body, file: f.file, fn: f, bindings: bindings}, nil
	}
	return handlerInfo{}, nil
}

// lookupFunc resuelve una funcion por nombre: la del mismo archivo o, si no la hay, la
// unica del paquete; un metodo sobre el receptor de ctx, solo entre los de su tipo; con
// prefijo de paquete, la de ese paquete del modulo.
func (a *analyzer) lookupFunc(fun ast.Expr, file *fileInfo, ctx *funcInfo) *funcInfo {
	var name, self string
	method := false
	switch f := fun.(type) {
	case *ast.Ident:
		name = f.Name
	case *ast.SelectorExpr:
		if a.r.isPkgName(file, f.X) {
			pkg := a.r.importedPkg(file, f.X.(*ast.Ident).Name)
			if pkg == nil {
				return nil
			}
			var match *funcInfo
			for _, fn := range pkg.funcs {
				if !fn.isMethod() && fn.name() == f.Sel.Name {
					if match != nil {
						return nil
					}
					match = fn
				}
			}
			return match
		}
		name, method, self = f.Sel.Name, true, selfType(f, ctx)
	default:
		return nil
	}
	var sameFile, samePkg []*funcInfo
	for _, fn := range file.pkg.funcs {
		if fn.name() != name || fn.isMethod() != method || (self != "" && fn.recvType() != self) {
			continue
		}
		samePkg = append(samePkg, fn)
		if fn.file == file {
			sameFile = append(sameFile, fn)
		}
	}
	if len(sameFile) == 1 {
		return sameFile[0]
	}
	if len(sameFile) == 0 && len(samePkg) == 1 {
		return samePkg[0]
	}
	return nil
}

func returnedFuncLit(body *ast.BlockStmt) *ast.FuncLit {
	var lit *ast.FuncLit
	ast.Inspect(body, func(n ast.Node) bool {
		if lit != nil {
			return false
		}
		switch s := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(s.Results) > 0 {
				lit, _ = s.Results[0].(*ast.FuncLit)
			}
		}
		return true
	})
	return lit
}

// payloadKeysRead busca la variable que recibe evt.Data por asercion de tipo y devuelve
// las claves literales con las que se la indexa, tambien en las funciones auxiliares a
// las que se pasa el mapa. Con parametros ligados se descartan las ramas que no corren
// para ese subject. Si el handler no usa ese patron (p.ej. deserializa a un struct), se
// declara opaco: no se adivina.
func (a *analyzer) payloadKeysRead(body *ast.BlockStmt, file *fileInfo, fn *funcInfo, bindings map[string]string) (map[string]token.Pos, bool) {
	dead := a.deadNodes(body, file, bindings)
	var dataVars []string
	ast.Inspect(body, func(n ast.Node) bool {
		if dead[n] {
			return false
		}
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
	reads := map[string]token.Pos{}
	a.collectReads(body, file, fn, dead, dataVars, reads, map[*ast.FuncDecl]bool{}, 0)
	return reads, true
}

func (a *analyzer) collectReads(body *ast.BlockStmt, file *fileInfo, fn *funcInfo, dead map[ast.Node]bool, vars []string, reads map[string]token.Pos, visited map[*ast.FuncDecl]bool, depth int) {
	ast.Inspect(body, func(n ast.Node) bool {
		if dead[n] {
			return false
		}
		switch x := n.(type) {
		case *ast.IndexExpr:
			ident, ok := x.X.(*ast.Ident)
			if !ok || !contains(vars, ident.Name) {
				return true
			}
			if key, err := a.evalString(x.Index, chain{{file: file}}, 0, 0); err == nil {
				if _, seen := reads[key]; !seen {
					reads[key] = x.Pos()
				}
			}
		case *ast.CallExpr:
			if depth >= maxHelperDepth {
				return true
			}
			for k, arg := range x.Args {
				id, ok := arg.(*ast.Ident)
				if !ok || !contains(vars, id.Name) {
					continue
				}
				f := a.lookupFunc(x.Fun, file, fn)
				if f == nil || visited[f.decl] || !f.accepts(len(x.Args)) || k >= len(f.params) {
					continue
				}
				if p := f.params[k]; p != "" && p != "_" {
					visited[f.decl] = true
					a.collectReads(f.decl.Body, f.file, f, nil, []string{p}, reads, visited, depth+1)
				}
			}
		}
		return true
	})
}

// deadNodes marca lo que no corre cuando los parametros ligados valen lo que valen: la
// rama descartada de un if o switch que compara uno de ellos con una constante, y lo que
// sigue en el bloque a una rama tomada que termina.
func (a *analyzer) deadNodes(body *ast.BlockStmt, file *fileInfo, bindings map[string]string) map[ast.Node]bool {
	dead := map[ast.Node]bool{}
	if len(bindings) == 0 {
		return dead
	}
	pruneIf := func(s *ast.IfStmt) bool {
		for s != nil {
			taken, known := a.evalCond(s.Cond, file, bindings)
			if !known {
				return false
			}
			if taken {
				if s.Else != nil {
					dead[s.Else] = true
				}
				return terminates(s.Body.List)
			}
			dead[s.Body] = true
			switch e := s.Else.(type) {
			case *ast.IfStmt:
				s = e
			case *ast.BlockStmt:
				return terminates(e.List)
			default:
				return false
			}
		}
		return false
	}
	pruneList := func(list []ast.Stmt) {
		for k, st := range list {
			terminal := false
			switch s := st.(type) {
			case *ast.IfStmt:
				terminal = pruneIf(s)
			case *ast.SwitchStmt:
				terminal = a.pruneSwitch(s, file, bindings, dead)
			}
			if terminal {
				for _, rest := range list[k+1:] {
					dead[rest] = true
				}
				return
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if dead[n] {
			return false
		}
		switch x := n.(type) {
		case *ast.BlockStmt:
			pruneList(x.List)
		case *ast.CaseClause:
			pruneList(x.Body)
		case *ast.CommClause:
			pruneList(x.Body)
		}
		return true
	})
	return dead
}

func (a *analyzer) pruneSwitch(s *ast.SwitchStmt, file *fileInfo, bindings map[string]string, dead map[ast.Node]bool) bool {
	if s.Tag == nil {
		return false
	}
	tag, bound, ok := a.condValue(s.Tag, file, bindings)
	if !ok || !bound {
		return false
	}
	var taken, def *ast.CaseClause
	for _, st := range s.Body.List {
		cc := st.(*ast.CaseClause)
		if cc.List == nil {
			def = cc
			continue
		}
		for _, e := range cc.List {
			v, _, ok := a.condValue(e, file, bindings)
			if !ok {
				return false
			}
			if v == tag && taken == nil {
				taken = cc
			}
		}
	}
	if taken == nil {
		taken = def
	}
	for _, st := range s.Body.List {
		if st != taken {
			dead[st] = true
		}
	}
	return taken != nil && terminates(taken.Body)
}

// evalCond evalua una condicion solo si compara un parametro ligado con una constante.
func (a *analyzer) evalCond(e ast.Expr, file *fileInfo, bindings map[string]string) (bool, bool) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return a.evalCond(x.X, file, bindings)
	case *ast.UnaryExpr:
		if x.Op == token.NOT {
			v, ok := a.evalCond(x.X, file, bindings)
			return !v, ok
		}
	case *ast.BinaryExpr:
		switch x.Op {
		case token.EQL, token.NEQ:
			l, lb, lok := a.condValue(x.X, file, bindings)
			r, rb, rok := a.condValue(x.Y, file, bindings)
			if !lok || !rok || (!lb && !rb) {
				return false, false
			}
			return (l == r) == (x.Op == token.EQL), true
		case token.LAND, token.LOR:
			l, lok := a.evalCond(x.X, file, bindings)
			r, rok := a.evalCond(x.Y, file, bindings)
			short := x.Op == token.LOR // valor que decide sin mirar el otro lado
			switch {
			case lok && l == short, rok && r == short:
				return short, true
			case lok && rok:
				return !short, true
			}
		}
	}
	return false, false
}

func (a *analyzer) condValue(e ast.Expr, file *fileInfo, bindings map[string]string) (value string, bound, ok bool) {
	if id, isIdent := e.(*ast.Ident); isIdent {
		if v, found := bindings[id.Name]; found {
			return v, true, true
		}
	}
	v, err := a.evalString(e, chain{{file: file}}, 0, 0)
	return v, false, err == nil
}

// terminates: el bloque no sigue hacia la siguiente sentencia.
func terminates(list []ast.Stmt) bool {
	if len(list) == 0 {
		return false
	}
	switch s := list[len(list)-1].(type) {
	case *ast.ReturnStmt, *ast.BranchStmt:
		return true
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
				return true
			}
		}
	}
	return false
}
