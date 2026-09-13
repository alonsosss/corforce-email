package domain

import (
	"net"
	"strings"
)

const (
	// MaxConsentSource acota el origen declarado (url del formulario, id de importacion).
	MaxConsentSource = 500
	// MaxUserAgent acota el user agent guardado como evidencia.
	MaxUserAgent = 512
)

// GrantStatuses son los estados que una empresa puede registrar por API.
func GrantStatuses() []ConsentStatus { return []ConsentStatus{ConsentGranted, ConsentRevoked} }

// ParseGrantStatus valida el estado que una empresa puede registrar por API.
func ParseGrantStatus(s string) (ConsentStatus, error) {
	for _, st := range GrantStatuses() {
		if ConsentStatus(s) == st {
			return st, nil
		}
	}
	return "", ErrInvalidConsentStatus
}

// ParseAPIMethod valida el metodo que una empresa puede declarar por API: lo recogio su
// propio sistema (api) o un formulario suyo (form). El doble opt-in, la importacion, el
// enlace de baja y la supresion los registra la plataforma, nunca el cliente.
func ParseAPIMethod(s string) (ConsentMethod, error) {
	for _, m := range APIMethods() {
		if ConsentMethod(s) == m {
			return m, nil
		}
	}
	return "", ErrInvalidConsentMethod
}

// APIMethods son los metodos que una empresa puede declarar por API (ParseAPIMethod).
func APIMethods() []ConsentMethod { return []ConsentMethod{MethodAPI, MethodForm} }

// NormalizeIP valida la ip de la evidencia. Vacio = sin ip.
func NormalizeIP(raw string) (*string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, ErrInvalidIP
	}
	norm := ip.String()
	return &norm, nil
}

// NormalizeUserAgent recorta el user agent a su tope. Vacio = sin user agent.
func NormalizeUserAgent(raw string) *string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	s = truncate(strings.ToValidUTF8(s, ""), MaxUserAgent)
	return &s
}

// CheckGrant decide si se puede registrar un consentimiento concedido para el contacto.
//
// Quien se dio de baja (status unsubscribed) o retiro su consentimiento (vigente
// revoked) solo vuelve por una prueba de que lo pidio el mismo: el doble opt-in o un
// formulario con la ip de quien lo envio. Una empresa que re-suscribe por API a quien
// se fue es exactamente lo que la evidencia existe para impedir.
func CheckGrant(c *Contact, method ConsentMethod, ip *string) error {
	if c.Status != StatusUnsubscribed && c.ConsentStatus != ConsentRevoked {
		return nil
	}
	if method == MethodDoubleOptIn || (method == MethodForm && ip != nil) {
		return nil
	}
	return ErrResubscribeRequiresOptIn
}

// CheckConfirmationRequest decide si se puede pedir el doble opt-in. A una direccion que
// rebota o que se quejo no se le envia nada; a quien ya consintio no se le pide de nuevo,
// porque la fila pending lo sacaria de la audiencia hasta que confirmara.
func CheckConfirmationRequest(c *Contact) error {
	if c.Status == StatusBounced || c.Status == StatusComplained {
		return ErrContactNotReachable
	}
	if c.ConsentStatus == ConsentGranted {
		return ErrConsentAlreadyGranted
	}
	return nil
}

// Reactivate vuelve a active a quien se dio de baja tras un consentimiento concedido
// valido. Un rebote o una queja no se levantan por consentir: siguen siendo hechos de la
// direccion. Devuelve si hubo cambio.
func (c *Contact) Reactivate() bool {
	if c.Status != StatusUnsubscribed {
		return false
	}
	c.Status = StatusActive
	return true
}

// ImportMayGrant decide si una importacion con base legal declarada puede conceder el
// consentimiento a un contacto que ya existia. Nunca re-suscribe (baja, rebote, queja o
// consentimiento retirado) y no duplica uno ya concedido.
func ImportMayGrant(c *Contact) bool {
	return c.Status == StatusActive && c.ConsentStatus != ConsentRevoked && c.ConsentStatus != ConsentGranted
}

// ApplySuppression aplica al contacto el estado que implica una exclusion de envio, sin
// degradar uno mas grave. Devuelve si el estado cambio.
func (c *Contact) ApplySuppression(target Status) bool {
	if target.Severity() <= c.Status.Severity() {
		return false
	}
	c.Status = target
	return true
}

// LiftSuppression vuelve a active cuando se retira la exclusion que explica el estado
// actual (un rebote o una queja reactivados por un operador). Devuelve si cambio.
func (c *Contact) LiftSuppression(lifted Status) bool {
	if c.Status != lifted || (lifted != StatusBounced && lifted != StatusComplained) {
		return false
	}
	c.Status = StatusActive
	return true
}
