package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"strconv"
	"strings"
)

const (
	// maxChainDepth acota cuantas funciones se suben siguiendo un parametro.
	maxChainDepth = 6
	maxEvalDepth  = 32
)

// errUnbound: el valor sale de un parametro de la funcion mas externa de la cadena, asi que
// hay que repetir la evaluacion desde cada llamada a esa funcion.
var errUnbound = errors.New("depende de un parametro de una funcion sin llamada fijada")

type unresolvedError struct {
	pos token.Pos
	why string
}

func (e *unresolvedError) Error() string { return e.why }

func unresolved(n ast.Node, why string) error { return &unresolvedError{pos: n.Pos(), why: why} }

// link es una funcion de la cadena de llamadas; call es la llamada (en la funcion del
// siguiente eslabon) que fija sus parametros, o nil en el eslabon mas externo.
type link struct {
	fn   *funcInfo
	file *fileInfo
	call *ast.CallExpr
}

// chain va de dentro hacia fuera: chain[0] es la funcion donde esta la publicacion.
type chain []link

func (c chain) has(fn *funcInfo) bool {
	if fn == nil {
		return false
	}
	for _, l := range c {
		if l.fn == fn {
			return true
		}
	}
	return false
}

type payload struct {
	fields map[string]bool
	opaque bool
}

func emptyPayload() payload  { return payload{fields: map[string]bool{}} }
func opaquePayload() payload { return payload{fields: map[string]bool{}, opaque: true} }

func (p *payload) add(f string) {
	if p.fields == nil {
		p.fields = map[string]bool{}
	}
	p.fields[f] = true
}

func (p *payload) merge(o payload) {
	p.opaque = p.opaque || o.opaque
	for f := range o.fields {
		p.add(f)
	}
}

type publication struct {
	Subject string
	Service string
	Fields  []string
	Opaque  bool
	Pos     token.Pos // llamada que fija el subject, en la funcion mas externa de la cadena
}

// sink es una llamada que publica: Publish/PublishPersistent(subject, evento) o
// outbox.Enqueue(ctx, q, subject, evento).
type sink struct {
	call    *ast.CallExpr
	subject ast.Expr
	event   ast.Expr
	fn      *funcInfo
	file    *fileInfo
}

type analyzer struct {
	r        *repo
	svc      *service
	pubs     []publication
	cons     []consumption
	problems []string
	notices  []string
}

func (a *analyzer) sinks() []sink {
	outboxPath := a.r.module + "/pkg/outbox"
	var out []sink
	for _, cs := range a.svc.calls {
		sel, ok := cs.call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		args := cs.call.Args
		switch sel.Sel.Name {
		case "Enqueue":
			id, ok := sel.X.(*ast.Ident)
			if !ok || cs.file.imports[id.Name] != outboxPath || len(args) != 4 {
				continue
			}
			out = append(out, sink{call: cs.call, subject: args[2], event: args[3], fn: cs.fn, file: cs.file})
		case "Publish", "PublishPersistent":
			if len(args) != 2 {
				continue
			}
			out = append(out, sink{call: cs.call, subject: args[0], event: args[1], fn: cs.fn, file: cs.file})
		}
	}
	return out
}

// collectPublishers registra cada publicacion del servicio. Un subject que no se puede
// resolver es un error: dejarlo fuera es publicar un evento sin dueno ni contrato.
func (a *analyzer) collectPublishers() {
	for _, s := range a.sinks() {
		a.expand(chain{{fn: s.fn, file: s.file}}, 0, func(ch chain) error {
			subject, err := a.evalString(s.subject, ch, 0, 0)
			if err != nil {
				return err
			}
			p, err := a.evalEnvelope(s.event, ch, 0, 0)
			if err != nil {
				return err
			}
			pos := s.call.Pos()
			if len(ch) > 1 {
				pos = ch[len(ch)-2].call.Pos()
			}
			a.pubs = append(a.pubs, publication{Subject: subject, Service: a.svc.name,
				Fields: sortedKeys(p.fields), Opaque: p.opaque, Pos: pos})
			return nil
		}, func(pos token.Pos, why string) {
			a.problems = append(a.problems, fmt.Sprintf(
				"%s: %s publica sin subject resoluble (%s); usa un literal o una constante",
				a.r.rel(pos), a.svc.name, why))
		})
	}
}

