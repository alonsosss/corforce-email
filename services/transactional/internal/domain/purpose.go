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

// IgnoresSuppression dice si, con ese proposito, una exclusion por esa causa no bloquea.
func IgnoresSuppression(purpose, reason string) bool {
	return purpose == PurposeDoubleOptIn && reason == SuppressionReasonUnsubscribe
}
