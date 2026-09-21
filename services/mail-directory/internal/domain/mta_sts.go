package domain

import (
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MTASTSMode es el modo de una politica MTA-STS (RFC 8461, 3.2). none no publica ninguna
// politica; testing la publica sin que los remitentes dejen de entregar si el TLS falla; enforce
// hace que no entreguen.
type MTASTSMode string

const (
	MTASTSNone    MTASTSMode = "none"
	MTASTSTesting MTASTSMode = "testing"
	MTASTSEnforce MTASTSMode = "enforce"
)

// Un remitente conserva la politica max_age segundos. testing es corto para poder corregir; enforce
// es de una semana, el orden de magnitud que recomienda el RFC para no reconsultar en cada entrega.
const (
	MTASTSTestingMaxAge = 24 * 60 * 60
	MTASTSEnforceMaxAge = 7 * 24 * 60 * 60
)

// ParseMTASTSMode valida el modo que llega del cliente.
func ParseMTASTSMode(raw string) (MTASTSMode, error) {
	switch m := MTASTSMode(strings.ToLower(strings.TrimSpace(raw))); m {
	case MTASTSNone, MTASTSTesting, MTASTSEnforce:
		return m, nil
	}
	return "", ErrInvalidMTASTSMode
}

// Published dice si el modo se sirve a los remitentes.
func (m MTASTSMode) Published() bool { return m != MTASTSNone }

// MaxAge es lo que un remitente conserva la politica en ese modo.
func (m MTASTSMode) MaxAge() int {
	if m == MTASTSEnforce {
		return MTASTSEnforceMaxAge
	}
	return MTASTSTestingMaxAge
}

// MTASTSPolicy es la fila de un dominio con politica.
type MTASTSPolicy struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Domain    string
	Mode      MTASTSMode
	MaxAge    int
	PolicyID  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewMTASTSPolicy crea la politica de un dominio en el modo dado, con version nueva.
func NewMTASTSPolicy(tenantID uuid.UUID, name string, mode MTASTSMode) *MTASTSPolicy {
	return &MTASTSPolicy{
		ID: uuid.New(), TenantID: tenantID, Domain: name, Mode: mode, MaxAge: mode.MaxAge(), PolicyID: NewMTASTSPolicyID(),
	}
}

// Change pasa la politica a otro modo con una version nueva: el TXT que la anuncia cambia y los
// remitentes vuelven a pedirla en vez de seguir con la que tenian en cache.
func (p *MTASTSPolicy) Change(mode MTASTSMode) {
	p.Mode = mode
	p.MaxAge = mode.MaxAge()
	p.PolicyID = NewMTASTSPolicyID()
}

// NewMTASTSPolicyID es un identificador de version aleatorio de 32 caracteres hexadecimales, el
// maximo que admite el id de _mta-sts (RFC 8461, 3.1). Aleatorio y no derivado de la hora: dos
// cambios en el mismo instante no pueden compartir version.
func NewMTASTSPolicyID() string { return strings.ReplaceAll(uuid.NewString(), "-", "") }

// MTASTSState es lo que se muestra de un dominio: su modo (none si no tiene politica) y con que
// version se anuncia.
type MTASTSState struct {
	Domain string `json:"domain"`
	// DomainActive dice si el dominio esta verificado y activo en el directorio: solo asi se
	// publica su politica y solo asi se admite enforce.
	DomainActive bool       `json:"domain_active"`
	Mode         MTASTSMode `json:"mode"`
	MaxAge       int        `json:"max_age"`
	PolicyID     string     `json:"policy_id"`
	UpdatedAt    *time.Time `json:"updated_at"`
	// AllowedModes son los modos a los que puede pasar ahora: la interfaz ofrece solo esos.
	AllowedModes []MTASTSMode `json:"allowed_modes"`
}

// NextModes son los modos a los que puede pasar el dominio: los que permite MTASTSTransition y,
// para enforce, solo con el dominio activo. Que sus MX esten bien se comprueba al pedirlo.
func (s MTASTSState) NextModes() []MTASTSMode {
	next := []MTASTSMode{}
	for _, m := range []MTASTSMode{MTASTSNone, MTASTSTesting, MTASTSEnforce} {
		if m == s.Mode || MTASTSTransition(s.Mode, m) != nil || (m == MTASTSEnforce && !s.DomainActive) {
			continue
		}
		next = append(next, m)
	}
	return next
}

// MTASTSTransition dice si la politica puede pasar de un modo a otro. Se entra por testing y se
// sale por testing: un remitente que guardo enforce lo sigue aplicando hasta que vence su cache,
// asi que apagar una politica endurecida de golpe deja a esos remitentes con una politica que ya
// no se sirve. testing no bloquea entregas y sustituye a la anterior en cuanto cambia su version.
func MTASTSTransition(from, to MTASTSMode) error {
	if from == to || from == MTASTSTesting || to == MTASTSTesting {
		return nil
	}
	return ErrMTASTSTransition
}

// MXMatchesPlatform dice si TODOS los MX publicados del dominio son el de la plataforma y hay al
// menos uno. Con enforce los remitentes solo entregan a los MX de la politica: un MX propio del
// cliente que no este en ella se quedaria sin correo.
func MXMatchesPlatform(published []string, platformMX string) bool {
	want := normalizeDNSName(platformMX)
	if want == "" || len(published) == 0 {
		return false
	}
	for _, mx := range published {
		if normalizeDNSName(mx) != want {
			return false
		}
	}
	return true
}

// normalizeDNSName compara nombres DNS sin distinguir mayusculas ni el punto final que devuelven
// los resolvedores.
func normalizeDNSName(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// Body es el documento que sirve https://mta-sts.<dominio>/.well-known/mta-sts.txt (RFC 8461,
// 3.2): claves separadas por CRLF y el MX de la plataforma como unico mx autorizado.
func (p MTASTSPolicy) Body(platformMX string) string {
	return "version: STSv1\r\n" +
		"mode: " + string(p.Mode) + "\r\n" +
		"mx: " + normalizeDNSName(platformMX) + "\r\n" +
		"max_age: " + strconv.Itoa(p.MaxAge) + "\r\n"
}