// expand prueba try sobre la cadena. Si lo evaluado depende de un parametro de la funcion
// mas externa, repite desde cada llamada del servicio a esa funcion (por nombre y aridad:
// sin tipos no se distingue el receptor). Lo que no se resuelve se entrega a fail.
func (a *analyzer) expand(ch chain, depth int, try func(chain) error, fail func(token.Pos, string)) {
	err := try(ch)
	if err == nil {
		return
	}
	var u *unresolvedError
	if errors.As(err, &u) {
		fail(u.pos, u.why)
		return
	}
	top := ch[len(ch)-1]
	if !errors.Is(err, errUnbound) || top.fn == nil {
		fail(top.file.ast.Pos(), err.Error())
		return
	}
	if depth >= maxChainDepth {
		fail(top.fn.decl.Pos(), "cadena de llamadas demasiado larga para seguir el parametro")
		return
	}
	followed := 0
	for _, cs := range a.callSites(top.fn) {
		if ch.has(cs.fn) {
			continue
		}
		followed++
		next := make(chain, 0, len(ch)+1)
		next = append(next, ch[:len(ch)-1]...)
		next = append(next, link{fn: top.fn, file: top.file, call: cs.call}, link{fn: cs.fn, file: cs.file})
		a.expand(next, depth+1, try, fail)
	}
	if followed == 0 {
		fail(top.fn.decl.Pos(), fmt.Sprintf("%s lo recibe por parametro y ninguna llamada del servicio lo fija", top.fn.name()))
	}
}

func (a *analyzer) callSites(target *funcInfo) []callSite {
	var out []callSite
	for _, cs := range a.svc.calls {
		if !target.accepts(len(cs.call.Args)) {
			continue
		}
		switch fun := cs.call.Fun.(type) {
		case *ast.Ident:
			if !target.isMethod() && fun.Name == target.name() && cs.file.pkg == target.file.pkg {
				out = append(out, cs)
			}
		case *ast.SelectorExpr:
			if fun.Sel.Name != target.name() {
				continue
			}
			if a.r.isPkgName(cs.file, fun.X) {
				if !target.isMethod() && a.r.importedPkg(cs.file, fun.X.(*ast.Ident).Name) == target.file.pkg {
					out = append(out, cs)
				}
				continue
			}
			if target.isMethod() {
				if t := selfType(fun, cs.fn); t != "" && (t != target.recvType() || cs.file.pkg != target.file.pkg) {
					continue
				}
				out = append(out, cs)
			}
		}
	}
	return out
}

func boundArg(ch chain, i, idx int) (ast.Expr, error) {
	l := ch[i]
	if l.call == nil {
		return nil, errUnbound
	}
	if (l.fn.variadic && idx == len(l.fn.params)-1) || idx >= len(l.call.Args) {
		return nil, unresolved(l.call, "argumento variadico")
	}
	return l.call.Args[idx], nil
}

// evalString resuelve un subject: literal, constante del paquete o de otro paquete del
// modulo, concatenacion o conversion de esas, variable local con un unico valor, o
// parametro (en el llamante).
func (a *analyzer) evalString(e ast.Expr, ch chain, i, depth int) (string, error) {
	if depth > maxEvalDepth {
		return "", unresolved(e, "definicion demasiado anidada")
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return a.evalString(x.X, ch, i, depth+1)
	case *ast.BasicLit:
		if s, ok := stringLit(x); ok {
			return s, nil
		}
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			l, err := a.evalString(x.X, ch, i, depth+1)
			if err != nil {
				return "", err
			}
			r, err := a.evalString(x.Y, ch, i, depth+1)
			if err != nil {
				return "", err
			}
			return l + r, nil
		}
	case *ast.Ident:
		return a.evalIdent(x, ch, i, depth)
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if pkg := a.r.importedPkg(ch[i].file, id.Name); pkg != nil {
				if def, ok := pkg.values[x.Sel.Name]; ok && def.expr != nil {
					return a.evalString(def.expr, chain{{file: def.file}}, 0, depth+1)
				}
			}
		}
	case *ast.CallExpr:
		if len(x.Args) == 1 && a.isStringConversion(x.Fun, ch[i].file) {
			return a.evalString(x.Args[0], ch, i, depth+1)
		}
	}
	return "", unresolved(e, exprString(e)+" no es un literal ni una constante")
}

func (a *analyzer) isStringConversion(fun ast.Expr, file *fileInfo) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		if f.Name == "string" {
			return true
		}
		_, ok := file.pkg.types[f.Name]
		return ok
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			if pkg := a.r.importedPkg(file, id.Name); pkg != nil {
				_, ok := pkg.types[f.Sel.Name]
				return ok
			}
		}
	}
	return false
}

