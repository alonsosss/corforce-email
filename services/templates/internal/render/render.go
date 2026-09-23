// Package render compila y ejecuta el contenido de una version de plantilla.
//
// El HTML se procesa con html/template (escapado contextual: una variable dentro de un
// href se escapa como URL y en texto como HTML) y el asunto y el texto con text/template.
// Solo se admite un subconjunto del lenguaje: variables planas {{.nombre}}, {{if}} y las
// funciones de funcs.go. Todo lo demas (range, with, define, variables locales,
// cualquier otra funcion) se rechaza al compilar con un error que dice que y donde.
package render

import (
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"regexp"
	"sort"
	"strings"
	texttemplate "text/template"
	"text/template/parse"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
)

// Engine compila y renderiza. No tiene estado: es seguro compartirlo entre peticiones.
type Engine struct{}

func New() *Engine { return &Engine{} }

// Compiled es una version lista para ejecutar. Las plantillas de html/template ya han
// pasado por el escapador (se ejecutan una vez al compilar), asi que Render puede
// invocarse en paralelo.
type Compiled struct {
	subject   *texttemplate.Template
	html      *htmltemplate.Template
	text      *texttemplate.Template
	variables []domain.Variable
	refs      []string
	// conditionals son los comentarios condicionales de Outlook apartados del HTML (conditional.go).
	conditionals *conditionals
}

// Variables devuelve las variables declaradas de la version compilada.
func (c *Compiled) Variables() []domain.Variable { return c.variables }

// References devuelve, ordenados, los nombres de variable que las plantillas usan.
func (c *Compiled) References() []string { return c.refs }

// forbiddenHTML son las construcciones que nunca deben salir en un correo: ejecutan
// codigo o cargan contenido activo en clientes que lo admitan. Se comprueban sobre la
// fuente, antes de parsear, para que el error senale la construccion exacta.
var forbiddenHTML = []struct {
	re  *regexp.Regexp
	msg string
}{
	{regexp.MustCompile(`(?i)<\s*script\b`), "no se admite <script>"},
	{regexp.MustCompile(`(?i)<\s*iframe\b`), "no se admite <iframe>"},
	{regexp.MustCompile(`(?i)<\s*object\b`), "no se admite <object>"},
	{regexp.MustCompile(`(?i)<\s*embed\b`), "no se admite <embed>"},
	{regexp.MustCompile(`(?i)<\s*form\b`), "no se admite <form>"},
	{regexp.MustCompile(`(?i)\bon[a-z]+\s*=`), "no se admiten atributos de evento (on*=)"},
	{regexp.MustCompile(`(?i)=\s*["']?\s*javascript\s*:`), "no se admite javascript: en atributos"},
}

// Compile valida y compila el contenido de una version. Cualquier defecto se devuelve
// envuelto en domain.ErrInvalidTemplate o domain.ErrInvalidVariableDeclaration.
func (e *Engine) Compile(c domain.Content) (*Compiled, error) {
	return e.compile(c, false)
}

// CompileDraft es Compile para verificar un contenido que todavia no se guarda (el panel en
// vivo del editor): una variable usada y no declarada se declara como cadena opcional en vez
// de rechazar la plantilla. Guardar la version sigue exigiendo declararla.
func (e *Engine) CompileDraft(c domain.Content) (*Compiled, error) {
	return e.compile(c, true)
}

