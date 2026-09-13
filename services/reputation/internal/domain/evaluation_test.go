package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func baseThresholds() Thresholds {
	return Thresholds{
		BounceWarn: dec("0.02"), BounceBlock: dec("0.04"),
		ComplaintWarn: dec("0.0005"), ComplaintBlock: dec("0.0008"),
		MinVolume: 200,
	}
}

var evalNow = time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC)

func TestRateSinEnviosEsCero(t *testing.T) {
	for _, c := range []struct{ events, sent int64 }{{5, 0}, {0, 0}, {0, 100}, {3, -1}} {
		if r := Rate(c.events, c.sent); !r.IsZero() {
			t.Fatalf("Rate(%d, %d) = %s; se esperaba 0", c.events, c.sent, r)
		}
	}
}

func TestRateDecimalConEscalaFija(t *testing.T) {
	cases := []struct {
		events, sent int64
		want         string
	}{
		{1, 3, "0.333333"},
		{2, 3, "0.666667"},
		{4, 100, "0.040000"},
		{1, 2000, "0.000500"},
		{1, 3000000, "0.000000"},
	}
	for _, c := range cases {
		if got := Rate(c.events, c.sent).StringFixed(RateScale); got != c.want {
			t.Errorf("Rate(%d, %d) = %s; se esperaba %s", c.events, c.sent, got, c.want)
		}
	}
}

func TestRateAcotadaAUno(t *testing.T) {
	if r := Rate(10, 5); !r.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("una tasa por encima de 1 debe acotarse: %s", r)
	}
}

func TestEvaluateVolumenMinimo(t *testing.T) {
	v := Evaluate(ClassMarketing, Counts{Sent: 199, Bounced: 150, Complained: 50}, baseThresholds())
	if v.State != StateOK || v.Reason != ReasonInsufficientVolume {
		t.Fatalf("con poco volumen no se juzga: %+v", v)
	}
	if v.BounceRate.IsZero() || v.ComplaintRate.IsZero() {
		t.Fatalf("las tasas se informan aunque no se juzgue: %+v", v)
	}
}

func TestEvaluateMarketing(t *testing.T) {
	cases := []struct {
		name   string
		c      Counts
		state  State
		reason string
	}{
		{"limpio", Counts{Sent: 1000, Bounced: 19}, StateOK, ReasonWithinThresholds},
		{"rebote en warn", Counts{Sent: 1000, Bounced: 20}, StateWarning, ReasonBounceWarn},
		{"rebote en block", Counts{Sent: 1000, Bounced: 40}, StateRestricted, ReasonBounceBlock},
		{"queja en warn", Counts{Sent: 10000, Complained: 5}, StateWarning, ReasonComplaintWarn},
		{"queja en block", Counts{Sent: 10000, Complained: 8}, StateRestricted, ReasonComplaintBlock},
		{"la queja se juzga antes que el rebote", Counts{Sent: 10000, Bounced: 400, Complained: 8}, StateRestricted, ReasonComplaintBlock},
		{"rebote en block con queja en warn", Counts{Sent: 10000, Bounced: 400, Complained: 5}, StateRestricted, ReasonBounceBlock},
		{"justo en el volumen minimo", Counts{Sent: 200, Bounced: 8}, StateRestricted, ReasonBounceBlock},
	}
	for _, c := range cases {
		v := Evaluate(ClassMarketing, c.c, baseThresholds())
		if v.State != c.state || v.Reason != c.reason {
			t.Errorf("%s: %s/%s; se esperaba %s/%s", c.name, v.State, v.Reason, c.state, c.reason)
		}
	}
}

func TestEvaluateTransaccionalDobleUmbral(t *testing.T) {
	cases := []struct {
		name   string
		c      Counts
		state  State
		reason string
	}{
		{"rebote justo en 1x no restringe", Counts{Sent: 1000, Bounced: 40}, StateWarning, ReasonBounceWarn},
		{"rebote entre 1x y 2x queda en warning", Counts{Sent: 1000, Bounced: 79}, StateWarning, ReasonBounceWarn},
		{"rebote en 2x restringe", Counts{Sent: 1000, Bounced: 80}, StateRestricted, ReasonBounceBlock},
		{"queja entre 1x y 2x queda en warning", Counts{Sent: 10000, Complained: 10}, StateWarning, ReasonComplaintWarn},
		{"queja en 2x restringe", Counts{Sent: 10000, Complained: 16}, StateRestricted, ReasonComplaintBlock},
		{"los umbrales de aviso no se doblan", Counts{Sent: 1000, Bounced: 20}, StateWarning, ReasonBounceWarn},
	}
	for _, c := range cases {
		v := Evaluate(ClassTransactional, c.c, baseThresholds())
		if v.State != c.state || v.Reason != c.reason {
			t.Errorf("%s: %s/%s; se esperaba %s/%s", c.name, v.State, v.Reason, c.state, c.reason)
		}
	}
}