func (a *analyzer) evalIdent(x *ast.Ident, ch chain, i, depth int) (string, error) {
	l := ch[i]
	if l.fn != nil {
		if idx := l.fn.paramIndex(x.Name); idx >= 0 {
			arg, err := boundArg(ch, i, idx)
			if err != nil {
				return "", err
			}
			return a.evalString(arg, ch, i+1, depth+1)
		}
		if li := localDecl(l.fn.decl.Body, x.Name); li.found {
			if li.opaque || len(li.values) == 0 {
				return "", unresolved(x, x.Name+" es una variable que se decide en tiempo de ejecucion")
			}
			var out string
			for k, v := range li.values {
				s, err := a.evalString(v, ch, i, depth+1)
				if err != nil {
					return "", err
				}
				if k > 0 && s != out {
					return "", unresolved(x, x.Name+" toma valores distintos")
				}
				out = s
			}
			return out, nil
		}
	}
	if def, ok := l.file.pkg.values[x.Name]; ok && def.expr != nil {
		return a.evalString(def.expr, chain{{file: def.file}}, 0, depth+1)
	}
	return "", unresolved(x, x.Name+" no es un literal ni una constante")
}

type localInfo struct {
	found  bool
	opaque bool // variable de rango, parametro de un closure o asignacion multiple
	values []ast.Expr
}

// localDecl reune las asignaciones a un nombre dentro de una funcion. No distingue
// ambitos: si el nombre se declara de una forma que no se puede seguir, se da por opaco.
func localDecl(body *ast.BlockStmt, name string) localInfo {
	var li localInfo
	if body == nil || name == "_" {
		return li
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for k, lhs := range s.Lhs {
				if id, ok := lhs.(*ast.Ident); !ok || id.Name != name {
					continue
				}
				li.found = true
				if (s.Tok == token.DEFINE || s.Tok == token.ASSIGN) && len(s.Lhs) == len(s.Rhs) {
					li.values = append(li.values, s.Rhs[k])
				} else {
					li.opaque = true
				}
			}
		case *ast.ValueSpec:
			for k, id := range s.Names {
				if id.Name != name {
					continue
				}
				li.found = true
				if len(s.Values) == len(s.Names) {
					li.values = append(li.values, s.Values[k])
				} else if len(s.Values) != 0 {
					li.opaque = true
				}
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{s.Key, s.Value} {
				if id, ok := e.(*ast.Ident); ok && id.Name == name {
					li.found, li.opaque = true, true
				}
			}
		case *ast.FuncLit:
			for _, f := range s.Type.Params.List {
				for _, id := range f.Names {
					if id.Name == name {
						li.found, li.opaque = true, true
					}
				}
			}
		}
		return true
	})
	return li
}

// sources dice de donde toma su valor un identificador: el argumento de la llamada si es
// un parametro (evaluado en el llamante) o sus asignaciones si es una variable local.
func sources(x *ast.Ident, ch chain, i int) (exprs []ast.Expr, at int, ok bool, err error) {
	l := ch[i]
	if l.fn == nil {
		return nil, 0, false, nil
	}
	if idx := l.fn.paramIndex(x.Name); idx >= 0 {
		arg, err := boundArg(ch, i, idx)
		if err != nil {
			return nil, 0, false, err
		}
		return []ast.Expr{arg}, i + 1, true, nil
	}
	li := localDecl(l.fn.decl.Body, x.Name)
	if !li.found || li.opaque {
		return nil, 0, false, nil
	}
	return li.values, i, true, nil
}

// evalEnvelope lee el payload del evento: el campo Data de un literal events.Event, o el
// mapa mismo en la forma directa Publish(subject, map{...}).
func (a *analyzer) evalEnvelope(e ast.Expr, ch chain, i, depth int) (payload, error) {
	if depth > maxEvalDepth {
		return opaquePayload(), nil
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return a.evalEnvelope(x.X, ch, i, depth+1)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return a.evalEnvelope(x.X, ch, i, depth+1)
		}
	case *ast.CompositeLit:
		if under, _ := a.underlying(x.Type, ch[i].file, 0); under != nil {
			if _, isMap := under.(*ast.MapType); isMap {
				return a.literalFields(x, ch, i, depth)
			}
		}
		for _, elt := range x.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Data" {
				return a.evalData(kv.Value, ch, i, depth+1)
			}
		}
		return opaquePayload(), nil
	case *ast.Ident:
		exprs, at, ok, err := sources(x, ch, i)
		if err != nil {
			return unboundOr(err)
		}
		if !ok {
			return opaquePayload(), nil
		}
		out := emptyPayload()
		for _, v := range exprs {
			p, err := a.evalEnvelope(v, ch, at, depth+1)
			if err != nil {
				return payload{}, err
			}
			out.merge(p)
		}
		assigned, found, err := a.dataAssigns(ch[i].fn.decl.Body, x.Name, ch, i, depth)
		if err != nil {
			return payload{}, err
		}
		if len(exprs) == 0 && !found {
			return opaquePayload(), nil
		}
		out.merge(assigned)
		return out, nil
	}
	return opaquePayload(), nil
}

