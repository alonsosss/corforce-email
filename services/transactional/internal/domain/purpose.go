package domain

// PurposeDoubleOptIn es el unico proposito que admite el envio interno de mensajes
// (POST /internal/transactional/messages): el correo que pide confirmar el consentimiento
// de marketing, que envia automations. Quien se dio de baja y ahora pide volver necesita
// recibirlo, asi que con este proposito la supresion previa ignora SOLO las bajas
// voluntarias; rebotes duros, quejas, direcciones invalidas y exclusiones manuales siguen
// bloqueando. Todo lo demas (remitente verificado, reputacion transactional, plantilla
// transaccional) se aplica igual.
const PurposeDoubleOptIn = "double_opt_in"

// SuppressionReasonUnsubscribe es la causa con que suppression registra una baja pedida
// por la persona.
const SuppressionReasonUnsubscribe = "unsubscribe"

// ValidPurpose: sin proposito, o uno conocido.
func ValidPurpose(purpose string) bool {
	return purpose == "" || purpose == PurposeDoubleOptIn
}

// IgnoresSuppression dice si, con ese proposito, la exclusion de esa direccion no bloquea.
// Con el doble opt-in una direccion solo pasa si TODAS sus causas vigentes son una baja
// voluntaria: una exclusion manual, un rebote o una queja junto a la baja siguen
// bloqueando. Sin la lista de causas (suppression anterior a reasons, o una respuesta
// guardada antes) no se puede descartar otra causa detras de la baja, y se bloquea.
func IgnoresSuppression(purpose string, s SuppressedRecipient) bool {
	if purpose != PurposeDoubleOptIn || s.Reason != SuppressionReasonUnsubscribe || len(s.Reasons) == 0 {
		return false
	}
	for _, r := range s.Reasons {
		if r != SuppressionReasonUnsubscribe {
			return false
		}
	}
	return true
}
