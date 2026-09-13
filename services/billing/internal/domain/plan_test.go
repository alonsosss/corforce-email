package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func fullLimits() []PlanLimit {
	out := make([]PlanLimit, 0, len(resources))
	for _, r := range Resources() {
		out = append(out, PlanLimit{Resource: r, Included: 10, HardLimit: true})
	}
	return out
}

func validPlan() *Plan {
	return &Plan{
		Code: "business", Name: "Business", Currency: "USD",
		BasePrice: decimal.RequireFromString("49.00"), BillingPeriod: PeriodMonthly,
		Status: PlanActive, Limits: fullLimits(),
	}
}

func TestPlanValidoPasa(t *testing.T) {
	if err := validPlan().Validate(); err != nil {
		t.Fatalf("plan valido rechazado: %v", err)
	}
}

func TestCodigoDePlan(t *testing.T) {
	cases := map[string]bool{
		"business":                    true,
		"plan_pro-2":                  true,
		"b2":                          true,
		"p" + strings.Repeat("a", 39): true,
		"b":                           false,
		"Business":                    false,
		"1plan":                       false,
		"plan pro":                    false,
		"p" + strings.Repeat("a", 40): false,
	}
	for code, ok := range cases {
		p := validPlan()
		p.Code = code
		err := p.Validate()
		if ok && err != nil {
			t.Errorf("%q deberia valer: %v", code, err)
		}
		if !ok && !errors.Is(err, ErrInvalidPlan) {
			t.Errorf("%q deberia rechazarse, hubo %v", code, err)
		}
	}
}

func TestMonedaISO4217(t *testing.T) {
	cases := map[string]bool{"USD": true, "PEN": true, "usd": false, "US": false, "USDX": false, "": false}
	for currency, ok := range cases {
		p := validPlan()
		p.Currency = currency
		if err := p.Validate(); (err == nil) != ok {
			t.Errorf("moneda %q: se esperaba valida=%v, hubo %v", currency, ok, err)
		}
	}
}

func TestImportesDecimalesSinFloat(t *testing.T) {
	ok := map[string]string{
		"49.00":            "49.00",
		"49":               "49.00",
		"0.10":             "0.10",
		"0":                "0.00",
		"9999999999999.99": "9999999999999.99",
	}
	for in, want := range ok {
		d, err := ParsePrice(in)
		if err != nil || d.StringFixed(PriceScale) != want {
			t.Errorf("ParsePrice(%q) = %s, %v; se esperaba %s", in, d.StringFixed(PriceScale), err, want)
		}
	}
	for _, in := range []string{"49.001", "-1.00", "1e3", "1,000.00", " 49", "", "abc", ".5", "5.", "10000000000000.00"} {
		if _, err := ParsePrice(in); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("ParsePrice(%q) deberia rechazarse, hubo %v", in, err)
		}
	}

	// La suma exacta que un float no da: 0.10 + 0.20 == 0.30.
	a, _ := ParsePrice("0.10")
	b, _ := ParsePrice("0.20")
	c, _ := ParsePrice("0.30")
	if !a.Add(b).Equal(c) {
		t.Fatalf("0.10 + 0.20 = %s", a.Add(b))
	}

	u, err := ParseUnitPrice("0.001500")
	if err != nil || u.StringFixed(UnitPriceScale) != "0.001500" {
		t.Fatalf("ParseUnitPrice: %s %v", u, err)
	}
	for _, in := range []string{"0.0000001", "1000000000", "-0.5"} {
		if _, err := ParseUnitPrice(in); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("ParseUnitPrice(%q) deberia rechazarse, hubo %v", in, err)
		}
	}
}

func TestPrecioBaseDelPlan(t *testing.T) {
	for _, bad := range []string{"-1", "49.001"} {
		p := validPlan()
		p.BasePrice = decimal.RequireFromString(bad)
		if err := p.Validate(); !errors.Is(err, ErrInvalidPlan) {
			t.Errorf("base_price %s deberia rechazarse, hubo %v", bad, err)
		}
	}
	p := validPlan()
	p.BillingPeriod = "weekly"
	if err := p.Validate(); !errors.Is(err, ErrInvalidPlan) {
		t.Errorf("billing_period weekly deberia rechazarse, hubo %v", err)
	}
}

func TestLimitesDelPlan(t *testing.T) {
	price := decimal.RequireFromString("0.0015")
	tooFine := decimal.RequireFromString("0.0000001")
	mutate := map[string]func(l []PlanLimit) []PlanLimit{
		"falta un recurso": func(l []PlanLimit) []PlanLimit { return l[:len(l)-1] },
		"recurso repetido": func(l []PlanLimit) []PlanLimit { return append(l, l[0]) },
		"recurso desconocido": func(l []PlanLimit) []PlanLimit {
			l[0].Resource = "gigas"
			return l
		},
		"included menor que -1": func(l []PlanLimit) []PlanLimit {
			l[0].Included = -2
			return l
		},
		"excedente en limite duro": func(l []PlanLimit) []PlanLimit {
			l[0].OverageUnitPrice = &price
			return l
		},
		"excedente en ilimitado": func(l []PlanLimit) []PlanLimit {
			l[0].HardLimit, l[0].Included, l[0].OverageUnitPrice = false, Unlimited, &price
			return l
		},
		"excedente con demasiados decimales": func(l []PlanLimit) []PlanLimit {
			l[0].HardLimit, l[0].OverageUnitPrice = false, &tooFine
			return l
		},
	}
	for name, fn := range mutate {
		p := validPlan()
		p.Limits = fn(fullLimits())
		if err := p.Validate(); !errors.Is(err, ErrInvalidPlan) {
			t.Errorf("%s: deberia rechazarse, hubo %v", name, err)
		}
	}

	p := validPlan()
	p.Limits[0].HardLimit, p.Limits[0].OverageUnitPrice = false, &price
	p.Limits[1].Included = Unlimited
	if err := p.Validate(); err != nil {
		t.Fatalf("limite blando con excedente e ilimitado deberian valer: %v", err)
	}
}

func TestLimiteAusenteFallaCerrado(t *testing.T) {
	p := validPlan()
	p.Limits = nil
	l := p.EffectiveLimit(ResourceMailboxes)
	if l.Included != 0 || !l.HardLimit || l.Resource != ResourceMailboxes {
		t.Fatalf("un recurso sin fila debe ser 0 incluido y limite duro: %+v", l)
	}
}

func TestCambioParcialDePlan(t *testing.T) {
	if !(PlanPatch{}).Empty() {
		t.Fatal("un cambio sin campos debe ser vacio")
	}
	name := "  Nuevo nombre  "
	if (PlanPatch{Name: &name}).ChangesTerms() {
		t.Fatal("el nombre no toca las condiciones")
	}
	price := decimal.RequireFromString("59.90")
	patch := PlanPatch{Name: &name, BasePrice: &price, ReplaceLimits: true, Limits: fullLimits()[:1]}
	if !patch.ChangesTerms() {
		t.Fatal("precio y limites son condiciones")
	}
	p := validPlan()
	p.Apply(patch)
	if p.Name != "Nuevo nombre" || !p.BasePrice.Equal(price) || len(p.Limits) != 1 {
		t.Fatalf("cambio no aplicado: %+v", p)
	}
}
