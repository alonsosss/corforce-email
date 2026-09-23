package domain

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validAB() *ABTest {
	return &ABTest{
		Criterion: CriterionOpens, SamplePercent: 20, DecisionWindowMinutes: 240,
		Variants: []ABVariant{{Subject: "Oferta de otono"}, {Subject: "Solo hoy: 20 % menos"}},
	}
}

func TestABTestValidate(t *testing.T) {
	tpl := uuid.New()
	other := uuid.New()
	nilID := uuid.Nil
	zero, two := 0, 2
	cases := map[string]struct {
		mutate func(a *ABTest)
		ok     bool
	}{
		"valida":               {func(a *ABTest) {}, true},
		"criterio clics":       {func(a *ABTest) { a.Criterion = CriterionClicks }, true},
		"criterio desconocido": {func(a *ABTest) { a.Criterion = "revenue" }, false},
		"muestra minima":       {func(a *ABTest) { a.SamplePercent = MinSamplePercent }, true},
		"muestra maxima":       {func(a *ABTest) { a.SamplePercent = MaxSamplePercent }, true},
		"muestra por debajo":   {func(a *ABTest) { a.SamplePercent = MinSamplePercent - 1 }, false},
		"muestra por encima":   {func(a *ABTest) { a.SamplePercent = MaxSamplePercent + 1 }, false},
		"ventana minima":       {func(a *ABTest) { a.DecisionWindowMinutes = 60 }, true},
		"ventana maxima":       {func(a *ABTest) { a.DecisionWindowMinutes = 72 * 60 }, true},
		"ventana corta":        {func(a *ABTest) { a.DecisionWindowMinutes = 59 }, false},
		"ventana larga":        {func(a *ABTest) { a.DecisionWindowMinutes = 72*60 + 1 }, false},
		"una variante":         {func(a *ABTest) { a.Variants = a.Variants[:1] }, false},
		"cuatro variantes":     {func(a *ABTest) { a.Variants = append(a.Variants, ABVariant{Subject: "c"}, ABVariant{Subject: "d"}) }, true},
		"cinco variantes": {func(a *ABTest) {
			a.Variants = append(a.Variants, ABVariant{Subject: "c"}, ABVariant{Subject: "d"}, ABVariant{Subject: "e"})
		}, false},
		"asunto repetido": {func(a *ABTest) { a.Variants[1].Subject = a.Variants[0].Subject }, false},
		"dos sin cambios": {func(a *ABTest) { a.Variants[0].Subject, a.Variants[1].Subject = "", "" }, false},
		"contenido distinto": {func(a *ABTest) {
			a.Variants[0].Subject, a.Variants[1].Subject = "", ""
			a.Variants[1].TemplateID = &other
		}, true},
		"plantilla propia igual": {func(a *ABTest) {
			a.Variants[0].Subject, a.Variants[1].Subject = "", ""
			a.Variants[1].TemplateID = &tpl
		}, false},
		"version distinta": {func(a *ABTest) {
			a.Variants[0].Subject, a.Variants[1].Subject = "", ""
			a.Variants[1].TemplateVersion = &two
		}, true},
		"plantilla vacia":           {func(a *ABTest) { a.Variants[0].TemplateID = &nilID }, false},
		"version cero":              {func(a *ABTest) { a.Variants[0].TemplateVersion = &zero }, false},
		"asunto con salto de linea": {func(a *ABTest) { a.Variants[0].Subject = "Hola\r\nBcc: x@y.z" }, false},
		"asunto con control":        {func(a *ABTest) { a.Variants[0].Subject = "Hola\x00" }, false},
		"asunto en el tope":         {func(a *ABTest) { a.Variants[0].Subject = strings.Repeat("n", MaxSubjectLength) }, true},
		"asunto demasiado largo":    {func(a *ABTest) { a.Variants[0].Subject = strings.Repeat("n", MaxSubjectLength+1) }, false},
		"asunto largo en multibyte": {func(a *ABTest) { a.Variants[0].Subject = strings.Repeat("\u00f1", MaxSubjectLength) }, true},
	}
	for name, tc := range cases {
		a := validAB()
		tc.mutate(a)
		err := a.Validate(tpl)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidCampaign) {
			t.Errorf("%s: se esperaba error de validacion, err = %v", name, err)
		}
	}
}

