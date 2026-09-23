package domain

import (
	"regexp"
	"strings"
	"time"
)

const (
	MinPasswordLength = 12
	MaxPasswordLength = 256
	// MaxSieveScriptBytes acota lo que Dovecot compila por buzon.
	MaxSieveScriptBytes  = 64 * 1024
	MaxDomainLength      = 253
	MaxLabelLength       = 63
	MaxLocalPartLength   = 64
	MaxDisplayNameLength = 255
	MaxDescriptionLength = 255
	// MaxSearchLength acota el texto de busqueda de los listados: acaba en un ILIKE por
	// cada fila de la empresa.
	MaxSearchLength = 100

	// Unlimited es el valor que significa "sin limite" en cuotas (bytes) y en los maximos
	// de buzones y aliases de un dominio. Postfix y Dovecot lo leen asi de la celda.
	Unlimited = 0
	// QuotaUnit es la unidad de todas las cuotas del directorio.
	QuotaUnit = "bytes"
)

var (
	dnsLabelRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	tldRegex      = regexp.MustCompile(`^[a-z]{2,}$`)
	// localPartRegex es el subconjunto de RFC 5321 que se admite para buzones propios.
	localPartRegex = regexp.MustCompile(`^[a-z0-9._+-]+$`)
	// externalLocalRegex es mas amplio: un destino externo puede llevar caracteres que
	// no se permiten al crear un buzon propio.
	externalLocalRegex = regexp.MustCompile("^[a-z0-9!#$%&'*+/=?^_`{|}~.-]+$")
	hostnameRegex      = regexp.MustCompile(`^(?:\[[a-f0-9.:]+\]|[a-z0-9.-]+)(?::[0-9]{1,5})?$`)
)

// NormalizeDomain deja el nombre en minusculas, sin espacios ni punto final, y lo valida
// como nombre DNS: etiquetas de 1 a 63 caracteres, al menos dos, TLD alfabetico.
func NormalizeDomain(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > MaxDomainLength {
		return "", ErrInvalidDomainName
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", ErrInvalidDomainName
	}
	for _, label := range labels {
		if len(label) > MaxLabelLength || !dnsLabelRegex.MatchString(label) {
			return "", ErrInvalidDomainName
		}
	}
	if !tldRegex.MatchString(labels[len(labels)-1]) {
		return "", ErrInvalidDomainName
	}
	return name, nil
}

// NormalizeLocalPart valida la parte local de un buzon propio: minusculas, sin puntos
// consecutivos ni en los extremos.
func NormalizeLocalPart(raw string) (string, error) {
	local := strings.ToLower(strings.TrimSpace(raw))
	if local == "" || len(local) > MaxLocalPartLength || !localPartRegex.MatchString(local) {
		return "", ErrInvalidLocalPart
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return "", ErrInvalidLocalPart
	}
	return local, nil
}

// NormalizeEmail valida una direccion completa (propia o externa) y la devuelve en
// minusculas junto con su dominio.
func NormalizeEmail(raw string) (address, domainPart string, err error) {
	addr := strings.ToLower(strings.TrimSpace(raw))
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "", "", ErrInvalidEmail
	}
	local, dom := addr[:at], addr[at+1:]
	if len(local) > MaxLocalPartLength || !externalLocalRegex.MatchString(local) {
		return "", "", ErrInvalidEmail
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return "", "", ErrInvalidEmail
	}
	dom, err = NormalizeDomain(dom)
	if err != nil {
		return "", "", ErrInvalidEmail
	}
	return local + "@" + dom, dom, nil
}

// NormalizeAddress admite una direccion completa o '@dominio' (catch-all). Devuelve la
// direccion normalizada y su dominio.
func NormalizeAddress(raw string) (address, domainPart string, err error) {
	addr := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(addr, "@") {
		dom, err := NormalizeDomain(addr[1:])
		if err != nil {
			return "", "", ErrInvalidAddress
		}
		return "@" + dom, dom, nil
	}
	address, domainPart, err = NormalizeEmail(addr)
	if err != nil {
		return "", "", ErrInvalidAddress
	}
	return address, domainPart, nil
}

// NormalizeGoto convierte la lista de destinos en la forma que entiende
// virtual_alias_maps: minusculas, sin espacios, separada por comas, sin duplicados.
func NormalizeGoto(raw string) (string, error) {
	seen := make(map[string]struct{})
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addr, _, err := NormalizeEmail(part)
		if err != nil {
			return "", ErrInvalidGoto
		}
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	if len(out) == 0 {
		return "", ErrInvalidGoto
	}
	return strings.Join(out, ","), nil
}

func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if len(password) > MaxPasswordLength {
		return ErrPasswordTooLong
	}
	return nil
}

