package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// SESIdentityStatus resume lo que Amazon SES dice de la identidad del dominio. Solo verified permite
// enviar: es VerifiedForSendingStatus de GetEmailIdentity.
type SESIdentityStatus string

const (
	SESIdentityPending  SESIdentityStatus = "pending"
	SESIdentityVerified SESIdentityStatus = "verified"
	SESIdentityFailed   SESIdentityStatus = "failed"
)

// SESCheckStatus es el estado de una comprobacion de SES (DKIM o MAIL FROM), en minusculas.
type SESCheckStatus string

const (
	SESCheckPending          SESCheckStatus = "pending"
	SESCheckSuccess          SESCheckStatus = "success"
	SESCheckFailed           SESCheckStatus = "failed"
	SESCheckTemporaryFailure SESCheckStatus = "temporary_failure"
	SESCheckNotStarted       SESCheckStatus = "not_started"
)

// ParseSESCheckStatus traduce el valor de SES (PENDING, SUCCESS...) a uno conocido. Un valor que SES
// anada en el futuro queda vacio: se guarda como desconocido en vez de romper la escritura.
func ParseSESCheckStatus(raw string) SESCheckStatus {
	switch s := SESCheckStatus(strings.ToLower(strings.TrimSpace(raw))); s {
	case SESCheckPending, SESCheckSuccess, SESCheckFailed, SESCheckTemporaryFailure, SESCheckNotStarted:
		return s
	}
	return ""
}

// SESDKIMOriginExternal es el origen de las claves DKIM que SES firma con la clave que custodia
// domain-service (BYODKIM).
const SESDKIMOriginExternal = "EXTERNAL"

// MaxSESErrorLength acota el ultimo error de SES que se guarda (caracteres).
const MaxSESErrorLength = 500

// SESState es el estado de la identidad del dominio en SES tal como lo vio la ultima comprobacion.
// IdentityStatus vacio: el dominio no tiene identidad en SES (o nunca se comprobo).
type SESState struct {
	IdentityStatus SESIdentityStatus
	DKIMStatus     SESCheckStatus
	MailFromStatus SESCheckStatus
	CheckedAt      *time.Time
	// LastError es el ultimo fallo al sincronizar con SES; vacio si la ultima sincronizacion fue bien.
	LastError string
}

// VerifiedForSending dice si SES acepta envios desde el dominio.
func (s SESState) VerifiedForSending() bool { return s.IdentityStatus == SESIdentityVerified }

// Checked dice si SES se consulto alguna vez para este dominio.
func (s SESState) Checked() bool { return s.CheckedAt != nil }

// SESIdentityObservation es lo que GetEmailIdentity devuelve de una identidad, sin tipos del SDK.
type SESIdentityObservation struct {
	VerifiedForSending bool
	// DKIMOrigin es EXTERNAL (BYODKIM) o AWS_SES (Easy DKIM).
	DKIMOrigin string
	// DKIMSelectors son los selectores que SES firma; con BYODKIM, el que se le dio.
	DKIMSelectors  []string
	DKIMStatus     SESCheckStatus
	MailFromDomain string
	MailFromStatus SESCheckStatus
	// BehaviorOnMXFailure es USE_DEFAULT_VALUE o REJECT_MESSAGE.
	BehaviorOnMXFailure string
	ConfigurationSet    string
	// TenantTag es la etiqueta SESTenantTag de la identidad; vacia en una creada a mano.
	TenantTag string
}

// SESTenantTag es la etiqueta con la que domain-service marca la identidad de SES con la empresa
// duena del dominio. Las identidades son de la cuenta de SES, no de una empresa: sin ella, la baja del
// dominio en una empresa borraria la identidad con la que envia otra.
const SESTenantTag = "cfm_tenant_id"

// OwnedBy dice si la identidad es de la empresa: lleva su etiqueta o no lleva ninguna (creada a mano
// por quien opera la cuenta, que se adopta).
func (o SESIdentityObservation) OwnedBy(tenantID uuid.UUID) bool {
	return o.TenantTag == "" || strings.EqualFold(o.TenantTag, tenantID.String())
}

// SESBehaviorOnMXFailure es lo que SES hace si el MX del MAIL FROM falta: enviar con su propio MAIL
// FROM, nunca rechazar el mensaje.
const SESBehaviorOnMXFailure = "USE_DEFAULT_VALUE"

// SignsWith dice si SES firma el dominio con la clave BYODKIM de ese selector.
func (o SESIdentityObservation) SignsWith(selector string) bool {
	if o.DKIMOrigin != SESDKIMOriginExternal {
		return false
	}
	for _, s := range o.DKIMSelectors {
		if strings.EqualFold(s, selector) {
			return true
		}
	}
	return false
}

// State es el estado que se guarda a partir de una observacion.
func (o SESIdentityObservation) State(at time.Time) SESState {
	status := SESIdentityPending
	switch {
	case o.VerifiedForSending:
		status = SESIdentityVerified
	case o.DKIMStatus == SESCheckFailed:
		status = SESIdentityFailed
	}
	return SESState{IdentityStatus: status, DKIMStatus: o.DKIMStatus, MailFromStatus: o.MailFromStatus, CheckedAt: &at}
}

// TruncateSESError deja el mensaje de error en texto de una linea y acotado.
func TruncateSESError(msg string) string {
	msg = strings.Join(strings.Fields(strings.ToValidUTF8(msg, "")), " ")
	if utf8.RuneCountInString(msg) <= MaxSESErrorLength {
		return msg
	}
	return string([]rune(msg)[:MaxSESErrorLength])
}

// SESMailFromLabel es la etiqueta del subdominio MAIL FROM que se da a SES.
const SESMailFromLabel = "bounce"

// SESMailFromDomain es el subdominio MAIL FROM de SES para el dominio.
func SESMailFromDomain(name string) string { return SESMailFromLabel + "." + name }

// SESFeedbackHost es el MX de rebotes de SES de una region, el que exige el MAIL FROM propio.
func SESFeedbackHost(region string) string { return "feedback-smtp." + region + ".amazonses.com" }

// SESMailFromSPFValue es el SPF que SES pide para el subdominio MAIL FROM.
const SESMailFromSPFValue = "v=spf1 include:amazonses.com ~all"
