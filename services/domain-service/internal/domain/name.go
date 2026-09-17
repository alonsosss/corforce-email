package domain

import (
	"net"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// maxDomainLength es el limite de un nombre DNS completo (RFC 1035).
const maxDomainLength = 253

var (
	labelRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	tldRegex   = regexp.MustCompile(`^[a-z]{2,63}$`)
)

// NormalizeDomainName deja el nombre como se guarda: minusculas, sin espacios y sin el
// punto final que algunos clientes copian de su zona.
func NormalizeDomainName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// ValidateDomainName acepta solo nombres DNS reales de al menos dos etiquetas, en ASCII
// (los IDN llegan ya en punycode), y rechaza direcciones IP. platformHostname es el
// nombre de la plataforma (MAIL_HOSTNAME): ni el ni sus subdominios ni su dominio
// registrable pueden darse de alta como dominio de una empresa. Tampoco un sufijo publico
// (com.pe, co.uk, github.io): no lo registra ningun titular y verificarlo lo reservaria para
// una sola empresa.
func ValidateDomainName(name, platformHostname string) error {
	if err := validateDNSName(name); err != nil {
		return err
	}
	if isPublicSuffix(name) {
		return ErrPublicSuffixDomain
	}
	if isPlatformDomain(name, NormalizeDomainName(platformHostname)) {
		return ErrPlatformDomain
	}
	return nil
}

// ValidatePlatformHostname comprueba al arrancar que MAIL_HOSTNAME es un nombre DNS valido
// con dominio registrable propio. Un hostname que es a su vez un sufijo publico (com.pe,
// co.uk, github.io) no identifica a la plataforma: protegerlo con sus subdominios
// bloquearia el sufijo entero para todas las empresas, y el servicio no arranca con el.
func ValidatePlatformHostname(hostname string) error {
	host := NormalizeDomainName(hostname)
	if err := validateDNSName(host); err != nil {
		return ErrInvalidPlatformHostname
	}
	if registrableDomain(host) == "" {
		return ErrInvalidPlatformHostname
	}
	return nil
}

// registrableDomain es el dominio que un titular registra (eTLD+1) segun la Public Suffix
// List, incluida su seccion privada: mail.plataforma.com.pe da plataforma.com.pe. Devuelve
// "" si el nombre es un sufijo publico o no se puede derivar.
func registrableDomain(host string) string {
	base, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return ""
	}
	return base
}

// isPublicSuffix dice si el nombre es entero un sufijo de la Public Suffix List, ICANN o
// privado. Un TLD fuera de la lista (test, example) solo cuenta como sufijo en su etiqueta
// final por la regla por defecto: cfm.test no lo es.
func isPublicSuffix(name string) bool {
	suffix, _ := publicsuffix.PublicSuffix(name)
	return suffix == name
}

func validateDNSName(name string) error {
	if name == "" || len(name) > maxDomainLength || name != NormalizeDomainName(name) {
		return ErrInvalidDomainName
	}
	if net.ParseIP(name) != nil {
		return ErrInvalidDomainName
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return ErrInvalidDomainName
	}
	for _, label := range labels {
		if !labelRegex.MatchString(label) {
			return ErrInvalidDomainName
		}
	}
	if !tldRegex.MatchString(labels[len(labels)-1]) {
		return ErrInvalidDomainName
	}
	return nil
}

// isPlatformDomain compara contra el hostname de la plataforma y contra su dominio
// registrable (mail.ejemplo.com.pe protege tambien ejemplo.com.pe y *.ejemplo.com.pe): un
// cliente que registrara el dominio base podria publicar SPF o DKIM que la plataforma
// firma en su nombre. Si el hostname no tiene dominio registrable (sufijo publico, lo que
// ValidatePlatformHostname impide al arrancar) solo se protege el nombre exacto: sus
// subdominios son dominios de terceros.
func isPlatformDomain(name, platform string) bool {
	if platform == "" {
		return false
	}
	if name == platform {
		return true
	}
	base := registrableDomain(platform)
	return base != "" && (name == base || strings.HasSuffix(name, "."+base))
}