// evalData lee las claves del payload: mapa literal, struct (etiquetas json), variable o
// parametro con las claves que se le anaden despues, o el resultado de una funcion del
// servicio que lo construye.
func (a *analyzer) evalData(e ast.Expr, ch chain, i, depth int) (payload, error) {
	if depth > maxEvalDepth {
		return opaquePayload(), nil
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return a.evalData(x.X, ch, i, depth+1)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return a.evalData(x.X, ch, i, depth+1)
		}
	case *ast.CompositeLit:
		return a.literalFields(x, ch, i, depth)
	case *ast.Ident:
		if x.Name == "nil" {
			return emptyPayload(), nil
		}
		exprs, at, ok, err := sources(x, ch, i)
		if err != nil {
			return unboundOr(err)
		}
		if !ok || len(exprs) == 0 {
			return opaquePayload(), nil
		}
		out := emptyPayload()
		for _, v := range exprs {
			p, err := a.evalData(v, ch, at, depth+1)
			if err != nil {
				return payload{}, err
			}
			out.merge(p)
		}
		added, err := a.indexAssignKeys(ch[i].fn.decl.Body, x.Name, ch, i, depth)
		if err != nil {
			return payload{}, err
		}
		out.merge(added)
		return out, nil
	case *ast.CallExpr:
		return a.evalCallResult(x, ch, i, depth)
	}
	return opaquePayload(), nil
}

func unboundOr(err error) (payload, error) {
	if errors.Is(err, errUnbound) {
		return payload{}, err
	}
	return opaquePayload(), nil
}

func (a *analyzer) evalCallResult(call *ast.CallExpr, ch chain, i, depth int) (payload, error) {
	if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "make" && len(call.Args) > 0 {
		if _, isMap := call.Args[0].(*ast.MapType); isMap {
			return emptyPayload(), nil
		}
	}
	callee := a.callee(call, ch[i].file, ch[i].fn)
	if callee == nil {
		return opaquePayload(), nil
	}
	results := returnResults(callee.decl.Body)
	if len(results) == 0 {
		return opaquePayload(), nil
	}
	sub := make(chain, 0, len(ch)-i+1)
	sub = append(sub, link{fn: callee, file: callee.file, call: call})
	sub = append(sub, ch[i:]...)
	out := emptyPayload()
	for _, r := range results {
		p, err := a.evalData(r, sub, 0, depth+1)
		if err != nil {
			return payload{}, err
		}
		out.merge(p)
	}
	return out, nil
}

// indexAssignKeys: claves que la funcion le anade al mapa con m["clave"] = v.
func (a *analyzer) indexAssignKeys(body *ast.BlockStmt, name string, ch chain, i, depth int) (payload, error) {
	out := emptyPayload()
	var failure error
	ast.Inspect(body, func(n ast.Node) bool {
		s, ok := n.(*ast.AssignStmt)
		if !ok || failure != nil {
			return failure == nil
		}
		for _, lhs := range s.Lhs {
			idx, ok := lhs.(*ast.IndexExpr)
			if !ok {
				continue
			}
			if id, ok := idx.X.(*ast.Ident); !ok || id.Name != name {
				continue
			}
			key, err := a.evalString(idx.Index, ch, i, depth+1)
			if errors.Is(err, errUnbound) {
				failure = err
				return false
			}
			if err != nil {
				out.opaque = true
				continue
			}
			out.add(key)
		}
		return true
	})
	return out, failure
}

// dataAssigns: payload que la funcion fija despues con evt.Data = x.
func (a *analyzer) dataAssigns(body *ast.BlockStmt, name string, ch chain, i, depth int) (payload, bool, error) {
	out := emptyPayload()
	found := false
	var failure error
	ast.Inspect(body, func(n ast.Node) bool {
		s, ok := n.(*ast.AssignStmt)
		if !ok || failure != nil || len(s.Lhs) != len(s.Rhs) {
			return failure == nil
		}
		for k, lhs := range s.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Data" {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != name {
				continue
			}
			found = true
			p, err := a.evalData(s.Rhs[k], ch, i, depth+1)
			if err != nil {
				failure = err
				return false
			}
			out.merge(p)
		}
		return true
	})
	return out, found, failure
}

