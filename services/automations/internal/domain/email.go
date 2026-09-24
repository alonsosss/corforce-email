package domain

import (
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"
)

// emailPattern exige local@dominio.tld con caracteres seguros: la misma forma que acepta
// transactional, para no dar por bueno aqui un remitente que alli se rechaza.
var emailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$`)

const (
	maxEmailLen = 320
	// MaxNameLen acota nombres de flujo y de remitente.
	MaxNameLen = 200
	// maxReasonLen acota los motivos guardados (errores de vecinos incluidos).
	maxReasonLen = 1000
)

// NormalizeEmail recorta y pasa el dominio a minusculas; la parte local se conserva.
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
	if len(email) > maxEmailLen || !emailPattern.MatchString(email) {
		return false
	}
	_, err := mail.ParseAddress(email)
	return err == nil
}

// TruncateReason acota un motivo a un tamano razonable para guardarlo y mostrarlo.
func TruncateReason(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxReasonLen {
		return s
	}
	return string([]rune(s)[:maxReasonLen])
}

// normalizeSender valida y normaliza un remitente: direccion obligatoria, nombre y
// respuesta opcionales. field prefija los mensajes de error.
func normalizeSender(field string, email, name, replyTo *string) error {
	*email = NormalizeEmail(*email)
	if !ValidEmail(*email) {
		return NewValidationError("%sfrom_email debe ser un correo válido", field)
	}
	*name = strings.TrimSpace(*name)
	if utf8.RuneCountInString(*name) > MaxNameLen {
		return NewValidationError("%sfrom_name admite como máximo %d caracteres", field, MaxNameLen)
	}
	*replyTo = NormalizeEmail(*replyTo)
	if *replyTo != "" && !ValidEmail(*replyTo) {
		return NewValidationError("%sreply_to debe ser un correo válido", field)
	}
	return nil
}
