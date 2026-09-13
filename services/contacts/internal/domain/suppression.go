package domain

import "time"

// SuppressionCause es una causa de exclusion tal como la publica suppression.
type SuppressionCause string

// ActiveCause es una causa vigente de la direccion en suppression y la hora de alta de su
// fila. RegisteredAt es cero si suppression no la publica (una replica anterior a causes).
type ActiveCause struct {
	Cause        SuppressionCause
	RegisteredAt time.Time
}

// CausesOf devuelve las causas, en el mismo orden, sin sus horas.
func CausesOf(active []ActiveCause) []SuppressionCause {
	out := make([]SuppressionCause, len(active))
	for i, a := range active {
		out[i] = a.Cause
	}
	return out
}

// CauseRegisteredAt devuelve la hora de alta de la causa y si esta entre las vigentes.
func CauseRegisteredAt(active []ActiveCause, cause SuppressionCause) (time.Time, bool) {
	for _, a := range active {
		if a.Cause == cause {
			return a.RegisteredAt, true
		}
	}
	return time.Time{}, false
}

// UnsubscribeRevokes decide si una baja vigente, dada de alta en suppression a la hora
// unsubscribedAt, revoca el consentimiento de un contacto con este historial (consents).
//
// La revoca salvo que la persona haya vuelto a consentir DESPUES de esa hora con una
// prueba de que lo pidio ella (provesOwnRequest): esa baja ya la levanto el nuevo
// consentimiento y solo espera a que suppression la retire al recibir
// contacts.contact.resubscribed. Un consentimiento posterior por API o importacion no la
// levanta: CheckGrant lo habria rechazado si contacts hubiera conocido la baja a tiempo.
//
// Empate (misma hora): revoca, porque de los dos errores posibles solo uno se corrige sin
// dano (la persona vuelve a confirmar); el otro es enviar publicidad a quien se fue. Sin
// hora (suppression anterior a causes): revoca, como antes de esta regla. Las dos horas
// las pone el mismo servidor, el de la base de la empresa que guarda los dos esquemas
// (created_at de suppression.entries y occurred_at de contacts.consents), asi que no hay
// deriva de relojes que tolerar.
func UnsubscribeRevokes(unsubscribedAt time.Time, consents []Consent) bool {
	if unsubscribedAt.IsZero() {
		return true
	}
	for _, c := range consents {
		if c.Purpose == PurposeMarketing && c.Status == ConsentGranted &&
			provesOwnRequest(c.Method, c.IP) && c.OccurredAt.After(unsubscribedAt) {
			return false
		}
	}
	return true
}

const (
	CauseComplaint   SuppressionCause = "complaint"
	CauseHardBounce  SuppressionCause = "hard_bounce"
	CauseUnsubscribe SuppressionCause = "unsubscribe"
	CauseInvalid     SuppressionCause = "invalid"
	CauseManual      SuppressionCause = "manual"
)

// StatusForCause es el estado del contacto que implica cada causa de suppression y la
// unica copia de ese catalogo en este servicio: un test la contrasta con el orden de
// gravedad de services/suppression/internal/domain. manual e invalid no dicen nada de la
// persona y no implican estado (status vacio). known=false es una causa que este
// servicio aun no conoce.
func StatusForCause(cause SuppressionCause) (status Status, known bool) {
	switch cause {
	case CauseComplaint:
		return StatusComplained, true
	case CauseHardBounce:
		return StatusBounced, true
	case CauseUnsubscribe:
		return StatusUnsubscribed, true
	case CauseInvalid, CauseManual:
		return "", true
	}
	return "", false
}

// ReconcileSuppression deja el estado del contacto de acuerdo con las causas vigentes de
// su direccion en suppression (causes, leidas despues del cambio y en cualquier orden):
//
//   - una queja o un rebote vigentes fijan el estado de la mas grave, tambien a la baja:
//     retirar la queja con un rebote vigente deja bounced;
//   - una baja vigente sin queja ni rebote deja unsubscribed a quien estaba en un estado
//     mas grave, pero a quien esta active solo lo pasa la baja que se acaba de registrar y
//     no es anterior a su ultimo reconsentimiento (unsubscribeRegistered, ver
//     UnsubscribeRevokes): un active con la baja aun vigente es quien reconsintio y espera
//     a que suppression la retire al recibir contacts.contact.resubscribed;
//   - sin ninguna causa vigente, bounced y complained vuelven a active; unsubscribed no,
//     porque una baja solo la levanta un consentimiento nuevo;
//   - manual, invalid o una causa desconocida no implican estado ni dejan volver a active:
//     la direccion sigue sin poder recibir nada.
//
// Devuelve si el estado cambio.
func (c *Contact) ReconcileSuppression(causes []SuppressionCause, unsubscribeRegistered bool) bool {
	var implied Status
	for _, cause := range causes {
		if st, _ := StatusForCause(cause); st.Severity() > implied.Severity() {
			implied = st
		}
	}
	next := c.Status
	switch {
	case implied == StatusComplained || implied == StatusBounced:
		next = implied
	case implied == StatusUnsubscribed:
		if c.Status != StatusActive || unsubscribeRegistered {
			next = StatusUnsubscribed
		}
	case len(causes) == 0 && (c.Status == StatusBounced || c.Status == StatusComplained):
		next = StatusActive
	}
	if next == c.Status {
		return false
	}
	c.Status = next
	return true
}
