package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

func TestUnsubscribeRevokes(t *testing.T) {
	baja := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	ip := "203.0.113.7"
	grant := func(method ConsentMethod, ip *string, at time.Time) Consent {
		return Consent{Purpose: PurposeMarketing, Status: ConsentGranted, Method: method, IP: ip, OccurredAt: at}
	}
	after, before := baja.Add(time.Microsecond), baja.Add(-time.Microsecond)
	cases := []struct {
		name     string
		at       time.Time
		consents []Consent
		want     bool
	}{
		{"sin historial", baja, nil, true},
		{"doble opt-in posterior: la baja es anterior y no revoca", baja, []Consent{grant(MethodDoubleOptIn, nil, after)}, false},
		{"formulario con ip posterior", baja, []Consent{grant(MethodForm, &ip, after)}, false},
		{"doble opt-in anterior: la baja es nueva", baja, []Consent{grant(MethodDoubleOptIn, nil, before)}, true},
		{"empate: revoca", baja, []Consent{grant(MethodDoubleOptIn, nil, baja)}, true},
		{"api posterior no levanta una baja", baja, []Consent{grant(MethodAPI, nil, after)}, true},
		{"importacion posterior no levanta una baja", baja, []Consent{grant(MethodImport, nil, after)}, true},
		{"formulario sin ip posterior no levanta una baja", baja, []Consent{grant(MethodForm, nil, after)}, true},
		{"pendiente o revocado posterior no cuentan", baja, []Consent{
			{Purpose: PurposeMarketing, Status: ConsentPending, Method: MethodDoubleOptIn, OccurredAt: after},
			{Purpose: PurposeMarketing, Status: ConsentRevoked, Method: MethodForm, IP: &ip, OccurredAt: after},
		}, true},
		{"cuenta cualquier reconsentimiento posterior, no solo el ultimo", baja, []Consent{
			grant(MethodDoubleOptIn, nil, after), grant(MethodAPI, nil, after.Add(time.Hour)),
		}, false},
		{"baja sin hora (suppression anterior): revoca como antes", time.Time{}, []Consent{grant(MethodDoubleOptIn, nil, after)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnsubscribeRevokes(tc.at, tc.consents); got != tc.want {
				t.Fatalf("UnsubscribeRevokes = %v, se esperaba %v", got, tc.want)
			}
		})
	}
}

func TestCausasConHora(t *testing.T) {
	at := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	active := []ActiveCause{{Cause: CauseHardBounce, RegisteredAt: at.Add(time.Hour)}, {Cause: CauseUnsubscribe, RegisteredAt: at}}
	if got := CausesOf(active); len(got) != 2 || got[0] != CauseHardBounce || got[1] != CauseUnsubscribe {
		t.Fatalf("CausesOf: %v", got)
	}
	if got, ok := CauseRegisteredAt(active, CauseUnsubscribe); !ok || !got.Equal(at) {
		t.Fatalf("hora de la baja: %v %v", got, ok)
	}
	if _, ok := CauseRegisteredAt(active, CauseComplaint); ok {
		t.Fatal("una causa que no esta vigente no tiene hora")
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
