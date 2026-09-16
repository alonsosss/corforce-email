package domain

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Formato que entiende ratelimit.lua de Rspamd: "N / 1h" con unidad s, m, h o d.
var rateLimitValue = regexp.MustCompile(`^\d+ / 1[smhd]$`)

// Caracteres admitidos en un patron de lista: direccion, '@dominio' o comodin '*'. El
// patron acaba dentro de una expresion regular en el UCL de Rspamd, por eso se acota
// aqui y no al generar.
var listPattern = regexp.MustCompile(`^[a-z0-9._%+@*-]+$`)

var domainName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

var dkimSelector = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ValidateRateLimitValue rechaza cualquier valor que ratelimit.lua no sabria leer.
func ValidateRateLimitValue(value string) error {
	if !rateLimitValue.MatchString(value) {
		return newValidation(`value debe tener el formato "N / 1h" (unidades s, m, h, d)`)
	}
	return nil
}

// ValidateListPattern acepta una direccion, '@dominio' o un comodin con '*'.
func ValidateListPattern(pattern string) error {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" || p == "*" || p == "@" || !listPattern.MatchString(p) {
		return newValidation("pattern debe ser una direccion, @dominio o un comodin con *")
	}
	if strings.Count(p, "@") > 1 {
		return newValidation("pattern solo admite una @")
	}
	return nil
}

// ValidateSettingsMapContent impide que un bloque adicional cierre el settings { } que
// lo envuelve o abra otro: cualquiera de las dos cosas rompe el UCL de toda la celda.
func ValidateSettingsMapContent(content string) error {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return newValidation("content no puede estar vacio")
	}
	if strings.Contains(strings.ToLower(strings.Join(strings.Fields(trimmed), " ")), "settings {") {
		return newValidation("content no puede contener un bloque settings { }")
	}
	depth := 0
	for _, r := range trimmed {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return newValidation("content cierra una llave que no abrio")
			}
		}
	}
	if depth != 0 {
		return newValidation("content tiene llaves desbalanceadas")
	}
	return nil
}

// ValidateQuarantineMaxSize acota el tamano de mensaje que una empresa guarda en
// cuarentena a lo que /pipe llega a recibir: por encima de MaxQuarantineMaxSizeBytes el
// ajuste no guardaria nada mas y taparia el motivo real de que un mensaje falte.
func ValidateQuarantineMaxSize(bytes int64) error {
	if bytes <= 0 || bytes > MaxQuarantineMaxSizeBytes {
		return newValidation(fmt.Sprintf("max_size_bytes debe estar entre 1 y %d, el mayor mensaje que los motores entregan a la cuarentena", MaxQuarantineMaxSizeBytes))
	}
	return nil
}

// ValidateQuarantineRetentionSize acota las filas que la celda guarda por buzon. Cero es valido:
// no guardar nada mas que el ultimo mensaje es una decision de la empresa.
func ValidateQuarantineRetentionSize(size int) error {
	if size < 0 || size > MaxQuarantineRetentionSize {
		return newValidation(fmt.Sprintf("retention_size debe estar entre 0 y %d mensajes por buzon, lo que la celda guarda y deja revisar", MaxQuarantineRetentionSize))
	}
	return nil
}

// ValidateQuarantineMaxAgeDays acota cuanto conserva la celda un mensaje en cuarentena.
func ValidateQuarantineMaxAgeDays(days int) error {
	if days <= 0 || days > MaxQuarantineMaxAgeDays {
		return newValidation(fmt.Sprintf("max_age_days debe estar entre 1 y %d dias, lo que la celda conserva el correo en cuarentena", MaxQuarantineMaxAgeDays))
	}
	return nil
}

// ValidateQuarantineExcludeDomains acota cuantos dominios excluye una empresa: la lista acaba en
// una clave de Redis compartida por toda la celda.
func ValidateQuarantineExcludeDomains(n int) error {
	if n > MaxQuarantineExcludeDomains {
		return newValidation(fmt.Sprintf("exclude_domains admite hasta %d dominios", MaxQuarantineExcludeDomains))
	}
	return nil
}

// NormalizeHost acepta una IP o un CIDR y devuelve siempre la forma CIDR canonica.
func NormalizeHost(host string) (string, error) {
	h := strings.TrimSpace(host)
	if ip := net.ParseIP(h); ip != nil {
		if ip.To4() != nil {
			return ip.String() + "/32", nil
		}
		return ip.String() + "/128", nil
	}
	if _, ipnet, err := net.ParseCIDR(h); err == nil {
		return ipnet.String(), nil
	}
	return "", newValidation("host debe ser una IP o un CIDR")
}

// ValidateDomainName acepta un nombre de dominio en minusculas.
func ValidateDomainName(domain string) error {
	if !domainName.MatchString(domain) || len(domain) > 253 {
		return newValidation("domain no es un nombre de dominio valido")
	}
	return nil
}

// ValidateDKIMSelector acota el selector a lo que cabe en un nombre DNS.
func ValidateDKIMSelector(selector string) error {
	if !dkimSelector.MatchString(selector) || len(selector) > 63 {
		return newValidation("selector no es valido")
	}
	return nil
}

// NormalizeAddress pasa a minusculas y quita la etiqueta +tag (local+tag@d -> local@d),
// como hacian los mapas dinamicos originales: los motores entregan la direccion tal cual
// llego en el sobre.
func NormalizeAddress(address string) string {
	a := strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(a, "@")
	if at <= 0 {
		return a
	}
	local := a[:at]
	if plus := strings.Index(local, "+"); plus > 0 {
		local = local[:plus]
	}
	return local + a[at:]
}

// SplitAddress separa local y dominio; ok es false si no hay una sola @ bien colocada.
func SplitAddress(address string) (local, domain string, ok bool) {
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return "", "", false
	}
	return address[:at], address[at+1:], true
}

// ObjectKindOf clasifica un objeto de politica: con @ es un buzon, sin @ un dominio.
func ObjectKindOf(object string) ObjectKind {
	if strings.Contains(object, "@") {
		return ObjectMailbox
	}
	return ObjectDomain
}

// ValidateObject acepta un buzon (user@dominio) o un dominio, en minusculas.
func ValidateObject(object string) error {
	if ObjectKindOf(object) == ObjectDomain {
		return ValidateDomainName(object)
	}
	local, domain, ok := SplitAddress(object)
	if !ok || !listPattern.MatchString(local) || strings.Contains(local, "*") || strings.Contains(local, "@") {
		return newValidation("object debe ser un buzon (user@dominio) o un dominio")
	}
	return ValidateDomainName(domain)
}

// ParseRateLimitInfo extrae nombre y hash del texto "nombre(hash)" que Rspamd deja en
// las opciones del simbolo RATELIMITED.
func ParseRateLimitInfo(info string) (name, hash string) {
	open := strings.Index(info, "(")
	if open <= 0 || !strings.HasSuffix(info, ")") {
		return info, ""
	}
	return info[:open], info[open+1 : len(info)-1]
}
