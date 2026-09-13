package domain

// DenyReason explica por que se deniega un derecho.
type DenyReason string

const (
	ReasonNoSubscription       DenyReason = "no_subscription"
	ReasonSubscriptionInactive DenyReason = "subscription_inactive"
	ReasonLimitReached         DenyReason = "limit_reached"
)

// MaxCheckQuantity acota la cantidad de una consulta; cubre bytes de cuota sin riesgo de
// desbordar la suma con lo ya usado.
const MaxCheckQuantity int64 = 1 << 50

// Entitlement es la respuesta a "puede esta empresa consumir N unidades mas". Limit y
// Remaining son nil cuando no hay plan que consultar, y -1 cuando el plan no limita.
type Entitlement struct {
	Allowed   bool
	Resource  Resource
	Limit     *int64
	Used      int64
	Remaining *int64
	HardLimit bool
	Reason    DenyReason
}

// EntitlementInput reune lo que decide un derecho. Subscription nil = la empresa no tiene
// suscripcion; Limit es el limite efectivo del plan para el recurso.
type EntitlementInput struct {
	Resource     Resource
	Quantity     int64
	Used         int64
	Subscription *Subscription
	Limit        *PlanLimit
	// Enforce deniega a la empresa sin suscripcion. Apagado solo durante la puesta en
	// marcha, mientras las empresas existentes reciben su plan.
	Enforce bool
}

// Evaluate decide el derecho. No incrementa nada: el consumo se cuenta cuando llega el
// evento del hecho consumado.
func Evaluate(in EntitlementInput) Entitlement {
	out := Entitlement{Resource: in.Resource, Used: in.Used}
	if in.Subscription == nil {
		out.Allowed = !in.Enforce
		if in.Enforce {
			out.Reason = ReasonNoSubscription
		}
		return out
	}

	limit := PlanLimit{Resource: in.Resource, Included: 0, HardLimit: true}
	if in.Limit != nil {
		limit = *in.Limit
	}
	included := limit.Included
	remaining := Unlimited
	if included != Unlimited {
		remaining = included - in.Used
		if remaining < 0 {
			remaining = 0
		}
	}
	out.Limit, out.Remaining, out.HardLimit = &included, &remaining, limit.HardLimit

	if !in.Subscription.Status.AllowsUsage() {
		out.Reason = ReasonSubscriptionInactive
		return out
	}
	if included == Unlimited || !limit.HardLimit || in.Used+in.Quantity <= included {
		out.Allowed = true
		return out
	}
	out.Reason = ReasonLimitReached
	return out
}
