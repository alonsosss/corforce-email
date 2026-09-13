package domain

import (
	"net/mail"
	"regexp"
	"strings"
)

// emailPattern exige una forma local@dominio.tld con caracteres seguros. Es mas estricta
// que RFC 5322 a proposito: las direcciones con comentarios, comillas o espacios son
// legales pero nunca llegan a un sistema transaccional por una via legitima.
var emailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$`)

// NormalizeEmail recorta y pasa el dominio a minusculas. La parte local se conserva:
// hay servidores que la distinguen por mayusculas.
func NormalizeEmail(email string) string {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return email
	}
	return email[:at] + "@" + strings.ToLower(email[at+1:])
}

// ValidEmail dice si la direccion cumple la forma exigida.
func ValidEmail(email string) bool {
	if len(email) > 320 {
		return false
	}
	if !emailPattern.MatchString(email) {
		return false
	}
	_, err := mail.ParseAddress(email)
	return err == nil
}

// DomainOf devuelve el dominio en minusculas de una direccion ya normalizada.
func DomainOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// FormatAddress arma "Nombre <email>" con las comillas y codificacion que exige RFC 5322
// para el nombre; sin nombre devuelve la direccion sola.
func FormatAddress(name, email string) string {
	if strings.TrimSpace(name) == "" {
		return email
	}
	return (&mail.Address{Name: name, Address: email}).String()
}
