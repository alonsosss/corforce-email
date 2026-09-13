package domain

// SuppressionCause es una causa de exclusion tal como la publica suppression.
type SuppressionCause string

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

// HasCause indica si la causa esta entre las vigentes.
func HasCause(causes []SuppressionCause, cause SuppressionCause) bool {
	for _, c := range causes {
		if c == cause {
			return true
		}
	}
	return false
}

// ReconcileSuppression deja el estado del contacto de acuerdo con las causas vigentes de
// su direccion en suppression (causes, leidas despues del cambio y en cualquier orden):
//
//   - una queja o un rebote vigentes fijan el estado de la mas grave, tambien a la baja:
//     retirar la queja con un rebote vigente deja bounced;
//   - una baja vigente sin queja ni rebote deja unsubscribed a quien estaba en un estado
//     mas grave, pero a quien esta active solo lo pasa la baja que se acaba de registrar
//     (unsubscribeRegistered): un active con la baja aun vigente es quien reconsintio y
//     espera a que suppression la retire al recibir contacts.contact.resubscribed;
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
