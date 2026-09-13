package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// repo es el arbol analizado: raiz, modulo y paquetes ya leidos (se leen una sola vez,
// tambien los de pkg/ que un servicio importa para resolver una constante o un tipo).
type repo struct {
	root     string
	module   string
	fset     *token.FileSet
	pkgs     map[string]*pkgInfo
	pkgNames map[string]string
}

type pkgInfo struct {
	dir    string
	name   string
	files  []*fileInfo
	values map[string]valueDef // const y var de paquete
	types  map[string]typeDef
	funcs  []*funcInfo
	byDecl map[*ast.FuncDecl]*funcInfo
}

type valueDef struct {
	expr ast.Expr // nil si la declaracion no tiene valor propio
	typ  ast.Expr
	file *fileInfo
}

type typeDef struct {
	expr ast.Expr
	file *fileInfo
}

type fileInfo struct {
	path    string
	ast     *ast.File
	pkg     *pkgInfo
	imports map[string]string // nombre local -> ruta de importacion
}

type funcInfo struct {
	decl       *ast.FuncDecl
	file       *fileInfo
	params     []string
	paramTypes []ast.Expr
	variadic   bool
}

func (f *funcInfo) name() string   { return f.decl.Name.Name }
func (f *funcInfo) isMethod() bool { return f.decl.Recv != nil }

func (f *funcInfo) paramIndex(name string) int {
	if name == "_" || name == "" {
		return -1
	}
	for i, p := range f.params {
		if p == name {
			return i
		}
	}
	return -1
}

// accepts dice si una llamada con n argumentos puede ser a esta funcion.
func (f *funcInfo) accepts(n int) bool {
	if f.variadic {
		return n >= len(f.params)-1
	}
	return n == len(f.params)
}

// recvType es el nombre del tipo receptor de un metodo; "" si no es metodo.
func (f *funcInfo) recvType() string {
	if f.decl.Recv == nil || len(f.decl.Recv.List) == 0 {
		return ""
	}
	t := f.decl.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		t = x.X
	case *ast.IndexListExpr:
		t = x.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// selfType: si sel.X es el receptor de fn, el tipo de ese receptor. Sin tipos es la unica
// forma de distinguir dos metodos homonimos de un mismo paquete.
func selfType(sel *ast.SelectorExpr, fn *funcInfo) string {
	if fn == nil || fn.decl.Recv == nil || len(fn.decl.Recv.List) == 0 || len(fn.decl.Recv.List[0].Names) == 0 {
		return ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != fn.decl.Recv.List[0].Names[0].Name {
		return ""
	}
	return fn.recvType()
}

// service son los paquetes de services/<nombre>, sus funciones y todas sus llamadas: el
// indice con el que se sigue un subject o un payload que llega por parametro.
type service struct {
	name  string
	pkgs  []*pkgInfo
	funcs []*funcInfo
	calls []callSite
}

type callSite struct {
	call *ast.CallExpr
	fn   *funcInfo // nil si la llamada esta fuera de una funcion
	file *fileInfo
}

func newRepo(root string) (*repo, error) {
	module, err := readModule(root)
	if err != nil {
		return nil, err
	}
	return &repo{root: root, module: module, fset: token.NewFileSet(),
		pkgs: map[string]*pkgInfo{}, pkgNames: map[string]string{}}, nil
}

func readModule(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	return "", fmt.Errorf("go.mod sin directiva module")
}

// rel da la posicion como ruta relativa a la raiz y linea, que es lo que se reporta.
func (r *repo) rel(pos token.Pos) string {
	p := r.fset.Position(pos)
	name := p.Filename
	if rel, err := filepath.Rel(r.root, name); err == nil {
		name = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("%s:%d", name, p.Line)
}

// dirOf traduce una ruta de importacion del modulo a su directorio; "" si es externa.
func (r *repo) dirOf(importPath string) string {
	if importPath == r.module {
		return r.root
	}
	rest, ok := strings.CutPrefix(importPath, r.module+"/")
	if !ok {
		return ""
	}
	return filepath.Join(r.root, filepath.FromSlash(rest))
}

func goSource(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

func (r *repo) loadPkg(dir string) *pkgInfo {
	if p, ok := r.pkgs[dir]; ok {
		return p
	}
	p := &pkgInfo{dir: dir, values: map[string]valueDef{}, types: map[string]typeDef{},
		byDecl: map[*ast.FuncDecl]*funcInfo{}}
	r.pkgs[dir] = p
	entries, err := os.ReadDir(dir)
	if err != nil {
		return p
	}
	for _, e := range entries {
		if e.IsDir() || !goSource(e.Name()) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		file, err := parser.ParseFile(r.fset, full, nil, parser.SkipObjectResolution)
		if err != nil {
			continue // un archivo que no parsea no es asunto de este check
		}
		fi := &fileInfo{path: full, ast: file, pkg: p, imports: r.imports(file)}
		p.files = append(p.files, fi)
		if p.name == "" {
			p.name = file.Name.Name
		}
		p.index(fi)
	}
	return p
}

func (p *pkgInfo) index(fi *fileInfo) {
	for _, decl := range fi.ast.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			f := newFuncInfo(d, fi)
			p.funcs = append(p.funcs, f)
			p.byDecl[d] = f
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for k, n := range s.Names {
						def := valueDef{typ: s.Type, file: fi}
						if len(s.Values) == len(s.Names) {
							def.expr = s.Values[k]
						}
						p.values[n.Name] = def
					}
				case *ast.TypeSpec:
					p.types[s.Name.Name] = typeDef{expr: s.Type, file: fi}
				}
			}
		}
	}
}