func (e *Engine) compile(c domain.Content, declareMissing bool) (*Compiled, error) {
	if err := checkSizes(c); err != nil {
		return nil, err
	}
	for _, f := range forbiddenHTML {
		if loc := f.re.FindStringIndex(c.HTML); loc != nil {
			return nil, fmt.Errorf("%w: html: %s (posicion %d)", domain.ErrInvalidTemplate, f.msg, loc[0])
		}
	}
	if err := domain.ValidateDeclarations(c.Variables); err != nil {
		return nil, err
	}

	refs := map[string]struct{}{}
	subject, err := compileText("subject", c.Subject, refs)
	if err != nil {
		return nil, err
	}
	htmlSrc, conds, err := extractConditionals(c.HTML)
	if err != nil {
		return nil, err
	}
	html, err := compileHTML(htmlSrc, refs)
	if err != nil {
		return nil, err
	}
	var text *texttemplate.Template
	if c.Text != nil {
		if text, err = compileText("text", *c.Text, refs); err != nil {
			return nil, err
		}
	}

	declared := make(map[string]struct{}, len(c.Variables))
	for _, v := range c.Variables {
		declared[v.Name] = struct{}{}
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	var missing []domain.Variable
	for _, name := range names {
		if _, ok := declared[name]; ok || domain.IsReserved(name) {
			continue
		}
		if !declareMissing {
			return nil, fmt.Errorf("%w: la variable %q se usa pero no esta declarada", domain.ErrInvalidTemplate, name)
		}
		missing = append(missing, domain.Variable{Name: name, Type: domain.VarString})
	}
	variables := c.Variables
	if len(missing) > 0 {
		variables = append(append([]domain.Variable(nil), c.Variables...), missing...)
		if err := domain.ValidateDeclarations(variables); err != nil {
			return nil, err
		}
	}

	compiled := &Compiled{subject: subject, html: html, text: text, variables: variables, refs: names, conditionals: conds}
	// Ejecucion en seco con valores vacios: fuerza el escapador de html/template, que es
	// quien detecta contextos ambiguos o mal cerrados, y deja la plantilla lista para
	// ejecutarse en paralelo.
	if err := compiled.dryRun(); err != nil {
		return nil, err
	}
	return compiled, nil
}

func checkSizes(c domain.Content) error {
	switch {
	case strings.TrimSpace(c.Subject) == "":
		return fmt.Errorf("%w: subject es obligatorio", domain.ErrInvalidTemplate)
	case len(c.Subject) > domain.MaxSubjectBytes:
		return fmt.Errorf("%w: subject supera %d bytes", domain.ErrInvalidTemplate, domain.MaxSubjectBytes)
	case strings.ContainsAny(c.Subject, "\r\n"):
		return fmt.Errorf("%w: subject no admite saltos de linea", domain.ErrInvalidTemplate)
	case strings.TrimSpace(c.HTML) == "":
		return fmt.Errorf("%w: html es obligatorio", domain.ErrInvalidTemplate)
	case len(c.HTML) > domain.MaxHTMLBytes:
		return fmt.Errorf("%w: html supera %d bytes", domain.ErrInvalidTemplate, domain.MaxHTMLBytes)
	case c.Text != nil && len(*c.Text) > domain.MaxHTMLBytes:
		return fmt.Errorf("%w: text supera %d bytes", domain.ErrInvalidTemplate, domain.MaxHTMLBytes)
	}
	return nil
}

func compileText(name, src string, refs map[string]struct{}) (*texttemplate.Template, error) {
	t, err := texttemplate.New(name).Funcs(allowedFuncs).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", domain.ErrInvalidTemplate, cleanParseError(err))
	}
	if len(t.Templates()) != 1 {
		return nil, fmt.Errorf("%w: %s: no se admiten define, block ni template", domain.ErrInvalidTemplate, name)
	}
	if err := (&walker{name: name, refs: refs}).walk(t.Tree.Root); err != nil {
		return nil, err
	}
	return t, nil
}

func compileHTML(src string, refs map[string]struct{}) (*htmltemplate.Template, error) {
	t, err := htmltemplate.New("html").Funcs(allowedFuncs).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", domain.ErrInvalidTemplate, cleanParseError(err))
	}
	if len(t.Templates()) != 1 {
		return nil, fmt.Errorf("%w: html: no se admiten define, block ni template", domain.ErrInvalidTemplate)
	}
	// El arbol se recorre ANTES de la primera ejecucion: el escapador inserta despues sus
	// propias funciones en las pipelines y las confundiria con funciones del usuario.
	if err := (&walker{name: "html", refs: refs}).walk(t.Tree.Root); err != nil {
		return nil, err
	}
	return t, nil
}

// cleanParseError quita el prefijo "template: " que text/template antepone.
func cleanParseError(err error) string {
	return strings.TrimPrefix(err.Error(), "template: ")
}

func (c *Compiled) dryRun() error {
	values := make(map[string]any, len(c.variables)+4)
	for _, v := range c.variables {
		values[v.Name] = domain.ZeroValue(v.Type)
	}
	for _, r := range domain.ReservedVariables() {
		values[r.Name] = ""
	}
	if err := c.html.Execute(io.Discard, values); err != nil {
		var escapeErr *htmltemplate.Error
		if errors.As(err, &escapeErr) {
			return fmt.Errorf("%w: html: %s", domain.ErrInvalidTemplate, cleanParseError(err))
		}
		// Otro error viene de una funcion con el valor vacio de prueba (p. ej. date
		// sobre una cadena no RFC 3339); depende de los valores reales, no de la
		// plantilla.
	}
	return nil
}