func (a *analyzer) literalFields(lit *ast.CompositeLit, ch chain, i, depth int) (payload, error) {
	under, file := a.underlying(lit.Type, ch[i].file, 0)
	switch t := under.(type) {
	case *ast.MapType:
		out := emptyPayload()
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, err := a.evalString(kv.Key, ch, i, depth+1)
			if err != nil {
				return unboundOr(err)
			}
			out.add(key)
		}
		return out, nil
	case *ast.StructType:
		return a.jsonFields(t, file, depth+1), nil
	}
	return opaquePayload(), nil
}

// jsonFields: los nombres con los que encoding/json serializa el struct.
func (a *analyzer) jsonFields(st *ast.StructType, file *fileInfo, depth int) payload {
	if depth > maxEvalDepth {
		return opaquePayload()
	}
	out := emptyPayload()
	for _, f := range st.Fields.List {
		tag := ""
		if f.Tag != nil {
			if s, err := strconv.Unquote(f.Tag.Value); err == nil {
				tag = reflect.StructTag(s).Get("json")
			}
		}
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if len(f.Names) == 0 {
			if name != "" {
				out.add(name)
				continue
			}
			inner, innerFile := a.underlying(f.Type, file, 0)
			embedded, ok := inner.(*ast.StructType)
			if !ok {
				out.opaque = true
				continue
			}
			out.merge(a.jsonFields(embedded, innerFile, depth+1))
			continue
		}
		for _, n := range f.Names {
			if !n.IsExported() {
				continue
			}
			if name != "" {
				out.add(name)
			} else {
				out.add(n.Name)
			}
		}
	}
	return out
}

// underlying sigue un nombre de tipo hasta su definicion (del paquete o de otro paquete
// del modulo). nil si no se puede.
func (a *analyzer) underlying(t ast.Expr, file *fileInfo, depth int) (ast.Expr, *fileInfo) {
	if t == nil || depth > 8 {
		return nil, nil
	}
	switch x := t.(type) {
	case *ast.ParenExpr:
		return a.underlying(x.X, file, depth+1)
	case *ast.StarExpr:
		return a.underlying(x.X, file, depth+1)
	case *ast.Ident:
		if def, ok := file.pkg.types[x.Name]; ok {
			return a.underlying(def.expr, def.file, depth+1)
		}
		return nil, nil
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if pkg := a.r.importedPkg(file, id.Name); pkg != nil {
				if def, ok := pkg.types[x.Sel.Name]; ok {
					return a.underlying(def.expr, def.file, depth+1)
				}
			}
		}
		return nil, nil
	}
	return t, file
}

// callee resuelve la funcion llamada cuando es inequivoca: funcion del paquete, de otro
// paquete del modulo, o el unico metodo del servicio con ese nombre y aridad (del tipo
// del receptor de ctx si la llamada es sobre el).
func (a *analyzer) callee(call *ast.CallExpr, file *fileInfo, ctx *funcInfo) *funcInfo {
	var cands []*funcInfo
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		for _, f := range file.pkg.funcs {
			if !f.isMethod() && f.name() == fun.Name {
				cands = append(cands, f)
			}
		}
	case *ast.SelectorExpr:
		if a.r.isPkgName(file, fun.X) {
			pkg := a.r.importedPkg(file, fun.X.(*ast.Ident).Name)
			if pkg == nil {
				return nil
			}
			for _, f := range pkg.funcs {
				if !f.isMethod() && f.name() == fun.Sel.Name {
					cands = append(cands, f)
				}
			}
		} else {
			self := selfType(fun, ctx)
			for _, f := range a.svc.funcs {
				if !f.isMethod() || f.name() != fun.Sel.Name {
					continue
				}
				if self != "" && (f.recvType() != self || f.file.pkg != file.pkg) {
					continue
				}
				cands = append(cands, f)
			}
		}
	}
	var match *funcInfo
	for _, f := range cands {
		if !f.accepts(len(call.Args)) {
			continue
		}
		if match != nil {
			return nil
		}
		match = f
	}
	return match
}

// returnResults: el primer resultado de cada return de la funcion (no de sus closures).
func returnResults(body *ast.BlockStmt) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(s.Results) > 0 {
				out = append(out, s.Results[0])
			}
		}
		return true
	})
	return out
}