func newFuncInfo(d *ast.FuncDecl, fi *fileInfo) *funcInfo {
	f := &funcInfo{decl: d, file: fi}
	for _, field := range d.Type.Params.List {
		if _, ok := field.Type.(*ast.Ellipsis); ok {
			f.variadic = true
		}
		if len(field.Names) == 0 {
			f.params = append(f.params, "")
			f.paramTypes = append(f.paramTypes, field.Type)
			continue
		}
		for _, n := range field.Names {
			f.params = append(f.params, n.Name)
			f.paramTypes = append(f.paramTypes, field.Type)
		}
	}
	return f
}

func (r *repo) imports(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range file.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		} else {
			name = r.defaultName(p)
		}
		if name == "" || name == "_" || name == "." {
			continue
		}
		out[name] = p
	}
	return out
}

var versionElem = regexp.MustCompile(`^v[0-9]+$`)

// defaultName es el nombre con el que se usa un import sin alias: el de su clausula
// package si es del modulo, y una aproximacion por la ruta si es externo (de un paquete
// externo solo importa no confundir su nombre con una variable).
func (r *repo) defaultName(importPath string) string {
	if dir := r.dirOf(importPath); dir != "" {
		if n, ok := r.pkgNames[dir]; ok {
			return n
		}
		n := path.Base(importPath)
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !goSource(e.Name()) {
					continue
				}
				f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, parser.PackageClauseOnly)
				if err == nil {
					n = f.Name.Name
					break
				}
			}
		}
		r.pkgNames[dir] = n
		return n
	}
	elems := strings.Split(importPath, "/")
	last := elems[len(elems)-1]
	if versionElem.MatchString(last) && len(elems) > 1 {
		last = elems[len(elems)-2]
	}
	last = strings.TrimSuffix(strings.TrimPrefix(last, "go-"), ".go")
	return strings.ReplaceAll(last, "-", "_")
}

// importedPkg devuelve el paquete del modulo al que apunta el nombre local, o nil.
func (r *repo) importedPkg(file *fileInfo, local string) *pkgInfo {
	p, ok := file.imports[local]
	if !ok {
		return nil
	}
	dir := r.dirOf(p)
	if dir == "" {
		return nil
	}
	return r.loadPkg(dir)
}

func (r *repo) isPkgName(file *fileInfo, e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = file.imports[id.Name]
	return ok
}

func (r *repo) loadService(name string) (*service, error) {
	svc := &service{name: name}
	base := filepath.Join(r.root, "services", name)
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != base && (d.Name() == "testdata" || d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		pkg := r.loadPkg(p)
		if len(pkg.files) == 0 {
			return nil
		}
		svc.pkgs = append(svc.pkgs, pkg)
		svc.funcs = append(svc.funcs, pkg.funcs...)
		for _, fi := range pkg.files {
			svc.collectCalls(fi)
		}
		return nil
	})
	return svc, err
}

func (s *service) collectCalls(fi *fileInfo) {
	for _, decl := range fi.ast.Decls {
		var fn *funcInfo
		if d, ok := decl.(*ast.FuncDecl); ok {
			fn = fi.pkg.byDecl[d]
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				s.calls = append(s.calls, callSite{call: call, fn: fn, file: fi})
			}
			return true
		})
	}
}
