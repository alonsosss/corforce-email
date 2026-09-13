package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// maxEmailLength es el limite de una direccion completa (RFC 5321).
const maxEmailLength = 320

// emailRegex es la misma forma que aceptan suppression y pkg/validate: una direccion que
// suppression no reconociera nunca se podria excluir de un envio.
var emailRegex = regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)

// NormalizeEmail deja la direccion como se guarda y se compara: sin espacios y en
// minusculas, igual que suppression, para que la baja de una direccion alcance al
// contacto escrito con otra capitalizacion.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > maxEmailLength || !emailRegex.MatchString(email) {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// EmailSHA256 es la huella con la que la evidencia de consentimiento de un titular
// borrado sigue pudiendo cotejarse con una direccion que se presente despues.
func EmailSHA256(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}
