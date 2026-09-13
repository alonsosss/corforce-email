package domain

import (
	"encoding/json"
	"testing"
)

func TestTasasConDenominadorCero(t *testing.T) {
	zero := Rates{Delivery: "0.0000", Bounce: "0.0000", Complaint: "0.0000", Open: "0.0000", Click: "0.0000", Unsubscribe: "0.0000"}
	if got := (Counters{}).Rates(); got != zero {
		t.Fatalf("sin envios todas las tasas son 0.0000: %+v", got)
	}
	// Aperturas sin entregas registradas: el denominador es cero y la tasa no puede ser Inf.
	if got := (Counters{Sent: 0, OpenedUnique: 7, Complained: 2}).Rates(); got.Open != "0.0000" || got.Complaint != "0.0000" {
		t.Fatalf("denominador cero: %+v", got)
	}
	if _, err := json.Marshal((Counters{OpenedUnique: 3}).Rates()); err != nil {
		t.Fatalf("las tasas siempre se serializan: %v", err)
	}
}

func TestTasasConCuatroDecimales(t *testing.T) {
	c := Counters{Sent: 3, Delivered: 2, BouncedHard: 1, Complained: 1, OpenedUnique: 2, ClickedUnique: 1, Unsubscribed: 0}
	want := Rates{Delivery: "0.6667", Bounce: "0.3333", Complaint: "0.5000", Open: "1.0000", Click: "0.5000", Unsubscribe: "0.0000"}
	if got := c.Rates(); got != want {
		t.Fatalf("tasas: %+v, se esperaba %+v", got, want)
	}
	if got := (Counters{Sent: 8, BouncedHard: 1, BouncedSoft: 1}).Rates().Bounce; got != "0.2500" {
		t.Fatalf("el rebote suma duros y blandos: %s", got)
	}
}

func TestContadoresNoNegativos(t *testing.T) {
	var c Counters
	c.Add(CounterBouncedHard, 1)
	c.Add(CounterBouncedSoft, -1)
	if c.NonNegative() {
		t.Fatal("un descuento no es aditivo")
	}
	if !(Counters{Sent: 1}).NonNegative() || !(Counters{}).IsZero() {
		t.Fatal("NonNegative/IsZero")
	}
}
