// Package mailcell reune los formatos que comparten el plano de control, el gateway y los
// servicios de celda: el codigo de una celda y el nombre de un dominio de correo. Solo usa la
// biblioteca estandar, asi que lo pueden usar tambien los paquetes de dominio.
package mailcell

import (
	"regexp"
	"strings"
)

// MaxCodeLen es la longitud maxima de un codigo de celda.
const MaxCodeLen = 63

// MaxDomainLen es la longitud maxima de un nombre DNS completo (RFC 1035).
const MaxDomainLen = 253

var (
	// Codigo de celda: minusculas, digitos y guiones entre ellos ("pe-01", "eu-west-1"). No lleva
	// puntos: el token del webmail separa la celda del secreto con uno.
	codeRe  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	labelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ValidCode dice si code tiene forma de codigo de celda.
func ValidCode(code string) bool {
	return len(code) <= MaxCodeLen && codeRe.MatchString(code)
}

// NormalizeDomain deja un dominio de correo en minusculas y sin espacios alrededor y dice si es
// un nombre DNS de al menos dos etiquetas en ASCII (los IDN llegan en punycode). No quita el
// punto final: "acme.test." no es el nombre de ningun dominio dado de alta.
func NormalizeDomain(raw string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" || len(name) > MaxDomainLen {
		return "", false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, label := range labels {
		if !labelRe.MatchString(label) {
			return "", false
		}
	}
	return name, true
}