func TestThresholdsForSoloDoblaElBloqueoTransaccional(t *testing.T) {
	base := baseThresholds()
	tx := base.For(ClassTransactional)
	if !tx.BounceBlock.Equal(dec("0.08")) || !tx.ComplaintBlock.Equal(dec("0.0016")) {
		t.Fatalf("bloqueo transaccional: %s / %s", tx.BounceBlock, tx.ComplaintBlock)
	}
	if !tx.BounceWarn.Equal(base.BounceWarn) || !tx.ComplaintWarn.Equal(base.ComplaintWarn) {
		t.Fatal("los umbrales de aviso no deben cambiar")
	}
	mk := base.For(ClassMarketing)
	if !mk.BounceBlock.Equal(base.BounceBlock) || !mk.ComplaintBlock.Equal(base.ComplaintBlock) {
		t.Fatal("marketing usa los umbrales tal cual")
	}
}

func TestTransitionNoPisaElEstadoManual(t *testing.T) {
	levantado := Record{State: StateOK, Manual: true}
	if _, changed := Transition(levantado, Verdict{State: StateRestricted}, evalNow); changed {
		t.Fatal("la evaluacion no debe pisar un levantamiento manual")
	}
	suspendido := Record{State: StateSuspended, Manual: true}
	if _, changed := Transition(suspendido, Verdict{State: StateOK}, evalNow); changed {
		t.Fatal("la evaluacion no debe levantar una suspension")
	}
}

func TestTransitionCambiaSoloSiCambiaElEstado(t *testing.T) {
	by := uuid.New()
	cur := Record{State: StateOK, Reason: ReasonInitial, ChangedBy: &by}
	next, changed := Transition(cur, Verdict{State: StateWarning, Reason: ReasonBounceWarn, BounceRate: dec("0.02")}, evalNow)
	if !changed || next.State != StateWarning || next.Reason != ReasonBounceWarn {
		t.Fatalf("transicion: %+v changed=%v", next, changed)
	}
	if next.Manual || next.ChangedBy != nil || !next.ChangedAt.Equal(evalNow) || !next.BounceRate.Equal(dec("0.02")) {
		t.Fatalf("un cambio automatico no lleva autor y fija el instante y las tasas: %+v", next)
	}
	if _, changed := Transition(next, Verdict{State: StateWarning, Reason: ReasonBounceWarn}, evalNow); changed {
		t.Fatal("repetir el estado no es un cambio")
	}
}

func TestSuspendYRelease(t *testing.T) {
	by := uuid.New()
	sus, changed := Suspend(Record{State: StateRestricted}, "abuso confirmado", Verdict{State: StateRestricted, BounceRate: dec("0.05")}, by, evalNow)
	if !changed || sus.State != StateSuspended || !sus.Manual || sus.ChangedBy == nil || *sus.ChangedBy != by || sus.Reason != "abuso confirmado" {
		t.Fatalf("suspension: %+v", sus)
	}
	if _, changed := Suspend(sus, "otra vez", Verdict{}, by, evalNow); changed {
		t.Fatal("suspender lo ya suspendido no es un cambio")
	}
	rel, changed := Release(sus, Verdict{State: StateWarning, Reason: ReasonBounceWarn}, by, evalNow)
	if !changed || rel.State != StateWarning || rel.Manual || rel.Reason != ReasonBounceWarn || *rel.ChangedBy != by {
		t.Fatalf("liberacion: %+v", rel)
	}
	if _, changed := Release(rel, Verdict{State: StateWarning}, by, evalNow); changed {
		t.Fatal("liberar sin marca y con el mismo estado no es un cambio")
	}
	manualOK := Record{State: StateOK, Manual: true}
	quitada, changed := Release(manualOK, Verdict{State: StateOK}, by, evalNow)
	if !changed || quitada.Manual {
		t.Fatal("quitar la marca manual es un cambio aunque el estado se repita")
	}
}

func TestNormalizeReason(t *testing.T) {
	if _, err := NormalizeReason("   "); err == nil {
		t.Fatal("un motivo vacio no vale")
	}
	if got, err := NormalizeReason("  abuso  "); err != nil || got != "abuso" {
		t.Fatalf("recorte: %q %v", got, err)
	}
	if _, err := NormalizeReason(strings.Repeat("a", MaxReasonLength+1)); err == nil {
		t.Fatal("un motivo demasiado largo no vale")
	}
	if _, err := NormalizeReason(strings.Repeat("ñ", MaxReasonLength)); err != nil {
		t.Fatalf("el limite cuenta caracteres, no bytes: %v", err)
	}
}

func TestChangeOf(t *testing.T) {
	by := uuid.New()
	from := Record{State: StateWarning}
	to := Record{TenantID: uuid.New(), Class: ClassMarketing, State: StateSuspended, Reason: "abuso", ChangedBy: &by, ChangedAt: evalNow, BounceRate: dec("0.03")}
	c := ChangeOf(from, to, true)
	if c.From != StateWarning || c.To != StateSuspended || !c.Manual || *c.ChangedBy != by || !c.CreatedAt.Equal(evalNow) || !c.BounceRate.Equal(dec("0.03")) {
		t.Fatalf("change: %+v", c)
	}
}
