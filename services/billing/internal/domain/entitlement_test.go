package domain

import "testing"

func TestDerechos(t *testing.T) {
	mb := ResourceMailboxes
	hard := &PlanLimit{Resource: mb, Included: 10, HardLimit: true}
	soft := &PlanLimit{Resource: mb, Included: 10, HardLimit: false}
	unlimited := &PlanLimit{Resource: mb, Included: Unlimited, HardLimit: true}
	sub := func(st SubscriptionStatus) *Subscription { return &Subscription{Status: st} }

	cases := []struct {
		name      string
		in        EntitlementInput
		allowed   bool
		reason    DenyReason
		remaining int64
	}{
		{"ilimitado", EntitlementInput{Resource: mb, Quantity: 1000, Used: 5000, Subscription: sub(StatusActive), Limit: unlimited}, true, "", Unlimited},
		{"duro con margen", EntitlementInput{Resource: mb, Quantity: 1, Used: 9, Subscription: sub(StatusActive), Limit: hard}, true, "", 1},
		{"duro en el tope", EntitlementInput{Resource: mb, Quantity: 1, Used: 10, Subscription: sub(StatusActive), Limit: hard}, false, ReasonLimitReached, 0},
		{"duro con cantidad que excede", EntitlementInput{Resource: mb, Quantity: 6, Used: 5, Subscription: sub(StatusActive), Limit: hard}, false, ReasonLimitReached, 5},
		{"blando por encima", EntitlementInput{Resource: mb, Quantity: 5, Used: 12, Subscription: sub(StatusActive), Limit: soft}, true, "", 0},
		{"en prueba", EntitlementInput{Resource: mb, Quantity: 1, Used: 0, Subscription: sub(StatusTrialing), Limit: hard}, true, "", 10},
		{"pago vencido en gracia", EntitlementInput{Resource: mb, Quantity: 1, Used: 0, Subscription: sub(StatusPastDue), Limit: hard}, true, "", 10},
		{"suspendida", EntitlementInput{Resource: mb, Quantity: 1, Used: 0, Subscription: sub(StatusSuspended), Limit: hard}, false, ReasonSubscriptionInactive, 10},
		{"cancelada", EntitlementInput{Resource: mb, Quantity: 1, Used: 0, Subscription: sub(StatusCancelled), Limit: unlimited}, false, ReasonSubscriptionInactive, Unlimited},
		{"plan sin fila para el recurso", EntitlementInput{Resource: mb, Quantity: 1, Used: 0, Subscription: sub(StatusActive)}, false, ReasonLimitReached, 0},
	}
	for _, c := range cases {
		got := Evaluate(c.in)
		if got.Allowed != c.allowed || got.Reason != c.reason {
			t.Errorf("%s: allowed=%v reason=%q; se esperaba %v %q", c.name, got.Allowed, got.Reason, c.allowed, c.reason)
			continue
		}
		if got.Remaining == nil || *got.Remaining != c.remaining || got.Limit == nil {
			t.Errorf("%s: remaining=%v limit=%v; se esperaba remaining %d", c.name, got.Remaining, got.Limit, c.remaining)
		}
		if got.Used != c.in.Used || got.Resource != mb {
			t.Errorf("%s: used=%d resource=%s", c.name, got.Used, got.Resource)
		}
	}
}

func TestDerechosSinSuscripcion(t *testing.T) {
	in := EntitlementInput{Resource: ResourceDomains, Quantity: 1, Used: 3}

	got := Evaluate(in)
	if !got.Allowed || got.Reason != "" || got.Limit != nil || got.Remaining != nil {
		t.Fatalf("sin enforce se permite y no hay limite que informar: %+v", got)
	}

	in.Enforce = true
	got = Evaluate(in)
	if got.Allowed || got.Reason != ReasonNoSubscription || got.Used != 3 {
		t.Fatalf("con enforce se deniega por no_subscription: %+v", got)
	}
}