func ValidateActive(active int) error {
	if active < ActiveOff || active > ActiveReceiveOnly {
		return ErrInvalidActive
	}
	return nil
}

func ValidateKind(kind string) error {
	for _, k := range mailboxKinds {
		if k == kind {
			return nil
		}
	}
	return ErrInvalidKind
}

func ValidateTLSPolicy(policy string) error {
	for _, p := range tlsPolicies {
		if p == policy {
			return nil
		}
	}
	return ErrInvalidTLSPolicy
}

func ValidateBCCType(t string) error {
	if t == BCCTypeSender || t == BCCTypeRcpt {
		return nil
	}
	return ErrInvalidBCCType
}

// NormalizeHostname acepta host, host:puerto o [host]:puerto en minusculas.
func NormalizeHostname(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" || len(host) > 255 || !hostnameRegex.MatchString(host) {
		return "", ErrInvalidHostname
	}
	return host, nil
}

func ValidateSieveScript(script string) error {
	if strings.TrimSpace(script) == "" {
		return ErrSieveEmpty
	}
	if len(script) > MaxSieveScriptBytes {
		return ErrSieveTooLarge
	}
	return nil
}

// ValidateSpamAliasValidity: un alias temporal caduca o es permanente, nunca ninguna de
// las dos cosas.
func ValidateSpamAliasValidity(validUntil *time.Time, permanent bool) error {
	if permanent || validUntil != nil {
		return nil
	}
	return ErrValidityRequired
}

// DomainLimits agrupa los limites de un dominio que condicionan sus buzones.
type DomainLimits struct {
	MaxAliases        int
	MaxMailboxes      int
	DefaultQuotaBytes int64
	MaxQuotaBytes     int64
	QuotaBytes        int64
}

func (l DomainLimits) Validate() error {
	if l.MaxAliases < 0 || l.MaxMailboxes < 0 || l.DefaultQuotaBytes < 0 || l.MaxQuotaBytes < 0 || l.QuotaBytes < 0 {
		return ErrInvalidLimit
	}
	if l.MaxQuotaBytes > 0 && l.DefaultQuotaBytes > l.MaxQuotaBytes {
		return ErrQuotaExceedsMax
	}
	return nil
}

// CheckMailboxQuota aplica las reglas de cuota del dominio a un buzon: la cuota pedida
// (0 = ilimitada) no supera max_quota_bytes cuando este es > 0, y la suma de cuotas de
// los buzones del dominio (excluido el que se edita) no supera quota_bytes cuando es > 0.
// Un buzon ilimitado no cabe en un dominio con cuota acotada.
func CheckMailboxQuota(requested int64, limits DomainLimits, usedByOthers int64) error {
	if requested < 0 {
		return ErrInvalidLimit
	}
	if limits.MaxQuotaBytes > Unlimited && (requested == Unlimited || requested > limits.MaxQuotaBytes) {
		return ErrQuotaExceedsMax
	}
	if limits.QuotaBytes > Unlimited && (requested == Unlimited || usedByOthers+requested > limits.QuotaBytes) {
		return ErrDomainQuotaExceeded
	}
	return nil
}

// CheckLimit comprueba un maximo (Unlimited = sin limite) contra lo ya existente.
func CheckLimit(max int, current int64, exceeded error) error {
	if max > Unlimited && current >= int64(max) {
		return exceeded
	}
	return nil
}

// PlanLimit es lo que el plan de la empresa incluye de un recurso. Unknown vale por "no hay
// plan que aplicar" (billing no respondio, la empresa no tiene plan o el plan no fija el
// recurso) y no restringe nada: el limite del dominio sigue aplicandose igual.
type PlanLimit struct {
	Included  int64
	HardLimit bool
	Unknown   bool
	// SubscriptionInactive: la empresa esta dada de baja o suspendida. No crece, sea cual
	// sea Included: quien se da de baja no sigue consumiendo.
	SubscriptionInactive bool
}

// CheckPlanLimit aplica el limite del plan a lo que quedaria tras el cambio. No restringe
// cuando no hay plan que aplicar, cuando el plan no limita el recurso (Included negativo,
// el Unlimited de billing) o cuando el limite es blando, que en billing significa que se
// puede exceder y se factura el exceso. resulting es el total DESPUES del cambio.
func CheckPlanLimit(resulting int64, limit PlanLimit, exceeded error) error {
	if limit.SubscriptionInactive {
		return ErrSubscriptionInactive
	}
	if limit.Unknown || limit.Included < 0 || !limit.HardLimit {
		return nil
	}
	if resulting > limit.Included {
		return exceeded
	}
	return nil
}
