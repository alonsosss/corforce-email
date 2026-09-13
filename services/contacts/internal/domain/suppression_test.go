package domain

import (
	"errors"
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
		{"causa desconocida no deja volver a active", StatusBounced, []SuppressionCause{"futura"}, false, StatusBounced},
		{"causa desconocida no cambia a un active", StatusActive, []SuppressionCause{"futura"}, false, StatusActive},
		{"causa desconocida retiene a un excluido", StatusExcluded, []SuppressionCause{"futura"}, false, StatusExcluded},

		// manual e invalid: el estado de la causa vigente mas grave, sin baja en medio.
		{"manual excluye a un active", StatusActive, []SuppressionCause{CauseManual}, false, StatusExcluded},
		{"invalid a un active", StatusActive, []SuppressionCause{CauseInvalid}, false, StatusInvalid},
		{"invalid pesa mas que manual", StatusActive, []SuppressionCause{CauseManual, CauseInvalid}, false, StatusInvalid},
		{"causa desconocida junto a manual", StatusActive, []SuppressionCause{"futura", CauseManual}, false, StatusExcluded},
		{"rebote retirado con manual vigente", StatusBounced, []SuppressionCause{CauseManual}, false, StatusExcluded},
		{"queja retirada con invalid vigente", StatusComplained, []SuppressionCause{CauseInvalid}, false, StatusInvalid},
		{"rebote pesa mas que manual", StatusExcluded, []SuppressionCause{CauseManual, CauseHardBounce}, false, StatusBounced},
		{"queja pesa mas que invalid", StatusInvalid, []SuppressionCause{CauseInvalid, CauseComplaint}, false, StatusComplained},
		{"invalid retirada con manual vigente", StatusInvalid, []SuppressionCause{CauseManual}, false, StatusExcluded},
		{"manual caducada con invalid vigente", StatusExcluded, []SuppressionCause{CauseInvalid}, false, StatusInvalid},

		// Levantarlas devuelve a active; el consentimiento no se toca (lo comprueba el bucle).
		{"manual retirada o caducada: vuelve a active", StatusExcluded, nil, false, StatusActive},
		{"invalid retirada: vuelve a active", StatusInvalid, nil, false, StatusActive},

		// Con una baja: la baja en vigor pesa mas y solo la levanta un reconsentimiento.
		{"baja en vigor pesa mas que manual", StatusUnsubscribed, []SuppressionCause{CauseManual, CauseUnsubscribe}, false, StatusUnsubscribed},
		{"baja en vigor pesa mas que invalid", StatusUnsubscribed, []SuppressionCause{CauseUnsubscribe, CauseInvalid}, false, StatusUnsubscribed},
		{"unsubscribed sin la baja en suppression no pasa a excluded", StatusUnsubscribed, []SuppressionCause{CauseManual}, false, StatusUnsubscribed},
		{"unsubscribed sin la baja en suppression no pasa a invalid", StatusUnsubscribed, []SuppressionCause{CauseInvalid}, false, StatusUnsubscribed},
		{"queja retirada con baja e invalid", StatusComplained, []SuppressionCause{CauseUnsubscribe, CauseInvalid}, false, StatusUnsubscribed},
		{"baja registrada sobre un excluido", StatusExcluded, []SuppressionCause{CauseUnsubscribe, CauseManual}, true, StatusUnsubscribed},
		{"baja registrada sobre un invalid", StatusInvalid, []SuppressionCause{CauseInvalid, CauseUnsubscribe}, true, StatusUnsubscribed},

		// Quien reconsintio y aun tiene la baja vigente (suppression no la ha retirado).
		{"reconsintio y la empresa lo excluye", StatusActive, []SuppressionCause{CauseUnsubscribe, CauseManual}, false, StatusExcluded},
		{"reconsintio y la direccion es invalida", StatusActive, []SuppressionCause{CauseInvalid, CauseUnsubscribe}, false, StatusInvalid},
		{"excluido que reconsintio: baja atrasada sin efecto", StatusExcluded, []SuppressionCause{CauseUnsubscribe, CauseManual}, false, StatusExcluded},
		{"excluido que reconsintio: retirar la manual", StatusExcluded, []SuppressionCause{CauseUnsubscribe}, false, StatusActive},
		{"invalid que reconsintio: retirar la invalid", StatusInvalid, []SuppressionCause{CauseUnsubscribe}, false, StatusActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, consent := range ConsentStatuses() {
				c := &Contact{Status: tc.status, ConsentStatus: consent}
				changed := c.ReconcileSuppression(tc.causes, tc.registered)
				if c.Status != tc.want || changed != (tc.status != tc.want) {
					t.Fatalf("%s con %v: quedo %s (cambio=%v), se esperaba %s", tc.status, tc.causes, c.Status, changed, tc.want)
				}
				if c.ConsentStatus != consent {
					t.Fatalf("el estado no toca el consentimiento: %s paso a %s", consent, c.ConsentStatus)
				}
			}
		})
	}
}

