package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestContadorDeStockNuncaNegativo(t *testing.T) {
	cases := []struct{ current, delta, want int64 }{
		{0, -1, 0},
		{1, -1, 0},
		{5, -1, 4},
		{0, 1, 1},
		{41, 1, 42},
	}
	for _, c := range cases {
		if got := NextQuantity(c.current, c.delta); got != c.want {
			t.Errorf("NextQuantity(%d, %d) = %d; se esperaba %d", c.current, c.delta, got, c.want)
		}
	}
}

func TestPeriodoDelContador(t *testing.T) {
	start := time.Date(2026, 2, 28, 13, 0, 0, 0, time.UTC)
	if got := ResourceMailboxes.CounterPeriod(start); !got.Equal(StockPeriodStart) {
		t.Fatalf("stock: %s", got)
	}
	if got := ResourceTransactionalMessages.CounterPeriod(start); !got.Equal(day(2026, 2, 28)) {
		t.Fatalf("flujo: %s", got)
	}
}

func TestLineaDeConsumo(t *testing.T) {
	limit := func(included int64) PlanLimit {
		return PlanLimit{Resource: ResourceTransactionalMessages, Included: included, HardLimit: false}
	}
	cases := []struct {
		included, used, overage int64
		percent                 string
	}{
		{200, 100, 0, "50.00"},
		{200, 250, 50, "125.00"},
		{3, 1, 0, "33.33"},
		{0, 4, 4, ""},
		{Unlimited, 900, 0, ""},
	}
	for _, c := range cases {
		line := NewUsageLine(limit(c.included), c.used)
		if line.Overage != c.overage || !line.Flow || line.Used != c.used || line.Included != c.included {
			t.Errorf("included=%d used=%d: %+v", c.included, c.used, line)
		}
		switch {
		case c.percent == "" && line.Percent != nil:
			t.Errorf("included=%d: sin porcentaje, hubo %s", c.included, line.Percent)
		case c.percent != "" && (line.Percent == nil || line.Percent.StringFixed(2) != c.percent):
			t.Errorf("included=%d used=%d: porcentaje %v; se esperaba %s", c.included, c.used, line.Percent, c.percent)
		}
	}
}

func TestAvisoDeLimiteUnaVezPorPeriodo(t *testing.T) {
	period := day(2026, 9, 1)
	c := &Counter{}
	if c.NotifiedIn(period) {
		t.Fatal("sin aviso previo")
	}
	c.LimitReachedPeriod = &period
	if !c.NotifiedIn(time.Date(2026, 9, 1, 18, 0, 0, 0, time.UTC)) || c.NotifiedIn(day(2026, 10, 1)) {
		t.Fatal("el aviso vale para su periodo y solo para el")
	}
	hard := PlanLimit{Included: 5, HardLimit: true}
	if hard.ReachedBy(4) || !hard.ReachedBy(5) || (PlanLimit{Included: 5}).ReachedBy(9) || (PlanLimit{Included: Unlimited, HardLimit: true}).ReachedBy(9) {
		t.Fatal("solo un limite duro con tope se alcanza")
	}
}

func TestValidacionDeUnCambioDeConsumo(t *testing.T) {
	valid := UsageChange{EventID: "e-1", Subject: "mail.mailbox.created", TenantID: uuid.New(), Resource: ResourceMailboxes, Delta: 1}
	if err := valid.Validate(); err != nil {
		t.Fatalf("cambio valido rechazado: %v", err)
	}
	bad := map[string]func(c *UsageChange){
		"sin id":              func(c *UsageChange) { c.EventID = " " },
		"sin empresa":         func(c *UsageChange) { c.TenantID = uuid.Nil },
		"recurso desconocido": func(c *UsageChange) { c.Resource = "gigas" },
		"delta de dos":        func(c *UsageChange) { c.Delta = 2 },
		"flujo que resta":     func(c *UsageChange) { c.Resource, c.Delta = ResourceTransactionalMessages, -1 },
		"flujo con objeto":    func(c *UsageChange) { c.Resource, c.ItemKey, c.Source = ResourceTransactionalMessages, "x", "y" },
		"objeto sin fuente":   func(c *UsageChange) { c.ItemKey = "a.test" },
	}
	for name, mutate := range bad {
		c := valid
		mutate(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s: se esperaba ErrInvalidEvent, hubo %v", name, err)
		}
	}
}
