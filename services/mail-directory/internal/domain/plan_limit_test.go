package domain

import (
	"errors"
	"testing"
)

func TestCheckPlanLimit(t *testing.T) {
	tope := errors.New("tope")
	casos := []struct {
		nombre    string
		resulting int64
		limite    PlanLimit
		quiere    error
	}{
		{"sin plan no restringe", 1 << 40, PlanLimit{Unknown: true}, nil},
		{"plan sin limite del recurso no restringe", 1 << 40, PlanLimit{Included: -1, HardLimit: true}, nil},
		{"limite blando no restringe: billing factura el exceso", 99, PlanLimit{Included: 10, HardLimit: false}, nil},
		{"por debajo del limite duro pasa", 10, PlanLimit{Included: 10, HardLimit: true}, nil},
		{"justo en el limite duro pasa", 10, PlanLimit{Included: 10, HardLimit: true}, nil},
		{"por encima del limite duro se rechaza", 11, PlanLimit{Included: 10, HardLimit: true}, tope},
		{"un plan de cero no admite nada", 1, PlanLimit{Included: 0, HardLimit: true}, tope},
		// Una empresa dada de baja no crece aunque su plan no limitara: quien cancela no sigue
		// consumiendo (ADR 0010). Gana sobre cualquier otra condicion.
		{"dada de baja no crece ni con plan sin limite", 1, PlanLimit{Included: -1, HardLimit: true, SubscriptionInactive: true}, ErrSubscriptionInactive},
		{"dada de baja no crece ni con limite blando", 1, PlanLimit{Included: 10, SubscriptionInactive: true}, ErrSubscriptionInactive},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := CheckPlanLimit(c.resulting, c.limite, tope); !errors.Is(got, c.quiere) {
				t.Fatalf("CheckPlanLimit(%d, %+v) = %v; se esperaba %v", c.resulting, c.limite, got, c.quiere)
			}
		})
	}
}