// El reparto es determinista, respeta el porcentaje y divide la muestra en partes casi
// iguales; campanas distintas reparten distinto.
func TestABAssignDistribution(t *testing.T) {
	for _, tc := range []struct{ pct, variants int }{{10, 2}, {20, 3}, {50, 4}, {33, 3}} {
		a := validAB()
		a.SamplePercent = tc.pct
		a.Variants = make([]ABVariant, tc.variants)
		for i := range a.Variants {
			a.Variants[i].Subject = string(rune('a' + i))
		}
		campaign := uuid.New()
		const n = 40000
		counts := make([]int, tc.variants)
		sample := 0
		for i := 0; i < n; i++ {
			id := uuid.New()
			v, in := a.Assign(campaign, id)
			if v2, in2 := a.Assign(campaign, id); v2 != v || in2 != in {
				t.Fatal("el reparto no es determinista")
			}
			if in {
				sample++
				counts[v]++
			} else if v != 0 {
				t.Fatalf("fuera de la muestra la variante es 0, no %d", v)
			}
		}
		got := float64(sample) / n * 100
		if math.Abs(got-float64(tc.pct)) > 1.5 {
			t.Errorf("%d %%: la muestra salio del %.2f %%", tc.pct, got)
		}
		for i, c := range counts {
			share := float64(c) / float64(sample)
			if math.Abs(share-1/float64(tc.variants)) > 0.03 {
				t.Errorf("%d %% con %d variantes: la %d recibe el %.3f de la muestra", tc.pct, tc.variants, i, share)
			}
		}
	}
	contact := uuid.New()
	differs := false
	for i := 0; i < 50 && !differs; i++ {
		differs = SampleBucket(uuid.New(), contact) != SampleBucket(uuid.New(), contact)
	}
	if !differs {
		t.Fatal("el reparto debe depender de la campana")
	}
	if b := SampleBucket(uuid.New(), contact); b < 0 || b >= sampleBuckets {
		t.Fatalf("posicion fuera de rango: %d", b)
	}
}