// El orden de las causas no cambia el resultado: ReconcileSuppression recibe las causas
// vigentes en cualquier orden.
func TestReconcileSuppressionNoDependeDelOrden(t *testing.T) {
	all := []SuppressionCause{CauseComplaint, CauseHardBounce, CauseUnsubscribe, CauseInvalid, CauseManual}
	for _, st := range Statuses() {
		for mask := 1; mask < 1<<len(all); mask++ {
			var causes []SuppressionCause
			for i, c := range all {
				if mask&(1<<i) != 0 {
					causes = append(causes, c)
				}
			}
			reversed := make([]SuppressionCause, len(causes))
			for i, c := range causes {
				reversed[len(causes)-1-i] = c
			}
			for _, registered := range []bool{false, true} {
				a, b := &Contact{Status: st}, &Contact{Status: st}
				a.ReconcileSuppression(causes, registered)
				b.ReconcileSuppression(reversed, registered)
				if a.Status != b.Status {
					t.Fatalf("%s con %v (registrada=%v): %s frente a %s", st, causes, registered, a.Status, b.Status)
				}
			}
		}
	}
}

func TestEstadosDeExclusion(t *testing.T) {
	cases := []struct {
		status  Status
		lift    StatusLift
		blocks  bool
		reachOK bool
	}{
		{StatusActive, "", false, true},
		{StatusUnsubscribed, LiftReconsent, false, true},
		{StatusBounced, LiftOperator, true, false},
		{StatusComplained, LiftOperator, true, false},
		{StatusInvalid, LiftOperator, true, false},
		{StatusExcluded, LiftOperatorOrExpiry, true, false},
	}
	if len(cases) != len(Statuses()) {
		t.Fatalf("la tabla debe cubrir todos los estados: %v", Statuses())
	}
	for _, tc := range cases {
		if _, err := ParseStatus(string(tc.status)); err != nil {
			t.Errorf("%s no se admite como filtro: %v", tc.status, err)
		}
		if got := tc.status.LiftedBy(); got != tc.lift {
			t.Errorf("%s: LiftedBy = %q, se esperaba %q", tc.status, got, tc.lift)
		}
		if got := tc.status.BlocksAllMail(); got != tc.blocks {
			t.Errorf("%s: BlocksAllMail = %v", tc.status, got)
		}
		err := CheckConfirmationRequest(&Contact{Status: tc.status, ConsentStatus: ConsentNone})
		if (err == nil) != tc.reachOK || (err != nil && !errors.Is(err, ErrContactNotReachable)) {
			t.Errorf("%s: pedir el doble opt-in: %v", tc.status, err)
		}
		// Retirar la causa (productor sin reasons) solo levanta lo que levanta un operador.
		c := &Contact{Status: tc.status}
		if lifted := c.LiftSuppression(tc.status); lifted != tc.blocks || (lifted && c.Status != StatusActive) {
			t.Errorf("%s: LiftSuppression = %v, quedo %s", tc.status, lifted, c.Status)
		}
	}
	if _, err := ParseStatus("suppressed"); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("un estado desconocido no se admite: %v", err)
	}
}

// Productor sin reasons: la causa del evento se aplica sin degradar un estado mas grave.
func TestApplySuppressionNoDegrada(t *testing.T) {
	cases := []struct {
		from, target, want Status
	}{
		{StatusActive, StatusExcluded, StatusExcluded},
		{StatusExcluded, StatusInvalid, StatusInvalid},
		{StatusInvalid, StatusExcluded, StatusInvalid},
		{StatusUnsubscribed, StatusExcluded, StatusUnsubscribed},
		{StatusExcluded, StatusUnsubscribed, StatusUnsubscribed},
		{StatusBounced, StatusInvalid, StatusBounced},
	}
	for _, tc := range cases {
		c := &Contact{Status: tc.from}
		if changed := c.ApplySuppression(tc.target); c.Status != tc.want || changed != (tc.from != tc.want) {
			t.Errorf("%s + %s: quedo %s (cambio=%v), se esperaba %s", tc.from, tc.target, c.Status, changed, tc.want)
		}
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
// fuente para comprobar que contacts conoce todas sus causas, que cada una implica un
// estado distinto y que los ordena igual. active es el unico estado sin causa.
func TestStatusForCauseSigueElCatalogoDeSuppression(t *testing.T) {
	reasons := suppressionReasons(t)
	prev := math.MaxInt
	implied := map[Status]bool{}
	for _, r := range reasons {
		st, known := StatusForCause(SuppressionCause(r))
		if !known {
			t.Errorf("suppression publica la causa %q y contacts no la conoce", r)
			continue
		}
		if _, err := ParseStatus(string(st)); err != nil || st == StatusActive {
			t.Errorf("la causa %q implica %q, que no es un estado de exclusion", r, st)
		}
		if st.Severity() >= prev {
			t.Errorf("la causa %q (estado %s) no sigue el orden de gravedad de suppression %v", r, st, reasons)
		}
		prev = st.Severity()
		implied[st] = true
	}
	for _, st := range Statuses() {
		if st != StatusActive && !implied[st] {
			t.Errorf("el estado %s no lo implica ninguna causa de suppression", st)
		}
	}
	if st, known := StatusForCause("futura"); known || st != "" {
		t.Fatalf("una causa desconocida no implica estado: %q %v", st, known)
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
