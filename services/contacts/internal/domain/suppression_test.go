package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReconcileSuppression(t *testing.T) {
	cases := []struct {
		name       string
		status     Status
		causes     []SuppressionCause
		registered bool
		want       Status
	}{
		{"queja", StatusActive, []SuppressionCause{CauseComplaint}, false, StatusComplained},
		{"rebote", StatusActive, []SuppressionCause{CauseHardBounce}, false, StatusBounced},
		{"la mas grave sin importar el orden", StatusBounced, []SuppressionCause{CauseHardBounce, CauseComplaint}, false, StatusComplained},
		{"queja retirada con rebote vigente", StatusComplained, []SuppressionCause{CauseHardBounce}, false, StatusBounced},
		{"rebote pesa mas que una baja previa", StatusUnsubscribed, []SuppressionCause{CauseHardBounce, CauseUnsubscribe}, false, StatusBounced},
		{"rebote retirado con baja vigente", StatusBounced, []SuppressionCause{CauseUnsubscribe}, false, StatusUnsubscribed},
		{"queja retirada con baja y manual", StatusComplained, []SuppressionCause{CauseUnsubscribe, CauseManual}, false, StatusUnsubscribed},
		{"baja registrada", StatusActive, []SuppressionCause{CauseUnsubscribe}, true, StatusUnsubscribed},
		{"active que reconsintio con la baja aun vigente", StatusActive, []SuppressionCause{CauseUnsubscribe}, false, StatusActive},
		{"baja que sigue", StatusUnsubscribed, []SuppressionCause{CauseUnsubscribe}, false, StatusUnsubscribed},
		{"sin causas: rebote levantado", StatusBounced, nil, false, StatusActive},
		{"sin causas: queja levantada", StatusComplained, []SuppressionCause{}, false, StatusActive},
		{"sin causas: la baja no se levanta", StatusUnsubscribed, nil, false, StatusUnsubscribed},
		{"sin causas: active sigue", StatusActive, nil, false, StatusActive},
		{"manual no deja volver a active", StatusBounced, []SuppressionCause{CauseManual}, false, StatusBounced},
		{"invalid no deja volver a active", StatusComplained, []SuppressionCause{CauseInvalid}, false, StatusComplained},
		{"causa desconocida no deja volver a active", StatusBounced, []SuppressionCause{"futura"}, false, StatusBounced},
		{"manual no cambia a un active", StatusActive, []SuppressionCause{CauseManual}, false, StatusActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Contact{Status: tc.status}
			changed := c.ReconcileSuppression(tc.causes, tc.registered)
			if c.Status != tc.want || changed != (tc.status != tc.want) {
				t.Fatalf("%s con %v: quedo %s (cambio=%v), se esperaba %s", tc.status, tc.causes, c.Status, changed, tc.want)
			}
		})
	}
}

// El catalogo de causas es de suppression y su paquete es internal: el test lee su
// fuente para comprobar que contacts conoce todas sus causas y que las ordena igual.
func TestStatusForCauseSigueElCatalogoDeSuppression(t *testing.T) {
	reasons := suppressionReasons(t)
	prev := math.MaxInt
	for _, r := range reasons {
		st, known := StatusForCause(SuppressionCause(r))
		if !known {
			t.Errorf("suppression publica la causa %q y contacts no la conoce", r)
			continue
		}
		if st == "" {
			continue
		}
		if st.Severity() >= prev {
			t.Errorf("la causa %q (estado %s) no sigue el orden de gravedad de suppression %v", r, st, reasons)
		}
		prev = st.Severity()
	}
}

// suppressionReasons devuelve las causas de domain.Reasons() de suppression, de mas a
// menos grave, resolviendo las constantes que las nombran.
func suppressionReasons(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "suppression", "internal", "domain", "entities.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("no se pudo leer el catalogo de suppression: %v", err)
	}
	consts := map[string]string{}
	var names []string
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						consts[name.Name] = v
					}
				}
			}
		case *ast.FuncDecl:
			if d.Recv != nil || d.Name.Name != "Reasons" {
				continue
			}
			ast.Inspect(d.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, e := range lit.Elts {
					if id, ok := e.(*ast.Ident); ok {
						names = append(names, id.Name)
					}
				}
				return false
			})
		}
	}
	if len(names) == 0 {
		t.Fatal("no se encontro domain.Reasons() en suppression")
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		v, ok := consts[n]
		if !ok {
			t.Fatalf("la causa %s de suppression no es una constante de cadena legible", n)
		}
		out = append(out, v)
	}
	return out
}
