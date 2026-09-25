package http

import "testing"

func TestComposeGateAcotaElTotalYUnoPorBuzon(t *testing.T) {
	g := newComposeGate(2)
	releaseAna, ok := g.acquire("ana@x.com")
	if !ok {
		t.Fatal("el primer envio entra")
	}
	if _, ok := g.acquire("ana@x.com"); ok {
		t.Fatal("un buzon compone de uno en uno")
	}
	releaseLuis, ok := g.acquire("luis@x.com")
	if !ok {
		t.Fatal("otro buzon entra mientras quede hueco")
	}
	if _, ok := g.acquire("eva@x.com"); ok {
		t.Fatal("sin hueco global no entra nadie mas")
	}
	releaseAna()
	if release, ok := g.acquire("eva@x.com"); !ok {
		t.Fatal("al terminar uno queda su hueco libre")
	} else {
		release()
	}
	releaseLuis()
	if release, ok := g.acquire("ana@x.com"); !ok {
		t.Fatal("el buzon vuelve a poder componer al terminar")
	} else {
		release()
	}
}
