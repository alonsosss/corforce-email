package domain

// Propositos que admite el envio interno de mensajes (POST /internal/transactional/messages).
// El API publico no acepta ninguno. Todo proposito exige un unico destinatario sin copias, y
// todo lo demas (remitente verificado, reputacion transactional, plantilla transaccional) se
// aplica igual que sin proposito.
const (
	// PurposeDoubleOptIn es el correo que pide confirmar el consentimiento de marketing, que
	// envia automations. Quien se dio de baja y ahora pide volver necesita recibirlo, asi que
	// con este proposito la supresion previa ignora SOLO las bajas voluntarias; rebotes duros,
	// quejas, direcciones invalidas y exclusiones manuales siguen bloqueando.
	PurposeDoubleOptIn = "double_opt_in"
	// PurposeQuarantineNotice es el aviso de cuarentena que mail-security envia al buzon con
	// la lista de mensajes retenidos. Clase transaccional con la supresion normal: un buzon
	// suprimido por cualquier causa no recibe el aviso.
	PurposeQuarantineNotice = "quarantine_notice"
)

// SuppressionReasonUnsubscribe es la causa con que suppression registra una baja pedida
// por la persona.
const SuppressionReasonUnsubscribe = "unsubscribe"

// Purposes enumera los propositos validos.
func Purposes() []string {
	return []string{PurposeDoubleOptIn, PurposeQuarantineNotice}
}

// ValidPurpose: sin proposito, o uno conocido.
func ValidPurpose(purpose string) bool {
	if purpose == "" {
		return true
	}
	for _, p := range Purposes() {
		if purpose == p {
			return true
		}
	}
	return false
}

// IgnoresSuppression dice si, con ese proposito, la exclusion de esa direccion no bloquea.
// Solo el doble opt-in relaja la supresion, y solo si TODAS las causas vigentes de la
// direccion son una baja voluntaria: una exclusion manual, un rebote o una queja junto a la
// baja siguen bloqueando. Sin la lista de causas (suppression anterior a reasons, o una
// respuesta guardada antes) no se puede descartar otra causa detras de la baja, y se bloquea.
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
