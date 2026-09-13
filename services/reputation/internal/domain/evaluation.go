package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Motivos de estado que escribe la evaluacion automatica.
const (
	ReasonInitial            = "initial"
	ReasonInsufficientVolume = "insufficient_volume"
	ReasonWithinThresholds   = "within_thresholds"
	ReasonBounceWarn         = "bounce_rate_warn"
	ReasonBounceBlock        = "bounce_rate_block"
	ReasonComplaintWarn      = "complaint_rate_warn"
	ReasonComplaintBlock     = "complaint_rate_block"
)

// EvaluationReasons son los motivos que escribe la evaluacion automatica.
func EvaluationReasons() []string {
	return []string{
		ReasonInitial, ReasonInsufficientVolume, ReasonWithinThresholds,
		ReasonBounceWarn, ReasonBounceBlock, ReasonComplaintWarn, ReasonComplaintBlock,
	}
}

// RateScale son los decimales con los que una tasa se calcula, se guarda (numeric(9,6)) y
// se compara: la tasa que decide un estado es exactamente la que se muestra.
const RateScale = 6

// Counts son los envios, rebotes permanentes y quejas acumulados en un periodo.
type Counts struct {
	Sent       int64
	Bounced    int64
	Complained int64
}

// Rate es events/sent con RateScale decimales. Sin envios la tasa es cero: no hay base
// sobre la que medir. Se acota a 1 porque un rebote puede entrar en la ventana por un
// envio que ya salio de ella, y una tasa por encima del 100 % no describe nada.
func Rate(events, sent int64) decimal.Decimal {
	if sent <= 0 || events <= 0 {
		return decimal.Zero
	}
	r := decimal.NewFromInt(events).DivRound(decimal.NewFromInt(sent), RateScale)
	if r.GreaterThan(one) {
		return one
	}
	return r
}

// Verdict es lo que dice la regla sobre una clase con los numeros de su ventana.
type Verdict struct {
	State         State
	Reason        string
	BounceRate    decimal.Decimal
	ComplaintRate decimal.Decimal
}

// Evaluate aplica la regla. La queja se comprueba antes que el rebote porque es la senal
// con la que los grandes buzones deciden bloquear a un remitente.
func Evaluate(class Class, c Counts, base Thresholds) Verdict {
	t := base.For(class)
	v := Verdict{
		BounceRate:    Rate(c.Bounced, c.Sent),
		ComplaintRate: Rate(c.Complained, c.Sent),
	}
	switch {
	case c.Sent < t.MinVolume:
		v.State, v.Reason = StateOK, ReasonInsufficientVolume
	case v.ComplaintRate.GreaterThanOrEqual(t.ComplaintBlock):
		v.State, v.Reason = StateRestricted, ReasonComplaintBlock
	case v.BounceRate.GreaterThanOrEqual(t.BounceBlock):
		v.State, v.Reason = StateRestricted, ReasonBounceBlock
	case v.ComplaintRate.GreaterThanOrEqual(t.ComplaintWarn):
		v.State, v.Reason = StateWarning, ReasonComplaintWarn
	case v.BounceRate.GreaterThanOrEqual(t.BounceWarn):
		v.State, v.Reason = StateWarning, ReasonBounceWarn
	default:
		v.State, v.Reason = StateOK, ReasonWithinThresholds
	}
	return v
}

// Transition decide si un veredicto cambia el estado vigente. Un estado fijado a mano no
// lo toca la evaluacion: solo el superadmin lo libera. Si el veredicto repite el estado no
// hay cambio, y el motivo y las tasas guardados siguen siendo los del ultimo cambio.
func Transition(cur Record, v Verdict, now time.Time) (Record, bool) {
	if cur.Manual || cur.State == StateSuspended || cur.State == v.State {
		return cur, false
	}
	next := cur
	next.State, next.Reason = v.State, v.Reason
	next.BounceRate, next.ComplaintRate = v.BounceRate, v.ComplaintRate
	next.Manual, next.ChangedBy, next.ChangedAt = false, nil, now
	return next, true
}

// Suspend fija la suspension manual de la clase. Suspender lo ya suspendido no cambia nada.
func Suspend(cur Record, reason string, v Verdict, by uuid.UUID, now time.Time) (Record, bool) {
	if cur.State == StateSuspended && cur.Manual {
		return cur, false
	}
	next := cur
	next.State, next.Reason = StateSuspended, reason
	next.BounceRate, next.ComplaintRate = v.BounceRate, v.ComplaintRate
	next.Manual, next.ChangedBy, next.ChangedAt = true, &by, now
	return next, true
}

// Release retira la marca manual y deja la clase en lo que diga la evaluacion. Si no habia
// marca y la evaluacion repite el estado, no hay cambio.
func Release(cur Record, v Verdict, by uuid.UUID, now time.Time) (Record, bool) {
	if !cur.Manual && cur.State == v.State {
		return cur, false
	}
	next := cur
	next.State, next.Reason = v.State, v.Reason
	next.BounceRate, next.ComplaintRate = v.BounceRate, v.ComplaintRate
	next.Manual, next.ChangedBy, next.ChangedAt = false, &by, now
	return next, true
}
