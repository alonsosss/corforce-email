package domain

// Motivos de denegacion por el derecho mensual cuando billing no da uno propio.
const (
	ReasonPlanDenied        = "plan_denied"
	ReasonPlanLimitExceeded = "plan_limit_exceeded"
)

// Entitlement es la respuesta de billing sobre el derecho mensual de una clase.
type Entitlement struct {
	Allowed bool
	// Limit y Remaining son nil cuando el plan no limita el recurso.
	Limit     *int64
	Used      int64
	Remaining *int64
	HardLimit bool
	Reason    string
	// Requested es la cantidad con la que se consulto a billing.
	Requested int64
}

// Admits decide si caben n mensajes mas, descontando los consumed que este proceso ya
// autorizo desde que billing respondio. La respuesta se reutiliza un tiempo corto para no
// consultar a billing en cada envio; por eso la decision se rehace con la cantidad de cada
// peticion en vez de copiar el allowed de la consulta original. Una denegacion que no se
// explica por el remanente (plan suspendido, sin suscripcion) se mantiene para cualquier n.
func (e Entitlement) Admits(n, consumed int64) (bool, string) {
	quota := e.HardLimit && e.Remaining != nil
	if !e.Allowed && !(quota && *e.Remaining < e.Requested) {
		return false, orDefault(e.Reason, ReasonPlanDenied)
	}
	if quota && *e.Remaining-consumed < n {
		return false, orDefault(e.Reason, ReasonPlanLimitExceeded)
	}
	return true, ""
}

// MonthlyUsage es el uso del derecho mensual tal como se informa al llamador.
type MonthlyUsage struct {
	Limit     *int64
	Used      int64
	Remaining *int64
}

// View es el uso mensual despues de los consumed autorizados localmente.
func (e Entitlement) View(consumed int64) MonthlyUsage {
	m := MonthlyUsage{Limit: e.Limit, Used: e.Used + consumed}
	if e.Remaining != nil {
		r := *e.Remaining - consumed
		if r < 0 {
			r = 0
		}
		m.Remaining = &r
	}
	return m
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