// Render ejecuta la version con los valores ya validados (domain.ResolveValues). Si la
// version no tiene parte de texto, se genera desde el HTML renderizado.
func (c *Compiled) Render(values map[string]any) (domain.Rendered, error) {
	var out domain.Rendered
	subject, err := execute(c.subject, values)
	if err != nil {
		return out, err
	}
	// Un asunto nunca lleva saltos de linea: un valor con CRLF podria inyectar cabeceras.
	out.Subject = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(subject))

	if out.HTML, err = execute(c.html, values); err != nil {
		return out, err
	}
	if out.HTML, err = c.conditionals.restore(out.HTML); err != nil {
		return out, err
	}
	if c.text != nil {
		if out.Text, err = execute(c.text, values); err != nil {
			return out, err
		}
	} else {
		out.Text = HTMLToText(out.HTML)
	}
	return out, nil
}

type executor interface {
	Execute(w io.Writer, data any) error
}

func execute(t executor, values map[string]any) (string, error) {
	w := &limitedBuffer{limit: domain.MaxOutputBytes}
	if err := t.Execute(w, values); err != nil {
		if errors.Is(err, domain.ErrOutputTooLarge) {
			return "", domain.ErrOutputTooLarge
		}
		return "", fmt.Errorf("%w: %s", domain.ErrInvalidVariables, cleanParseError(err))
	}
	return w.String(), nil
}

// limitedBuffer corta la ejecucion en cuanto la salida supera el limite, en vez de
// acumular y comprobar al final.
type limitedBuffer struct {
	strings.Builder
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, domain.ErrOutputTooLarge
	}
	return b.Builder.Write(p)
}

// walker recorre el arbol parseado y acepta solo el subconjunto admitido, anotando las
// variables referenciadas.
type walker struct {
	name string
	refs map[string]struct{}
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
		return w.pipe(n.Pipe)
	case *parse.IfNode:
		if err := w.pipe(n.Pipe); err != nil {
			return err
		}
		if err := w.walk(n.List); err != nil {
			return err
		}
		return w.walk(n.ElseList)
	case *parse.RangeNode:
		return w.reject(n, "range no se admite en esta fase; las variables son escalares")
	case *parse.WithNode:
		return w.reject(n, "with no se admite; use {{if .variable}}")
	case *parse.TemplateNode:
		return w.reject(n, "no se admiten plantillas anidadas")
	case *parse.BreakNode, *parse.ContinueNode:
		return w.reject(n, "break y continue no se admiten")
	default:
		return w.reject(n, "construccion no admitida")
	}
}

func (w *walker) pipe(p *parse.PipeNode) error {
	if p == nil {
		return nil
	}
	if len(p.Decl) > 0 {
		return w.reject(p, "no se admiten variables locales ($x)")
	}
	for _, cmd := range p.Cmds {
		for _, arg := range cmd.Args {
			if err := w.arg(arg); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *walker) arg(n parse.Node) error {
	switch n := n.(type) {
	case *parse.FieldNode:
		if len(n.Ident) != 1 {
			return w.reject(n, "solo se admiten variables planas {{.nombre}}")
		}
		w.refs[n.Ident[0]] = struct{}{}
		return nil
	case *parse.IdentifierNode:
		if !isAllowedFunc(n.Ident) {
			return w.reject(n, fmt.Sprintf("funcion no permitida %q; se admiten upper, lower, title, default y date", n.Ident))
		}
		return nil
	case *parse.StringNode, *parse.NumberNode, *parse.BoolNode, *parse.NilNode:
		return nil
	case *parse.PipeNode:
		return w.pipe(n)
	case *parse.DotNode:
		return w.reject(n, "{{.}} no se admite; nombre la variable")
	case *parse.VariableNode:
		return w.reject(n, "no se admiten variables locales ($x)")
	case *parse.ChainNode:
		return w.reject(n, "no se admiten accesos encadenados")
	default:
		return w.reject(n, "expresion no admitida")
	}
}
