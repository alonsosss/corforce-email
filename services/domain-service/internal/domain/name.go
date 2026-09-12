package domain

import (
	"net"
	"regexp"
	"strings"
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
// nombre de la plataforma (MAIL_HOSTNAME): ni el ni sus subdominios ni su dominio base
// pueden darse de alta como dominio de una empresa.
func ValidateDomainName(name, platformHostname string) error {
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
	if isPlatformDomain(name, NormalizeDomainName(platformHostname)) {
		return ErrPlatformDomain
	}
	return nil
}

// isPlatformDomain compara contra el hostname de la plataforma y contra su dominio
// registrable (mail.ejemplo.com protege tambien ejemplo.com y *.ejemplo.com): un
// cliente que registrara el dominio base podria publicar SPF o DKIM que la plataforma
// firma en su nombre.
func isPlatformDomain(name, platform string) bool {
	if platform == "" {
		return false
	}
	if name == platform || strings.HasSuffix(name, "."+platform) {
		return true
	}
	base := registrableDomain(platform)
	return base != "" && (name == base || strings.HasSuffix(name, "."+base))
}

// registrableDomain reduce un hostname a sus dos ultimas etiquetas. No consulta la lista
// de sufijos publicos: para el hostname de la plataforma, que es propio y conocido, la
// aproximacion basta y evita una dependencia.
func registrableDomain(host string) string {
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return ""
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