func TestSelectWinner(t *testing.T) {
	r := func(v int, delivered, opened, clicked int64) VariantResult {
		return VariantResult{Variant: v, Accepted: delivered, Delivered: delivered, Opened: opened, Clicked: clicked}
	}
	cases := []struct {
		name      string
		criterion ABCriterion
		variants  int
		results   []VariantResult
		winner    int
		reason    DecisionReason
	}{
		{"mejor tasa de aperturas", CriterionOpens, 2, []VariantResult{r(0, 100, 20, 5), r(1, 100, 30, 1)}, 1, ReasonCriterion},
		{"la tasa, no el numero", CriterionOpens, 2, []VariantResult{r(0, 1000, 150, 0), r(1, 100, 20, 0)}, 1, ReasonCriterion},
		{"mejor tasa de clics", CriterionClicks, 3, []VariantResult{r(0, 100, 50, 2), r(1, 100, 10, 9), r(2, 100, 60, 3)}, 1, ReasonCriterion},
		{"empate en aperturas: gana por clics", CriterionOpens, 2, []VariantResult{r(0, 100, 20, 1), r(1, 200, 40, 5)}, 1, ReasonSecondary},
		{"empate en clics: gana por aperturas", CriterionClicks, 2, []VariantResult{r(0, 100, 30, 2), r(1, 100, 10, 2)}, 0, ReasonSecondary},
		{"empate en tasas: mas entregados", CriterionOpens, 2, []VariantResult{r(0, 10, 1, 0), r(1, 100, 10, 0)}, 1, ReasonDelivered},
		{"empate total: la primera", CriterionOpens, 3, []VariantResult{r(0, 100, 10, 1), r(1, 100, 10, 1), r(2, 100, 10, 1)}, 0, ReasonFirst},
		{"sin datos: la primera", CriterionOpens, 2, nil, 0, ReasonFirst},
		{"sin entregas la tasa es cero", CriterionOpens, 2, []VariantResult{r(0, 0, 0, 0), r(1, 50, 1, 0)}, 1, ReasonCriterion},
		{"variante sin filas cuenta como cero", CriterionOpens, 3, []VariantResult{r(2, 100, 5, 0)}, 2, ReasonCriterion},
		{"filas fuera de rango se ignoran", CriterionOpens, 2, []VariantResult{r(0, 100, 5, 0), r(7, 100, 99, 99)}, 0, ReasonCriterion},
		{"la ultima de cuatro", CriterionClicks, 4, []VariantResult{r(0, 100, 1, 1), r(1, 100, 1, 2), r(2, 100, 1, 3), r(3, 100, 1, 4)}, 3, ReasonCriterion},
		// A y C empatan en todo salvo entregados; la ganadora (C) necesita el tercer criterio frente a A.
		{"razon: el desempate mas profundo", CriterionOpens, 3, []VariantResult{r(0, 100, 10, 1), r(1, 100, 5, 0), r(2, 200, 20, 2)}, 2, ReasonDelivered},
	}
	for _, tc := range cases {
		d := SelectWinner(tc.criterion, tc.variants, tc.results)
		if d.Winner != tc.winner || d.Reason != tc.reason {
			t.Errorf("%s: gana %d (%s), se esperaba %d (%s)", tc.name, d.Winner, d.Reason, tc.winner, tc.reason)
		}
		if len(d.Results) != tc.variants || d.Criterion != tc.criterion {
			t.Errorf("%s: la decision guarda una fila por variante y el criterio: %+v", tc.name, d)
		}
		for i, row := range d.Results {
			if row.Variant != i {
				t.Errorf("%s: fila %d con variante %d", tc.name, i, row.Variant)
			}
		}
	}
}

// Tasas que difieren en la decima cifra: la comparacion es exacta.
func TestCompareRatesIsExact(t *testing.T) {
	if compareRates(333333333, 1000000000, 1, 3) >= 0 {
		t.Fatal("0.333333333 < 1/3")
	}
	if compareRates(2, 6, 1, 3) != 0 {
		t.Fatal("2/6 = 1/3")
	}
	if compareRates(5, 0, 0, 10) != 0 {
		t.Fatal("sin entregas la tasa es cero")
	}
	if compareRates(math.MaxInt64, math.MaxInt64, math.MaxInt64-1, math.MaxInt64) <= 0 {
		t.Fatal("sin desbordar con contadores enormes")
	}
}

func TestVariantLabel(t *testing.T) {
	if VariantLabel(0) != "A" || VariantLabel(3) != "D" || VariantLabel(4) != "" || VariantLabel(-1) != "" {
		t.Fatal("letras de las variantes")
	}
}

func TestResendValidate(t *testing.T) {
	cases := map[string]struct {
		r  Resend
		ok bool
	}{
		"valido":           {Resend{Subject: "Te lo perdiste", DelayMinutes: 48 * 60}, true},
		"retraso minimo":   {Resend{Subject: "x", DelayMinutes: 24 * 60}, true},
		"retraso maximo":   {Resend{Subject: "x", DelayMinutes: 7 * 24 * 60}, true},
		"retraso corto":    {Resend{Subject: "x", DelayMinutes: 24*60 - 1}, false},
		"retraso largo":    {Resend{Subject: "x", DelayMinutes: 7*24*60 + 1}, false},
		"sin asunto":       {Resend{DelayMinutes: 48 * 60}, false},
		"asunto con salto": {Resend{Subject: "a\nb", DelayMinutes: 48 * 60}, false},
		"asunto muy largo": {Resend{Subject: strings.Repeat("a", MaxSubjectLength+1), DelayMinutes: 48 * 60}, false},
	}
	for name, tc := range cases {
		err := tc.r.Validate()
		if tc.ok != (err == nil) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
